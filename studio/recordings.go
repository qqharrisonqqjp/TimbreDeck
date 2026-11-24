package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type unusedReference struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

func (s *studio) unusedReferences() ([]unusedReference, error) {
	used := map[string]bool{}
	var draft settings
	if err := readJSON(s.path("config", "drafts.json"), &draft); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	used[draft.Reference] = true
	// Engine drafts also keep the secondary emotion recording.
	for _, data := range draft.Drafts {
		var refs struct{ Reference, EmotionReference, TranscriptFor, RefLangFor string }
		if err := json.Unmarshal(data, &refs); err != nil {
			return nil, err
		}
		used[refs.Reference] = true
		used[refs.EmotionReference] = true
		used[refs.TranscriptFor] = true
		used[refs.RefLangFor] = true
	}
	profiles, err := os.ReadDir(s.path("voices", "profiles"))
	if err != nil {
		return nil, err
	}
	for _, entry := range profiles {
		key, ok := strings.CutSuffix(entry.Name(), ".json")
		if !entry.IsDir() && ok && hexID(key, 32) {
			profile, err := s.readProfile(key)
			if err != nil {
				return nil, err
			}
			used[profile.Reference] = true
		}
	}
	projects, err := os.ReadDir(s.path("training"))
	if err != nil {
		return nil, err
	}
	for _, entry := range projects {
		if entry.IsDir() && hexID(entry.Name(), 32) {
			project, err := s.loadTraining(entry.Name())
			if err != nil {
				return nil, err
			}
			for _, clip := range project.Clips {
				used[clip.Reference] = true
			}
		}
	}
	outputs, err := os.ReadDir(s.path("outputs"))
	if err != nil {
		return nil, err
	}
	for _, entry := range outputs {
		if entry.IsDir() && hexID(entry.Name(), 32) {
			var record recording
			if err := readJSON(s.path("outputs", entry.Name(), "record.json"), &record); err != nil {
				return nil, err
			}
			used[record.Input.Reference] = true
			used[record.Input.EmotionReference] = true
		}
	}
	files, err := os.ReadDir(s.path("voices"))
	if err != nil {
		return nil, err
	}
	result := []unusedReference{}
	for _, entry := range files {
		key, ok := strings.CutSuffix(entry.Name(), ".wav")
		if !ok || !hexID(key, 64) || used[key] {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			result = append(result, unusedReference{key, info.Size()})
		}
	}
	return result, nil
}

func (s *studio) listUnusedRecordings(w http.ResponseWriter, _ *http.Request) {
	s.files.Lock()
	defer s.files.Unlock()
	items, err := s.unusedReferences()
	if err != nil {
		fail(w, 500, "未能核對錄音引用："+err.Error())
		return
	}
	send(w, 200, items)
}

func (s *studio) removeUnusedRecording(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !hexID(key, 64) {
		fail(w, 400, "錄音編號無效。")
		return
	}
	if !s.beginRemoval(w) {
		return
	}
	defer s.endRemoval()
	items, err := s.unusedReferences()
	if err != nil {
		fail(w, 500, "未能核對錄音引用："+err.Error())
		return
	}
	for _, item := range items {
		path := s.path("voices", key+".wav")
		if item.ID == key && s.managedPath(path) {
			if err := os.Remove(path); err != nil {
				fail(w, 500, err.Error())
				return
			}
			send(w, 200, map[string]string{"id": key})
			return
		}
	}
	fail(w, 409, "只可移除未被草稿、角色、訓練或歷史引用的錄音。")
}

func (s *studio) removeOutputAudio(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !hexID(key, 32) {
		fail(w, 400, "輸出編號無效。")
		return
	}
	if !s.beginRemoval(w) {
		return
	}
	defer s.endRemoval()
	dir := s.path("outputs", key)
	path := filepath.Join(dir, "speech.wav")
	var record recording
	if !s.managedPath(path) || readJSON(filepath.Join(dir, "record.json"), &record) != nil || record.ID != key {
		fail(w, 404, "找不到可移除的音檔。")
		return
	}
	if err := os.Remove(path); err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, 200, map[string]string{"id": key})
}
