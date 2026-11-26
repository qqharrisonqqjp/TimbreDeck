package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const cleanTestHost = "127.0.0.1:4242"

func cleanCall(t *testing.T, handler http.Handler, method, path string, body []byte, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://"+cleanTestHost+path, bytes.NewReader(body))
	r.Header.Set("X-TimbreDeck", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: want %d, got %d %s", method, path, status, w.Code, w.Body)
	}
	return w
}

func setupClean(t *testing.T) (*studio, http.Handler, []string, [][]byte, string) {
	t.Helper()
	s, err := openStudio(filepath.Join(t.TempDir(), "聲台"))
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	first, second := toneFixture(), toneFixture()
	second[len(second)-2] = 7
	var refs []string
	for _, audio := range [][]byte{first, second} {
		got := cleanCall(t, h, "POST", "/api/reference", audio, 201)
		var ref struct{ ID string }
		if err := json.Unmarshal(got.Body.Bytes(), &ref); err != nil || !hexID(ref.ID, 64) {
			t.Fatalf("invalid reference: %+v (%v)", ref, err)
		}
		refs = append(refs, ref.ID)
	}
	engine := s.path("configured runtime")
	models := s.path("noise models")
	for _, file := range []string{
		filepath.Join(engine, "runtime", "python.exe"),
		filepath.Join(models, "HP2_all_vocals.pth"),
		filepath.Join(models, "speech_frcrn_ans_cirm_16k", "configuration.json"),
		filepath.Join(models, "speech_frcrn_ans_cirm_16k", "pytorch_model.bin"),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(s.path("config", "engines.json"), map[string]string{"gpt-sovits": engine}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIMBREDECK_CLEAN_ASSETS", models)
	return s, h, refs, [][]byte{first, second}, engine
}

func postCleanFixture(t *testing.T, s *studio, req cleanRequest, runner cleanRunner) job {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.startClean(w, r, runner) })
	w := cleanCall(t, h, "POST", "/api/clean", body, 202)
	var j job
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || j.Kind != "clean" || j.Engine != "gpt-sovits" || !hexID(j.ID, 32) {
		t.Fatalf("invalid job: %+v (%v)", j, err)
	}
	return j
}

func awaitClean(t *testing.T, h http.Handler, key, status string) job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w := cleanCall(t, h, "GET", "/api/job", nil, 200)
		var j job
		if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || j.ID != key {
			t.Fatalf("invalid job state: %+v (%v)", j, err)
		}
		if j.Status == status {
			return j
		}
		if j.Status != "running" && j.Status != "cancelling" {
			t.Fatalf("expected %s, got %+v", status, j)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job did not reach %s", status)
	return job{}
}

func cleanFixtureWAV(frames int) []byte {
	wav := make([]byte, 44+frames*2)
	copy(wav, toneFixture()[:44])
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	binary.LittleEndian.PutUint32(wav[24:], cleanSampleRate)
	binary.LittleEndian.PutUint32(wav[28:], cleanSampleRate*2)
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
	return wav
}

func TestCleanReferencesAndHistory(t *testing.T) {
	s, h, refs, originals, engine := setupClean(t)
	if got := cleanCall(t, h, "GET", "/api/clean/history", nil, 200).Body.String(); got != "[]\n" {
		t.Fatal(got)
	}
	want := cleanFixtureWAV((9*1000 + 2*125) * cleanSampleRate / 1000)
	requested := []string{refs[1], refs[0], refs[1]}
	tasks := make(chan cleanTask, 1)
	runner := func(_ context.Context, task cleanTask, work, _ string) error {
		tasks <- task
		if err := os.WriteFile(filepath.Join(work, "temporary"), []byte("temp"), 0600); err != nil {
			return err
		}
		return os.WriteFile(task.Output, want, 0600)
	}
	j := postCleanFixture(t, s, cleanRequest{requested, "both", 125}, runner)
	awaitClean(t, h, j.ID, "done")
	task := <-tasks
	var order []string
	for _, key := range requested {
		order = append(order, s.path("voices", key+".wav"))
	}
	if task.Mode != "both" || task.GapMS != 125 || task.EngineRoot != engine ||
		task.Assets != os.Getenv("TIMBREDECK_CLEAN_ASSETS") || !slices.Equal(task.Inputs, order) ||
		!filepath.IsAbs(task.Output) || slices.Contains(order, task.Output) {
		t.Fatalf("unexpected worker contract: %+v", task)
	}
	if _, err := os.Stat(task.Output); !os.IsNotExist(err) {
		t.Fatalf("staging still present: %v", err)
	}
	for i, key := range refs {
		got, err := os.ReadFile(s.path("voices", key+".wav"))
		if err != nil || !bytes.Equal(got, originals[i]) {
			t.Fatalf("reference %d changed: %v", i, err)
		}
	}
	got := cleanCall(t, h, "GET", "/api/clean/audio/"+j.ID+"?download=1", nil, 200)
	if !bytes.Equal(got.Body.Bytes(), want) || got.Header().Get("Content-Type") != "audio/wav" ||
		!strings.Contains(got.Header().Get("Content-Disposition"), ".wav") {
		t.Fatal("WAV download mismatch")
	}
	cleanCall(t, h, "GET", "/api/history", nil, 200)
	// Re-open the workspace to confirm history comes from persistent metadata.
	reopened, err := openStudio(s.root)
	if err != nil {
		t.Fatal(err)
	}
	data := cleanCall(t, reopened.routes(cleanTestHost), "GET", "/api/clean/history", nil, 200)
	var records []cleanRecording
	if err := json.Unmarshal(data.Body.Bytes(), &records); err != nil || len(records) != 1 ||
		records[0].Count != 3 || records[0].GapMS != 125 || records[0].Mode != "both" ||
		records[0].ID != j.ID || records[0].Seconds != 9.25 {
		t.Fatalf("incorrect persisted metadata: %+v (%v)", records, err)
	}
}

