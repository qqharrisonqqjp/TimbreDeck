package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestSTSPreviewRecordedPCM(t *testing.T) {
	s, e := openStudio(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	key := id()
	buf := newSTSPreview(key)
	s.sts = buf
	payload := make([]byte, 8)
	copy(payload, "TD01")
	binary.LittleEndian.PutUint32(payload[4:], 24000)
	if _, e = buf.Write(payload); e != nil {
		t.Fatal(e)
	}
	if _, e = buf.Write([]byte{1, 2, 3, 4}); e != nil {
		t.Fatal(e)
	}
	buf.close(nil)
	r := httptest.NewRequest("GET", "/api/sts/"+key+"/stream", nil)
	r.SetPathValue("id", key)
	w := httptest.NewRecorder()
	s.streamSTS(w, r)
	if w.Code != 200 || w.Header().Get("X-Sample-Rate") != "24000" || !bytes.Equal(w.Body.Bytes(), []byte{1, 2, 3, 4}) {
		t.Fatal(w)
	}
	r.SetPathValue("id", id())
	w = httptest.NewRecorder()
	s.streamSTS(w, r)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestSTSProcessProbe(t *testing.T) {
	if os.Getenv("TIMBREDECK_STS_TEST_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
func TestOnlyAcceleratorWorkEvictsIdleTemporarySTS(t *testing.T) {
	s, e := openStudio(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	c := exec.Command(os.Args[0], "-test.run=^TestSTSProcessProbe$")
	c.Env = append(os.Environ(), "TIMBREDECK_STS_TEST_CHILD=1")
	input, e := c.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Start(); e != nil {
		t.Fatal(e)
	}
	p := &stsProcess{cmd: c, input: input, exited: make(chan struct{})}
	go func() { _ = c.Wait(); close(p.exited) }()
	s.stsWorker = p
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, ok := s.reserveLocked("gpt-sovits", "install")
	if !ok || s.stsWorker != p {
		t.Fatal("installation must preserve resident STS")
	}
	s.finishLocked(nil)
	if !s.prepareAccelerator(nil, "", nil) || s.stsWorker != nil || c.ProcessState == nil {
		t.Fatal("accelerator work must first unload a temporary resident")
	}
}
