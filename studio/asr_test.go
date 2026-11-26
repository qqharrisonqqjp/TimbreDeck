package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASRRejectsPathAndUnknownLanguage(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(s.asrHandler)
	cleanCall(t, h, "POST", "/api/asr", []byte(`{"reference":"../../file","language":"en"}`), 404)
	cleanCall(t, h, "POST", "/api/asr", []byte(`{"reference":"bogus","language":"xx-unsupported"}`), 400)
	cleanCall(t, h, "POST", "/api/asr", []byte(`{"reference":"bogus","language":"en","path":"file.wav"}`), 400)
	key := strings.Repeat("a", 64)
	if err := os.WriteFile(s.path("voices", key+".wav"), []byte("not a WAV"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanCall(t, h, "POST", "/api/asr", []byte(`{"reference":"`+key+`","language":"en"}`), 422)
}

func TestASRLimitsAndPreCancellation(t *testing.T) {
	s := &studio{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.recognizeFiles(ctx, []string{"test.wav"}, "en"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := s.recognizeFiles(context.Background(), make([]string, 257), "en"); err == nil {
		t.Fatal("accepted too many inputs")
	}
}

// Opt-in smoke test with the actual packaged CPU models, not a mock recognizer.
func TestASRLocalModel(t *testing.T) {
	fixtures := os.Getenv("TIMBREDECK_ASR_TEST_FIXTURES")
	if fixtures == "" {
		t.Skip("set TIMBREDECK_ASR_TEST_FIXTURES, TIMBREDECK_ASR_ENGINE_ROOT, and TIMBREDECK_ASR_ASSETS")
	}
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	for _, tc := range []struct{ file, language, provider, contains string }{
		{"synthetic-english.wav", "en", "faster-whisper", "offline speech recognition"},
		{"synthetic-cantonese.wav", "yue", "sensevoice", "我哋"},
	} {
		wav, err := os.ReadFile(filepath.Join(fixtures, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		w := cleanCall(t, h, "POST", "/api/reference", wav, 201)
		var saved struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || len(saved.ID) != 64 {
			t.Fatalf("invalid import: %s (%v)", w.Body.String(), err)
		}
		body, err := json.Marshal(asrRequest{saved.ID, tc.language})
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 2; index++ {
			w = cleanCall(t, h, "POST", "/api/asr", body, 200)
			var got asrResult
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil ||
				got.Provider != tc.provider || got.Language != tc.language || got.RequestedLanguage != tc.language ||
				!strings.Contains(got.Text, tc.contains) || got.Cached != (index == 1) || got.Warning != "" {
				t.Fatalf("unexpected recognition result: %+v (%v)", got, err)
			}
			t.Logf("%s[%d] %s", tc.language, index, w.Body.String())
		}
	}
}
