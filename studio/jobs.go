package main

import "net/http"

func (s *studio) jobByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !hexID(id, 32) {
		fail(w, 404, "找不到作業。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.ID == id {
		send(w, 200, *s.active)
		return
	}
	for i := len(s.recent) - 1; i >= 0; i-- {
		if s.recent[i].ID == id {
			send(w, 200, s.recent[i])
			return
		}
	}
	fail(w, 404, "找不到此作業，或狀態已超出保留期限。已匯出的檔案仍可查看。")
}

// Caller holds s.mu and first verifies that the work slot is free. An
// explicit pin is never released by an unrelated request or a stale UI.
func (s *studio) prepareAccelerator(w http.ResponseWriter, expected string, matches func(*stsProcess) bool) bool {
	p := s.residentLocked()
	if expected != "" && (p == nil || !p.pinned || p.instance != expected) {
		fail(w, 409, "常駐模型已改變，請重新確認。")
		return false
	}
	if p == nil || matches != nil && matches(p) {
		return true
	}
	if p.pinned && expected == "" {
		fail(w, 409, "請先確認停止目前的常駐模型。")
		return false
	}
	s.stopSTSWorkerLocked()
	return true
}

// CPU cleaning can share a pinned model's lifetime without sharing its GPU.
func (s *studio) chooseCleaningDevice() string {
	if p := s.residentLocked(); p != nil {
		if p.pinned {
			return "cpu"
		}
		s.stopSTSWorkerLocked()
	}
	return ""
}
