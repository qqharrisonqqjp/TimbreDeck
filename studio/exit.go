package main

import "net/http"

func (s *studio) exiting() bool {
	select {
	case <-s.exit:
		return true
	default:
		return false
	}
}

// Only the shutdown request bypasses the shared gate. Holding it exclusively
// prevents a request from reserving work or starting a file write after approval.
// An idle shutdown additionally checks the last activity and keeps pinned models.
func (s *studio) beginExit(quiet func() bool) bool {
	if !s.requestGate.TryLock() {
		return false
	}
	defer s.requestGate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exiting() {
		return true
	}
	if s.busy() || len(asrActive) != 0 {
		return false
	}
	if quiet != nil {
		worker := s.residentLocked()
		if !quiet() || worker != nil && worker.pinned {
			return false
		}
	}
	s.stopSTSWorkerLocked()
	close(s.exit)
	return true
}

func (s *studio) requestExit(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "http://"+r.Host {
		fail(w, http.StatusForbidden, "請在本機工作室確認結束。")
		return
	}
	if !s.beginExit(nil) {
		fail(w, http.StatusConflict, "有作業、儲存或檔案傳輸進行中。請等完成，再結束工作室。")
		return
	}
	send(w, http.StatusOK, map[string]bool{"stopping": true})
}
