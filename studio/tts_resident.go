package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
)

type ttsTarget struct {
	Engine, Variant, ModelProject string
	root, model, token            string
	trained                       *trainedModel
}

func newTTSTarget(q request, root, model string, trained *trainedModel) (*ttsTarget, error) {
	t := &ttsTarget{Engine: q.Engine, Variant: q.Variant, ModelProject: q.ModelProject,
		root: root, model: model, trained: trained}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00", q.Engine, q.Variant, q.ModelProject, root, model)
	if trained != nil {
		// A weight replaced in place must not silently reuse stale VRAM.
		for _, path := range []string{trained.GPT, trained.SoVITS} {
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(h, "%s\x00%d\x00%d\x00", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	t.token = fmt.Sprintf("tts:%x", h.Sum(nil))
	return t, nil
}

func (s *studio) residentLocked() *stsProcess {
	p := s.stsWorker
	if p != nil {
		select {
		case <-p.exited:
			s.stopSTSWorkerLocked()
			return nil
		default:
		}
	}
	return p
}

func (s *studio) ttsLocked() bool {
	p := s.residentLocked()
	return p != nil && p.tts != nil && p.pinned
}

type residentStatus struct {
	ID           string `json:"id,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Engine       string `json:"engine,omitempty"`
	Variant      string `json:"variant,omitempty"`
	ModelProject string `json:"modelProject,omitempty"`
	Busy         bool   `json:"busy"`
}

func (s *studio) residentStatusLocked() residentStatus {
	out := residentStatus{Busy: s.busy()}
	p := s.residentLocked()
	if p == nil || !p.pinned {
		return out
	}
	out.ID, out.Engine, out.Kind = p.instance, p.model, "sts"
	if t := p.tts; t != nil {
		out.Kind, out.Engine, out.Variant, out.ModelProject = "tts", t.Engine, t.Variant, t.ModelProject
	}
	return out
}

func (s *studio) ttsStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	send(w, http.StatusOK, s.residentStatusLocked())
}

func (s *studio) stopTTS(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID string }
	if !decode(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "請等目前作業完成，才停止常駐模型。")
		return
	}
	if p := s.residentLocked(); p != nil && p.pinned {
		if p.tts == nil || body.ID == "" || p.instance != body.ID {
			fail(w, 409, "常駐模型已改變，請重新確認。")
			return
		}
		s.stopSTSWorkerLocked()
	}
	send(w, http.StatusOK, s.residentStatusLocked())
}

func (s *studio) warmTTS(w http.ResponseWriter, r *http.Request) {
	var in struct{ Engine, Variant, ModelProject, Replace string }
	if !decode(w, r, &in) {
		return
	}
	e, ok := engineCatalog[in.Engine]
	if !ok || len(e.Variants) == 0 {
		fail(w, 422, "請選擇有效的 TTS 引擎。")
		return
	}
	if in.Variant == "" && (len(e.Variants) == 1 || in.Engine == "gpt-sovits") {
		in.Variant = e.Variants[0]
	}
	if !slices.Contains(e.Variants, in.Variant) || (in.ModelProject != "" && (in.Engine != "gpt-sovits" || in.Variant != "v2Pro")) {
		fail(w, 422, "TTS 模型選項無效。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "請等目前作業完成，才切換常駐模型。")
		return
	}
	q := request{Engine: in.Engine, Variant: in.Variant, ModelProject: in.ModelProject}
	var trained *trainedModel
	if in.ModelProject != "" {
		m, err := s.loadTrainedModel(in.ModelProject)
		if err != nil {
			fail(w, 422, "找不到可用的微調模型。")
			return
		}
		trained = &m
	}
	s.files.Lock()
	paths, err := s.paths()
	available := s.inspectSelected(in.Engine, paths)
	s.files.Unlock()
	root, model := available.variantPaths[in.Variant], available.modelPaths[in.Variant]
	if err != nil || !available.Ready || root == "" || model == "" {
		fail(w, 409, "請先連接所選 TTS 引擎及模型。")
		return
	}
	t, err := newTTSTarget(q, root, model, trained)
	if err != nil {
		fail(w, 422, "未能讀取微調權重。")
		return
	}
	if p := s.residentLocked(); p != nil && p.pinned {
		if p.tts != nil && p.key == t.token {
			send(w, 200, s.residentStatusLocked())
			return
		}
		if in.Replace == "" || in.Replace != p.instance {
			fail(w, 409, "已有常駐模型，請確認後先卸載舊模型。")
			return
		}
		s.stopSTSWorkerLocked()
	} else if in.Replace != "" {
		fail(w, 409, "常駐模型已改變，請重新確認。")
		return
	}
	ack, j, _ := s.reserveLocked(in.Engine, "tts-warm")
	go func() {
		ctx, cancel := context.WithTimeout(ack, 4*time.Minute)
		defer cancel()
		dir, err := os.MkdirTemp(s.path("cache"), ".tts-warm-*")
		if err == nil {
			defer os.RemoveAll(dir)
			rel := t.model
			if rel == t.root {
				rel = ""
			}
			var data []byte
			data, err = json.Marshal(map[string]any{"prepareTTS": true, "request": q, "trained": trained,
				"modelRoot": rel, "output": filepath.Join(dir, "warm.wav")})
			if err == nil {
				err = s.runTTSProcess(ctx, t, data, j.ID, io.Discard)
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if p := s.residentLocked(); err == nil && p != nil && p.key == t.token {
			p.pinned = true
		} else if err == nil {
			err = fmt.Errorf("常駐模型已結束")
		}
		s.finishLocked(err)
	}()
	send(w, 202, j)
}

func synthesisEnv(cmd *exec.Cmd, cache, root string) []string {
	return append(cmd.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1",
		"HF_HOME="+filepath.Join(cache, "huggingface"), "TORCH_HOME="+filepath.Join(cache, "torch"),
		"PATH="+filepath.Join(root, "runtime")+string(os.PathListSeparator)+root+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (s *studio) runTTSProcess(ctx context.Context, t *ttsTarget, data []byte, key string, trace io.Writer) error {
	src, _ := assets.ReadFile("worker.py")
	cmd := exec.Command(filepath.Join(t.root, "runtime", "python.exe"), "-u", "-c", string(src), "--tts-resident")
	cmd.Dir = t.root
	cmd.Env = synthesisEnv(cmd, s.path("cache"), t.root)
	return s.runSTSProcess(ctx, t.token, t.Engine, cmd, data, newSTSPreview(key), trace, t)
}
