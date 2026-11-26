package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func autoSpokenFixture(rate, value int, seconds int) []byte {
	frameCount := rate * seconds
	wav := make([]byte, 44+2*frameCount)
	copy(wav, toneFixture()[:44])
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	binary.LittleEndian.PutUint32(wav[24:], uint32(rate))
	binary.LittleEndian.PutUint32(wav[28:], uint32(rate*2))
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))
	for i := rate / 4; i < frameCount-rate/4; i++ {
		level := int16(value)
		if i/(rate/300)%2 == 0 {
			level = -level
		}
		binary.LittleEndian.PutUint16(wav[44+2*i:], uint16(level))
	}
	return wav
}

func importAutoFixture(t *testing.T, h http.Handler, wav []byte) string {
	t.Helper()
	out := cleanCall(t, h, "POST", "/api/reference", wav, 201)
	var got struct{ ID string }
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || !hexID(got.ID, 64) {
		t.Fatalf("invalid import: %+v, %v", got, err)
	}
	return got.ID
}

func postAutoFixture(t *testing.T, s *studio, q autoDatasetRequest, recognizer autoDatasetASR, cleaner cleanRunner) job {
	t.Helper()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.startAutoDatasetWith(w, r, recognizer, cleaner)
	})
	out := cleanCall(t, h, "POST", "/api/datasets/auto", data, 202)
	var j job
	if err := json.Unmarshal(out.Body.Bytes(), &j); err != nil || j.Kind != "dataset" || !hexID(j.ID, 32) {
		t.Fatalf("invalid job: %+v, %v", j, err)
	}
	return j
}

func readAutoFixtureZip(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err := errors.Join(err, r.Close()); err != nil {
			t.Fatal(err)
		}
		files[f.Name] = data
	}
	return files
}

func TestAutoDatasetZIPAndHistory(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	if got := cleanCall(t, h, "GET", "/api/datasets/auto", nil, 200).Body.String(); got != "[]\n" {
		t.Fatal(got)
	}
	wav1, wav2 := autoSpokenFixture(44100, 4500, 2), autoSpokenFixture(16000, 7500, 3)
	key1, key2 := importAutoFixture(t, h, wav1), importAutoFixture(t, h, wav2)
	recognizer := func(_ context.Context, paths []string, language string) ([]asrResult, error) {
		if len(paths) != 2 || language != "yue" {
			return nil, errors.New("unexpected ASR request")
		}
		for _, path := range paths {
			if !strings.HasPrefix(path, s.path("cache")+string(filepath.Separator)) {
				return nil, errors.New("unmanaged ASR staging")
			}
			w, _, err := cleanWAV(path, cleanClipBytes)
			if err != nil || w.rate != autoDatasetRate || w.channels != 1 || w.bits != 16 {
				return nil, errors.New("incorrect ASR WAV")
			}
		}
		return []asrResult{
			{Text: "今天天氣好。\n保留 | 標點", Provider: "fixture"},
			{Text: "帶傘。", Provider: "fixture"},
		}, nil
	}
	j := postAutoFixture(t, s, autoDatasetRequest{[]string{key1, key2}, "yue", "廣東話聲線", false},
		recognizer, nil)
	awaitClean(t, h, j.ID, "done")
	out := cleanCall(t, h, "GET", "/api/datasets/auto/"+j.ID+"/download", nil, 200)
	if out.Header().Get("Content-Type") != "application/zip" || !strings.Contains(out.Header().Get("Content-Disposition"), ".zip") {
		t.Fatal("incorrect download headers")
	}
	members := readAutoFixtureZip(t, out.Body.Bytes())
	if len(members) != 5 {
		t.Fatalf("unexpected ZIP entry count: %d", len(members))
	}
	list := strings.Split(strings.TrimSuffix(string(members["dataset.list"]), "\n"), "\n")
	valid := regexp.MustCompile(`^wavs/[0-9a-f]{32}\.wav\|[0-9a-f]{32}\|YUE\|[^|]+$`)
	if len(list) != 2 || !valid.MatchString(list[0]) || !valid.MatchString(list[1]) ||
		!strings.HasSuffix(list[0], "今天天氣好。 保留 ｜ 標點") || !strings.HasSuffix(list[1], "帶傘。") {
		t.Fatalf("invalid dataset.list: %q", list)
	}
	for _, line := range list {
		path, _, _ := strings.Cut(line, "|")
		if len(members[path]) <= 44 || string(members[path][:4]) != "RIFF" {
			t.Fatalf("no segmented WAV at %s", path)
		}
	}
	wav := members["full.wav"]
	if len(wav) != 44+5*autoDatasetRate*2 || binary.LittleEndian.Uint32(wav[24:]) != autoDatasetRate {
		t.Fatal("merged WAV did not retain both sources")
	}
	first := int16(binary.LittleEndian.Uint16(wav[44+autoDatasetRate:]))
	second := int16(binary.LittleEndian.Uint16(wav[44+5*autoDatasetRate:]))
	if absInt(int(first)) >= absInt(int(second)) || first == 0 {
		t.Fatal("merged WAV lost the selected source order or original audio")
	}
	var metadata autoDatasetRecord
	if err := json.Unmarshal(members["manifest.json"], &metadata); err != nil || metadata.ID != j.ID ||
		metadata.Count != 2 || metadata.Seconds != 5 || metadata.Speaker != "廣東話聲線" ||
		metadata.Provider != "fixture" || !strings.Contains(list[0], "|"+metadata.SpeakerID+"|") {
		t.Fatalf("incorrect manifest: %+v (%v)", metadata, err)
	}
	reopened, err := openStudio(s.root)
	if err != nil {
		t.Fatal(err)
	}
	var history []autoDatasetRecord
	got := cleanCall(t, reopened.routes(cleanTestHost), "GET", "/api/datasets/auto", nil, 200)
	if err := json.Unmarshal(got.Body.Bytes(), &history); err != nil || len(history) != 1 || history[0].ID != j.ID {
		t.Fatalf("incorrect persisted history: %+v (%v)", history, err)
	}
	for id, want := range map[string][]byte{key1: wav1, key2: wav2} {
		original := cleanCall(t, h, "GET", "/api/reference/"+id, nil, 200)
		if !bytes.Equal(original.Body.Bytes(), want) {
			t.Fatalf("original changed: %s", id)
		}
	}
}

