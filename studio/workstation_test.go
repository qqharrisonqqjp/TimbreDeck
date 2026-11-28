package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestJobReadSurvivesNewSlotAndKeepsPinnedModel(t *testing.T) {
	s := &studio{}
	p := &stsProcess{pinned: true, instance: id()}
	s.stsWorker = p
	_, first, _ := s.reserve("seed-vc-tiny", "sts")
	s.finish(nil)
	_, second, ok := s.reserve("cosyvoice3", "install")
	if !ok || s.stsWorker != p {
		t.Fatal("installation changed a pinned STS")
	}
	r := httptest.NewRequest("GET", "/api/jobs/"+first.ID, nil)
	r.SetPathValue("id", first.ID)
	w := httptest.NewRecorder()
	s.jobByID(w, r)
	var returned job
	if err := json.Unmarshal(w.Body.Bytes(), &returned); err != nil || returned.ID != first.ID || returned.Status != "done" {
		t.Fatalf("original job status lost when %s started: %s", second.ID, w.Body.String())
	}
	s.finish(nil)
	w = httptest.NewRecorder()
	if s.prepareAccelerator(w, "old", nil) || w.Code != 409 || s.stsWorker != p {
		t.Fatal("stale confirmation touched a pinned process")
	}
	if s.chooseCleaningDevice() != "cpu" || s.stsWorker != p {
		t.Fatal("CPU cleaning should preserve the pinned process")
	}
}

func TestTrainingASRBatchesKeepCompletedTranscript(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := fmt.Sprintf("%064x", 1)
	if err = os.WriteFile(s.path("voices", ref+".wav"), autoSpokenFixture(16000, 4500, 1), 0600); err != nil {
		t.Fatal(err)
	}
	p := trainingProject{ID: id(), Name: "Test batch", Revision: 1}
	for i := 0; i < 300; i++ {
		p.Clips = append(p.Clips, trainingClip{ID: id(), Reference: ref, StartMS: 0, EndMS: 500, Language: "en", Use: "pending"})
	}
	if err = os.Mkdir(s.path("training", p.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(s.path("training", p.ID, "project.json"), p); err != nil {
		t.Fatal(err)
	}
	ctx, j, _ := s.reserve("gpt-sovits", "transcription")
	count := 0
	recognize := func(_ context.Context, paths []string, lang, model string) ([]asrResult, error) {
		count++
		if lang != "en" || len(paths) > asrBatchLimit || filepath.Ext(paths[0]) != ".wav" {
			t.Fatalf("bad ASR batch %d", len(paths))
		}
		if count == 2 {
			partial, _ := s.loadTraining(p.ID)
			if len(paths) != 44 || partial.Clips[255].Transcript != "Test line" || partial.Revision != 2 {
				t.Fatal("first batch was not saved before the second")
			}
			return nil, errors.New("second batch fixture failure")
		}
		rows := make([]asrResult, len(paths))
		for i := range rows {
			rows[i].Text = "Test line"
		}
		return rows, nil
	}
	err = s.transcribeUsing(ctx, j.ID, "", "", "", p, needsTranscription, recognize)
	s.finish(err)
	actual, err := s.loadTraining(p.ID)
	if err != nil || count != 2 || actual.Revision != 3 || actual.Clips[256].Transcript != "" || actual.Clips[256].ASRStatus == "" || s.active.Warning == "" {
		t.Fatalf("retry lost successful or failed state: batches=%d status=%+v err=%v", count, s.active, err)
	}
}
