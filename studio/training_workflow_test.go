package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func testDatasetArchive(t *testing.T, entries []struct {
	name  string
	value []byte
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	for _, entry := range entries {
		f, err := archive.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(entry.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDatasetArchiveAcceptedAndRejectsBadPaths(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	key := strings.Repeat("b", 32)
	clip := "review/" + key + ".wav"
	valid := []struct {
		name  string
		value []byte
	}{
		{"dataset.list", nil}, {"pending.list", []byte(clip + "|" + key + "|EN|\n")},
		{clip, autoSpokenFixture(16000, 5700, 2)},
		{"review.json", []byte(`{"` + clip + `":{"name":"Example.mp4","group":"` + key + `","startMs":350}}`)},
	}
	for _, name := range []string{"../escape.txt", "review/../poison.wav", "c:\\malformed", clip} {
		bad := append(append([]struct {
			name  string
			value []byte
		}{}, valid...), struct {
			name  string
			value []byte
		}{name, []byte("not a file")})
		cleanCall(t, h, "POST", "/api/training/import", testDatasetArchive(t, bad), http.StatusUnprocessableEntity)
	}
	children, _ := os.ReadDir(s.path("training"))
	if len(children) != 0 {
		t.Fatal("failed archives must not publish a project", children)
	}
	out := cleanCall(t, h, "POST", "/api/training/import?name=Review", testDatasetArchive(t, valid), 201).Body.Bytes()
	var project trainingProject
	if err := json.Unmarshal(out, &project); err != nil || len(project.Clips) != 1 {
		t.Fatal(err, string(out))
	}
	c := project.Clips[0]
	if c.Reference == "" || c.Use != "pending" || c.Language != "en" || c.SourceGroup != key ||
		c.SourceOffsetMS != 350 || c.SourceName != "Example.mp4" || c.EndMS != 2000 {
		t.Fatalf("import changed review metadata: %+v", c)
	}
	payload, _ := json.Marshal(c)
	wave := cleanCall(t, h, "POST", "/api/training/preview", payload, 200).Body.Bytes()
	if string(wave[:4]) != "RIFF" || len(wave) != 44+2*16000*2 {
		t.Fatal("preview must be an actual, playable cropped PCM WAV", len(wave))
	}
	c.StartMS = 390
	c.EndMS = 1040
	payload, _ = json.Marshal(c)
	cropped := cleanCall(t, h, "POST", "/api/training/preview", payload, 200).Body.Bytes()
	if len(cropped) != 44+650*32 {
		t.Fatal("preview did not use current boundaries", len(cropped))
	}
}

func TestGenerationTimingOnlyFirstSegmentDeterminesWarmStatus(t *testing.T) {
	load, first, last := 12.3, 1.1, 0.7
	cold := executionEvidence{Timings: &workerTiming{Load: &load, Generate: &first}}
	warm := executionEvidence{Timings: &workerTiming{Reused: true, Generate: &last}}
	timing := summarizeTiming([]executionEvidence{cold, warm}, 15)
	if timing.Resident || timing.Load == nil || *timing.Load != load || timing.Generate == nil || *timing.Generate != first+last {
		t.Fatalf("bad cold aggregate %+v", timing)
	}
	timing = summarizeTiming([]executionEvidence{warm, warm}, 2)
	if !timing.Resident || timing.Load != nil || timing.Generate == nil {
		t.Fatalf("bad warm aggregate %+v", timing)
	}
	timing = summarizeTiming([]executionEvidence{cold, {}}, 15)
	if timing.Generate != nil || timing.Load == nil {
		t.Fatalf("missing metrics were invented %+v", timing)
	}
}

func TestProfileMultipleFineTunesKeepLegacyDefault(t *testing.T) {
	idA, idB := id(), id()
	p := voiceProfile{ModelProject: idA, ModelProjects: []string{idB, idA, idB, "invalid"}}
	p.normalizeModels()
	if p.ModelProject != idA || len(p.ModelProjects) != 2 || p.ModelProjects[0] != idA || p.ModelProjects[1] != idB {
		t.Fatalf("bad default normalization: %+v", p)
	}
}