func TestCleanCancelAndPartialResults(t *testing.T) {
	s, h, refs, _, _ := setupClean(t)
	started := make(chan struct{})
	runner := func(ctx context.Context, task cleanTask, _, _ string) error {
		if err := os.WriteFile(task.Output, cleanFixtureWAV(3*cleanSampleRate), 0600); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		// Even a worker that writes a valid result before cancel may not publish it.
		return nil
	}
	request := cleanRequest{refs[:1], "denoise", 0}
	j := postCleanFixture(t, s, request, runner)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	cleanCall(t, h, "POST", "/api/synthesize", []byte("{}"), 409)
	cleanCall(t, h, "POST", "/api/clean", []byte("{}"), 409)
	cleanCall(t, h, "GET", "/api/clean/audio/"+j.ID, nil, 404)
	cleanCall(t, h, "POST", "/api/cancel/"+j.ID, nil, 200)
	awaitClean(t, h, j.ID, "cancelled")
	cleanCall(t, h, "GET", "/api/clean/audio/"+j.ID, nil, 404)

	j = postCleanFixture(t, s, request, func(_ context.Context, task cleanTask, _, _ string) error {
		return os.WriteFile(task.Output, cleanFixtureWAV(1), 0600)
	})
	awaitClean(t, h, j.ID, "failed")
	cleanCall(t, h, "GET", "/api/clean/audio/"+j.ID, nil, 404)
	if got := cleanCall(t, h, "GET", "/api/clean/history", nil, 200).Body.String(); got != "[]\n" {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(s.path(cleanOutputDir))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary or partial output left behind: %v, %v", entries, err)
	}
}

func TestCleanRequestLimits(t *testing.T) {
	s, h, refs, _, _ := setupClean(t)
	for _, req := range []cleanRequest{
		{nil, "both", 0},
		{refs, "unknown", 0},
		{refs, "both", -1},
		{refs, "both", cleanGapLimit + 1},
		{slices.Repeat(refs[:1], cleanClipLimit+1), "both", 0},
		{[]string{"../audio.wav"}, "both", 0},
		{[]string{strings.Repeat("0", 64)}, "both", 0},
	} {
		b, _ := json.Marshal(req)
		cleanCall(t, h, "POST", "/api/clean", b, 422)
	}
	cleanCall(t, h, "POST", "/api/clean", []byte(`{"references":[],"mode":"both","gapMs":50.4}`), 400)
	cleanCall(t, h, "POST", "/api/clean", []byte(`{"references":[],"mode":"both","gapMs":0,"output":"x"}`), 400)
	cleanCall(t, h, "GET", "/api/clean/audio/not-an-id", nil, 404)

	longKey := strings.Repeat("1", 64)
	if err := os.WriteFile(s.path("voices", longKey+".wav"), cleanFixtureWAV(91*cleanSampleRate), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cleanRequest{[]string{longKey, longKey}, "denoise", 0})
	cleanCall(t, h, "POST", "/api/clean", b, 422)
	if err := os.Remove(filepath.Join(os.Getenv("TIMBREDECK_CLEAN_ASSETS"), "HP2_all_vocals.pth")); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(cleanRequest{refs, "uvr", 0})
	cleanCall(t, h, "POST", "/api/clean", b, 409)
}
