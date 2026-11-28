package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed sts_model_worker.py
var stsModelWorker []byte

const defaultSTS = "cosyvoice3"
const seedSTS = "seed-vc-tiny"
const seedBlockSamples = 4800

type stsModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Ready   bool   `json:"ready"`
	Mic     bool   `json:"mic"`
}

var stsCatalogue = []stsModel{
	{ID: defaultSTS, Name: "CosyVoice 3", Summary: "保留原有流式轉換及咪高峰。", Mic: true},
	{ID: "x-vc", Name: "X-VC", Summary: "以音質和速度平衡為目標，錄音轉換。"},
	{ID: seedSTS, Name: "Seed-VC Tiny", Summary: "25M 版本；整段錄音及 300 ms 原生分幀模式。"},
	{ID: "meanvc2", Name: "MeanVC 2", Summary: "使用 40 ms 模型及 40 ms 前瞻；不是整體延遲。"},
	{ID: "tvtsyn1", Name: "TVTSyn1", Summary: "實驗性 v1；上游只供非商業用途。"},
}

// A single configured asset root is shared by the desktop UI and the AI
// entrypoint. Per-request paths and executable names are not accepted.
func stsAssets() string {
	if root := os.Getenv("TIMBREDECK_STS_ASSETS"); filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "sts-models")
}

func stsPython(root string) string {
	var cfg struct{ Python string }
	if root == "" || readJSON(filepath.Join(root, "runtime.json"), &cfg) != nil || !filepath.IsAbs(cfg.Python) || !strings.EqualFold(filepath.Base(cfg.Python), "python.exe") {
		return ""
	}
	if st, e := os.Stat(cfg.Python); e != nil || !st.Mode().IsRegular() {
		return ""
	}
	return cfg.Python
}

func (s *studio) resolveSTS(id string) (entry stsModel, root, model string, err error) {
	if id == "" {
		id = defaultSTS
	}
	for _, x := range stsCatalogue {
		if x.ID == id {
			entry = x
			break
		}
	}
	if entry.ID == "" {
		return entry, "", "", errors.New("未有此 STS 模型。")
	}
	if id == defaultSTS {
		root, model = s.stsReady()
		entry.Ready = root != "" && model != ""
	} else {
		root = stsAssets()
		var stamp struct {
			Model string
			Files []string
		}
		entry.Ready = stsPython(root) != "" &&
			readJSON(filepath.Join(root, "verified", id+".json"), &stamp) == nil &&
			stamp.Model == id && len(stamp.Files) > 0
		for _, name := range stamp.Files {
			if !filepath.IsLocal(name) {
				entry.Ready = false
				break
			}
			if st, e := os.Stat(filepath.Join(root, name)); e != nil || !st.Mode().IsRegular() || st.Size() == 0 {
				entry.Ready = false
				break
			}
		}
		if entry.ID == seedSTS && entry.Ready {
			st, e := os.Stat(filepath.Join(root, "upstream", "seed-vc", "real-time-gui.py"))
			entry.Mic = e == nil && st.Mode().IsRegular() && st.Size() > 0
		}
		model = id
	}
	if !entry.Ready {
		return entry, "", "", errors.New(entry.Name + " 尚未完成安裝及驗證。")
	}
	return entry, root, model, nil
}

func (s *studio) listSTSModels(w http.ResponseWriter, r *http.Request) {
	list := make([]stsModel, 0, len(stsCatalogue))
	for _, x := range stsCatalogue {
		entry, _, _, _ := s.resolveSTS(x.ID)
		list = append(list, entry)
	}
	send(w, http.StatusOK, list)
}

func (s *studio) runExternalSTS(ctx context.Context, root, id string, payload []byte, target *stsPreview, trace io.Writer) error {
	py := stsPython(root)
	if py == "" {
		return errors.New("STS runtime is missing")
	}
	cmd := exec.Command(py, "-u", "-c", string(stsModelWorker), id, root)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1")
	return s.runSTSProcess(ctx, root+"\x00"+id, id, cmd, payload, target, trace)
}

func (s *studio) dispatchSTSPacket(ctx context.Context, entry stsModel, root, model string, data map[string]any, state *stsPreview) error {
	if entry.ID == defaultSTS {
		return s.stsPacket(ctx, root, model, data, state)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.runExternalSTS(ctx, root, entry.ID, payload, state, io.Discard)
}
