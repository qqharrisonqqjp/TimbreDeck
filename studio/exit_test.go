package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExitOnlyFromLocalPageAndOnlyWhenIdle(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(cleanTestHost)
	local := "http://" + cleanTestHost
	call := func(method, origin, token string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, local+"/api/studio/exit", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-TimbreDeck", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s origin=%q: got %d need %d: %s", method, origin, w.Code, want, w.Body)
		}
	}
	call("GET", local, "1", 404)
	call("POST", "https://elsewhere.invalid", "1", 403)
	call("POST", "", "1", 403)
	call("POST", local, "", 403)
	ctx, _, ok := s.reserve("gpt-sovits", "synthesis")
	if !ok {
		t.Fatal("fixture job wasn't reserved")
	}
	call("POST", local, "1", 409)
	if s.exiting() || ctx.Err() != nil {
		t.Fatal("exit must not close or cancel a busy studio")
	}
	s.finish(nil)
	asrActive <- struct{}{}
	defer func() {
		if len(asrActive) != 0 {
			<-asrActive
		}
	}()
	call("POST", local, "1", 409)
	<-asrActive
	s.requestGate.RLock()
	call("POST", local, "1", 409)
	s.requestGate.RUnlock()
	call("POST", local, "1", 200)
	if !s.exiting() {
		t.Fatal("no exit signal")
	}
	cleanCall(t, h, "PUT", "/api/draft", []byte(`{"engine":"gpt-sovits"}`), http.StatusServiceUnavailable)
}
