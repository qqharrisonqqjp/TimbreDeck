package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTTSCheckpointsChangeIdentity(t *testing.T) {
	d := t.TempDir()
	tuned := &trainedModel{GPT: filepath.Join(d, "s1.ckpt"), SoVITS: filepath.Join(d, "s2.pth")}
	for _, name := range []string{tuned.GPT, tuned.SoVITS} {
		if e := os.WriteFile(name, []byte("weights"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	q := request{Engine: "gpt-sovits", Variant: "v2Pro", ModelProject: id()}
	first, e := newTTSTarget(q, d, d, tuned)
	if e != nil {
		t.Fatal(e)
	}
	q.Text, q.Mode, q.Language = "different text", "clone", "en"
	next, e := newTTSTarget(q, d, d, tuned)
	if e != nil || first.token != next.token {
		t.Fatal("ordinary request edits reloaded a resident model", e)
	}
	if e := os.WriteFile(tuned.GPT, []byte("replacement weights"), 0600); e != nil {
		t.Fatal(e)
	}
	after, e := newTTSTarget(q, d, d, tuned)
	if e != nil || first.token == after.token {
		t.Fatal("replacement checkpoint reused the old resident", e)
	}
}

func TestTTSStopCannotInterruptRunningJob(t *testing.T) {
	s, e := openStudio(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := &stsProcess{tts: &ttsTarget{Engine: "gpt-sovits"}, pinned: true, instance: id()}
	s.stsWorker = p
	_, _, ok := s.reserve("gpt-sovits", "synthesis")
	if !ok {
		t.Fatal("job reservation failed")
	}
	w := httptest.NewRecorder()
	s.stopTTS(w, httptest.NewRequest("POST", "/api/tts/stop", strings.NewReader(`{"id":"`+p.instance+`"}`)))
	if w.Code != 409 || s.stsWorker != p {
		t.Fatalf("stop touched a running job: %d", w.Code)
	}
	s.finish(nil)
}