func absInt(x int) int { return max(x, -x) }

func TestAutoDatasetDenoiseCancelAndNoPartialOutput(t *testing.T) {
	s, h, _, _, engine := setupClean(t)
	wav := autoSpokenFixture(autoDatasetRate, 4500, 2)
	selection := importAutoFixture(t, h, wav)
	cleaned := make(chan cleanTask, 1)
	fakeClean := func(ctx context.Context, p cleanTask, _, _ string) error {
		cleaned <- p
		return os.WriteFile(p.Output, wav, 0600)
	}
	started := make(chan struct{})
	fakeASR := func(ctx context.Context, paths []string, _ string) ([]asrResult, error) {
		close(started)
		<-ctx.Done()
		return []asrResult{{Text: "should never publish", Provider: "fixture"}}, nil
	}
	q := autoDatasetRequest{[]string{selection}, "en", "", true}
	j := postAutoFixture(t, s, q, fakeASR, fakeClean)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("ASR did not start")
	}
	if task := <-cleaned; task.Mode != "denoise" || task.EngineRoot != engine ||
		len(task.Inputs) != 1 || task.Inputs[0] != s.path("voices", selection+".wav") {
		t.Fatalf("wrong denoise job: %+v", task)
	}
	cleanCall(t, h, "POST", "/api/clean", []byte("{}"), 409)
	cleanCall(t, h, "GET", "/api/datasets/auto/"+j.ID+"/download", nil, 404)
	cleanCall(t, h, "POST", "/api/cancel/"+j.ID, nil, 200)
	awaitClean(t, h, j.ID, "cancelled")
	cleanCall(t, h, "GET", "/api/datasets/auto/"+j.ID+"/download", nil, 404)

	q.Denoise = false
	j = postAutoFixture(t, s, q, func(_ context.Context, _ []string, _ string) ([]asrResult, error) {
		return []asrResult{{Provider: "fixture"}}, nil
	}, nil)
	awaitClean(t, h, j.ID, "failed")
	cleanCall(t, h, "GET", "/api/datasets/auto/"+j.ID+"/download", nil, 404)
	if got := cleanCall(t, h, "GET", "/api/datasets/auto", nil, 200).Body.String(); got != "[]\n" {
		t.Fatal(got)
	}
	for _, dir := range []string{autoDatasetDir, "cache"} {
		items, err := os.ReadDir(s.path(dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if strings.HasPrefix(item.Name(), autoDatasetWorkPrefix) {
				t.Fatal("leftover work directory", item.Name())
			}
		}
	}
}

func TestAutoDatasetValidationAndLongSpeech(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	for _, data := range []string{
		`{"references":[],"language":"en"}`,
		`{"references":["../other"],"language":"en"}`,
		`{"references":["` + strings.Repeat("0", 64) + `"],"language":"auto"}`,
		`{"references":["` + strings.Repeat("0", 64) + `"],"language":"yue","speaker":"wrong|name"}`,
	} {
		cleanCall(t, h, "POST", "/api/datasets/auto", []byte(data), 422)
	}
	cleanCall(t, h, "GET", "/api/datasets/auto/invalid/download", nil, 404)
	full := make([]int16, 26*autoDatasetRate)
	for i := range full {
		full[i] = 3000
	}
	split := splitAutoDatasetSpeech(full)
	if len(split) != 3 || split[0].start != 0 || split[len(split)-1].end != len(full) {
		t.Fatalf("unexpected cuts: %+v", split)
	}
	for i, span := range split {
		if span.end-span.start > 12*autoDatasetRate || (i > 0 && span.start != split[i-1].end) {
			t.Fatalf("continuous speech was lost or clip is too long: %+v", split)
		}
	}
	if len(splitAutoDatasetSpeech(make([]int16, autoDatasetRate))) != 0 {
		t.Fatal("silence should not enter recognition")
	}
}
