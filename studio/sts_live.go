package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func (s *studio) stsReady() (root, model string) {
	s.files.Lock()
	defer s.files.Unlock()
	paths, e := s.paths()
	if e != nil {
		return "", ""
	}
	const v = "Fun-CosyVoice3-0.5B"
	x := s.inspectSelected("cosyvoice3", paths)
	if !x.Ready {
		return "", ""
	}
	return x.variantPaths[v], x.modelPaths[v]
}
func (s *studio) stsPacket(ctx context.Context, root, model string, data map[string]any, state *stsPreview) error {
	src, e := assets.ReadFile("worker.py")
	if e != nil {
		return e
	}
	modelDir := ""
	if root != model {
		modelDir = model
	}
	data["modelRoot"] = modelDir
	payload, e := json.Marshal(data)
	if e != nil {
		return e
	}
	env := append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1",
		"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "PATH="+filepath.Join(root, "runtime")+string(os.PathListSeparator)+root+string(os.PathListSeparator)+os.Getenv("PATH"))
	return s.runSTSWorker(ctx, root, model, src, env, payload, state, io.Discard)
}
func (s *studio) prepareSTS(w http.ResponseWriter, id, root, model, replace string) bool {
	key := root + "\x00" + id
	if id == defaultSTS {
		key = root + "\x00" + model
	}
	return s.prepareAccelerator(w, replace, func(p *stsProcess) bool { return p.tts == nil && p.key == key })
}

func (s *studio) warmSTS(w http.ResponseWriter, r *http.Request) {
	var q struct{ Replace string }
	if r.ContentLength != 0 && !decode(w, r, &q) {
		return
	}
	entry, root, model, err := s.resolveSTS(r.URL.Query().Get("model"))
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	s.mu.Lock()
	if s.busy() {
		s.mu.Unlock()
		fail(w, 409, "已有作業進行中。")
		return
	}
	if !s.prepareSTS(w, entry.ID, root, model, q.Replace) {
		s.mu.Unlock()
		return
	}
	ack, j, ok := s.reserveLocked(entry.ID, "sts-warm")
	s.mu.Unlock()
	if !ok {
		fail(w, 409, "已有作業進行中。")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(ack, 3*time.Minute)
		defer cancel()
		dir, e := os.MkdirTemp(s.path("cache"), ".sts-prewarm-")
		if e == nil {
			defer os.RemoveAll(dir)
			wav := filepath.Join(dir, "calibration.wav")
			if e = os.WriteFile(wav, syntheticSTSCalibration(), 0600); e == nil {
				e = s.dispatchSTSPacket(ctx, entry, root, model, map[string]any{"prepare": true, "calibration": wav}, newSTSPreview(j.ID))
			}
		}
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e == nil && s.stsWorker != nil {
			s.stsWorker.pinned = true
		}
		s.finishLocked(e)
	}()
	send(w, 202, j)
}

func (s *studio) workerSTS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == "POST" {
		var q struct{ ID string }
		if r.ContentLength != 0 && !decode(w, r, &q) {
			return
		}
		if s.ttsLocked() {
			fail(w, 409, "請在 TTS 工作區停止常駐模型。")
			return
		}
		if s.busy() {
			fail(w, 409, "請先停止咪高峰或取消 STS 作業，再卸載模型。")
			return
		}
		if q.ID != "" && (s.stsWorker == nil || s.stsWorker.instance != q.ID || !s.stsWorker.pinned) {
			fail(w, 409, "常駐模型已改變，請重新確認。")
			return
		}
		s.stopSTSWorkerLocked()
	}
	ready, pinned, model := false, false, ""
	if p := s.stsWorker; p != nil && p.tts == nil {
		select {
		case <-p.exited:
		default:
			ready = true
			pinned = p.pinned
			model = p.model
		}
	}
	send(w, 200, map[string]any{"resident": ready, "pinned": pinned, "model": model})
}
func (s *studio) liveSTS(w http.ResponseWriter, r *http.Request) {
	entry, root, model, err := s.resolveSTS(r.URL.Query().Get("model"))
	key := r.URL.Query().Get("reference")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if !entry.Mic {
		fail(w, 422, "此模型只開放錄音轉換；咪高峰尚未驗證。")
		return
	}
	var session string
	var seq int
	if entry.ID == seedSTS {
		session = r.URL.Query().Get("session")
		seq, err = strconv.Atoi(r.URL.Query().Get("seq"))
		if !hexID(session, 32) || err != nil || seq < 0 || seq > 1000000 {
			fail(w, 422, "Seed-VC 即時音訊缺少有效的收音作業編號。")
			return
		}
	}
	if !hexID(key, 64) {
		fail(w, 422, "請匯入目標音色。")
		return
	}
	ref := s.path("voices", key+".wav")
	secs, e := wavDuration(ref)
	if e != nil || !s.managedPath(ref) || secs < 2 || secs > 20 {
		fail(w, 422, "目標音色須為 2–20 秒。")
		return
	}
	dir, e := os.MkdirTemp(s.path("cache"), ".live-sts-")
	if e != nil {
		fail(w, 500, "未能開啟即時轉換。")
		return
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "microphone.wav")
	f, e := os.Create(input)
	if e != nil {
		fail(w, 500, "未能讀取咪高峰。")
		return
	}
	_, e = io.Copy(f, http.MaxBytesReader(w, r.Body, 256<<10))
	f.Close()
	wf, _, err := cleanWAV(input, 256<<10)
	if e != nil || err != nil || wf.rate != 16000 || wf.channels != 1 || wf.bits != 16 || wf.format != 1 {
		fail(w, 422, "即時音訊須為單聲道 16 kHz PCM16。")
		return
	}
	if entry.ID == seedSTS {
		if wf.size != seedBlockSamples*2 {
			fail(w, 422, "Seed-VC 每段收音須為 300 ms。")
			return
		}
	} else if wf.size < 32000 || wf.size > 128000 {
		fail(w, 422, "CosyVoice 即時音訊須為 1–4 秒。")
		return
	}
	s.mu.Lock()
	if s.busy() {
		s.mu.Unlock()
		fail(w, 409, "已有作業進行中。")
		return
	}
	if !s.prepareSTS(w, entry.ID, root, model, "") {
		s.mu.Unlock()
		return
	}
	ack, j, ok := s.reserveLocked(entry.ID, "sts-live")
	s.mu.Unlock()
	if !ok {
		fail(w, 409, "已有作業進行中。")
		return
	}
	ctx, cancel := context.WithTimeout(ack, 2*time.Minute)
	defer cancel()
	connection := context.AfterFunc(r.Context(), cancel)
	defer connection()
	stream := newSTSPreview(j.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e := s.dispatchSTSPacket(ctx, entry, root, model, map[string]any{"id": j.ID, "request": request{Engine: entry.ID, Mode: "voice-convert", Speed: 1}, "ref": ref, "sourceSpeech": input,
			"output": filepath.Join(dir, "preview.wav"), "diagnostics": filepath.Join(dir, "execution.json"), "liveSession": session, "liveSeq": seq}, stream)
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		s.finish(e)
		stream.close(e)
	}()
	serveSTS(w, r, stream)
	cancel()
	<-done
}
