package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// One bounded live PCM buffer; completed downloads still use ordinary WAV history.
const stsPCMCap = 4 << 20

type stsPreview struct {
	id      string
	mu      sync.Mutex
	pcm     []byte
	changed chan struct{}
	done    bool
	err     error
}

func newSTSPreview(key string) *stsPreview { return &stsPreview{id: key, changed: make(chan struct{})} }
func (s *stsPreview) notify()              { close(s.changed); s.changed = make(chan struct{}) }
func (s *stsPreview) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || len(s.pcm)+len(p) > stsPCMCap {
		return 0, errors.New("STS audio exceeds the live buffer limit")
	}
	s.pcm = append(s.pcm, p...)
	if len(s.pcm) >= 8 && (string(s.pcm[:4]) != "TD01" || binary.LittleEndian.Uint32(s.pcm[4:]) != 24000) {
		return 0, errors.New("invalid STS audio stream")
	}
	s.notify()
	return len(p), nil
}
func (s *stsPreview) close(err error) {
	s.mu.Lock()
	s.done, s.err = true, err
	s.notify()
	s.mu.Unlock()
}

func (s *studio) startSTS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source, Reference, Model, Replace string
		Streaming                         bool
	}
	if !decode(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "已有作業進行中。")
		return
	}
	for _, slot := range []struct {
		key, label string
		min, max   float64
	}{{body.Source, "來源錄音", 1, 25}, {body.Reference, "目標音色參考", 2, 20}} {
		if !hexID(slot.key, 64) {
			fail(w, 422, "請匯入"+slot.label+"。")
			return
		}
		p := s.path("voices", slot.key+".wav")
		if !s.managedPath(p) {
			fail(w, 422, "找不到"+slot.label+"。")
			return
		}
		f, _, e := cleanWAV(p, 32<<20)
		if e != nil || float64(f.size/f.block)/float64(f.rate) < slot.min || float64(f.size/f.block)/float64(f.rate) > slot.max {
			fail(w, 422, slot.label+"長度不符（來源 1–25 秒；目標參考 2–20 秒）。")
			return
		}
	}
	entry, root, model, e := s.resolveSTS(body.Model)
	if e != nil {
		code := 409
		if entry.ID == "" {
			code = 422
		}
		fail(w, code, e.Error())
		return
	}
	if body.Streaming && (entry.ID != seedSTS || !entry.Mic) {
		fail(w, 422, "未能啟用 Seed-VC Tiny 原生分幀。")
		return
	}
	if !s.prepareSTS(w, entry.ID, root, model, body.Replace) {
		return
	}
	variant := entry.ID
	if entry.ID == defaultSTS {
		variant = "Fun-CosyVoice3-0.5B"
	}
	ack, j, _ := s.reserveLocked(entry.ID, "sts")
	preview := newSTSPreview(j.ID)
	s.sts = preview
	q := request{Engine: entry.ID, Variant: variant, Mode: "voice-convert", SourceSpeech: body.Source, Reference: body.Reference, Speed: 1, STSStreaming: body.Streaming}
	go func() {
		ctx, cancel := context.WithTimeout(ack, 10*time.Minute)
		defer cancel()
		err := s.renderWithStream(ctx, j.ID, root, model, q, "", nil, preview)
		preview.close(err)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.stopSTSWorkerLocked()
		}
		s.finishLocked(err)
	}()
	send(w, 202, j)
}

func (s *studio) streamSTS(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	s.mu.Lock()
	stream := s.sts
	s.mu.Unlock()
	if !hexID(key, 32) || stream == nil || stream.id != key {
		http.NotFound(w, r)
		return
	}
	serveSTS(w, r, stream)
}
func serveSTS(w http.ResponseWriter, r *http.Request, stream *stsPreview) {
	flush := http.NewResponseController(w)
	offset := 8
	started := false
	for {
		stream.mu.Lock()
		done, err, wake := stream.done, stream.err, stream.changed
		var pcm []byte
		if len(stream.pcm) > offset && len(stream.pcm) >= 8 && string(stream.pcm[:4]) == "TD01" && binary.LittleEndian.Uint32(stream.pcm[4:]) == 24000 {
			end := len(stream.pcm) &^ 1
			pcm = stream.pcm[offset:end]
		}
		stream.mu.Unlock()
		if len(pcm) > 0 {
			if !started {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("X-Sample-Rate", strconv.Itoa(24000))
				w.Header().Set("X-Audio-Format", "pcm-s16le-mono")
				started = true
			}
			n, e := w.Write(pcm)
			if e != nil {
				return
			}
			offset += n
			if e = flush.Flush(); e != nil {
				return
			}
		}
		if done {
			if !started {
				if err != nil {
					fail(w, 422, "STS 未能輸出音訊。請查看作業狀態。")
				} else {
					fail(w, 422, "STS 回傳空音訊。")
				}
			}
			return
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return
		}
	}
}
