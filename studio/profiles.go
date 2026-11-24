package main

import (
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

type voiceProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Reference    string `json:"reference"`
	Transcript   string `json:"transcript"`
	Language     string `json:"language"`
	ModelProject string `json:"modelProject,omitempty"`
}

func (s *studio) profilePath(id string) string {
	return s.path("voices", "profiles", id+".json")
}

func (s *studio) readProfile(id string) (voiceProfile, error) {
	var profile voiceProfile
	if !hexID(id, 32) {
		return profile, errors.New("角色編號無效。")
	}
	err := readJSON(s.profilePath(id), &profile)
	if err == nil && (profile.ID != id || !hexID(profile.Reference, 64)) {
		err = errors.New("角色資料無效。")
	}
	return profile, err
}

func (s *studio) profiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.path("voices", "profiles"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	profiles := []voiceProfile{}
	for _, entry := range entries {
		key, found := strings.CutSuffix(entry.Name(), ".json")
		if entry.IsDir() || !found || !hexID(key, 32) {
			continue
		}
		profile, err := s.readProfile(key)
		if err == nil {
			profiles = append(profiles, profile)
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Name != profiles[j].Name {
			return profiles[i].Name < profiles[j].Name
		}
		return profiles[i].ID < profiles[j].ID
	})
	send(w, 200, profiles)
}

func (s *studio) saveProfile(w http.ResponseWriter, r *http.Request) {
	var profile voiceProfile
	if !decode(w, r, &profile) {
		return
	}
	key := r.PathValue("id")
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Transcript = strings.TrimSpace(profile.Transcript)
	if profile.ID != "" && profile.ID != key {
		fail(w, 400, "角色編號與請求不符。")
		return
	}
	if profile.Name == "" || utf8.RuneCountInString(profile.Name) > 80 ||
		utf8.RuneCountInString(profile.Transcript) > 10000 || !language(profile.Language) ||
		!hexID(profile.Reference, 64) {
		fail(w, 422, "請填寫角色名稱、選擇錄音與語言。")
		return
	}
	if _, err := wavDuration(s.path("voices", profile.Reference+".wav")); err != nil {
		fail(w, 422, "找不到有效的參考錄音。")
		return
	}
	if profile.ModelProject != "" {
		if _, err := s.loadTrainedModel(profile.ModelProject); err != nil {
			fail(w, 422, "找不到選定的微調模型。")
			return
		}
	}
	s.files.Lock()
	defer s.files.Unlock()
	status := http.StatusCreated
	if key == "" {
		key = id()
	} else {
		if _, err := s.readProfile(key); err != nil {
			fail(w, 404, "找不到角色。")
			return
		}
		status = http.StatusOK
	}
	profile.ID = key
	if err := writeJSON(s.profilePath(key), profile); err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, status, profile)
}

func (s *studio) removeProfile(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !hexID(key, 32) {
		fail(w, 404, "找不到角色。")
		return
	}
	s.files.Lock()
	defer s.files.Unlock()
	if err := os.Remove(s.profilePath(key)); err != nil {
		if os.IsNotExist(err) {
			fail(w, 404, "找不到角色。")
		} else {
			fail(w, 500, err.Error())
		}
		return
	}
	send(w, 200, map[string]string{"id": key})
}

func (s *studio) resolveProfile(q *request) (string, error) {
	if q.VoiceID == "" {
		return "", nil
	}
	profile, err := s.readProfile(q.VoiceID)
	if err != nil {
		return "", errors.New("找不到選定的角色，請重新選擇。")
	}
	if q.Reference != "" && q.Reference != profile.Reference {
		return "", errors.New("角色的錄音已變更，請重新選擇或改用單次錄音。")
	}
	q.Reference = profile.Reference
	if strings.TrimSpace(q.Transcript) == "" {
		q.Transcript = profile.Transcript
	}
	if q.RefLang == "" {
		q.RefLang = profile.Language
	}
	return profile.Name, nil
}
