package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	finetuneMaxBytes = 1536 << 20
	gptWeight        = "weights/gpt.ckpt"
	sovitsWeight     = "weights/sovits.pth"
)

type finetuneArchive struct {
	trainedModel
	Format string            `json:"format"`
	SHA256 map[string]string `json:"sha256"`
}

func checkTrainedModel(model trainedModel) error {
	if model.Engine != "gpt-sovits" || model.Model != "v2Pro" || model.SourceRevision != gptTrainingSourceRevision ||
		!filepath.IsLocal(model.GPT) || !filepath.IsLocal(model.SoVITS) ||
		filepath.Ext(model.GPT) != ".ckpt" || filepath.Ext(model.SoVITS) != ".pth" {
		return errors.New("微調模型與此工作室的 GPT-SoVITS v2Pro 不相容。")
	}
	return nil
}

func (s *studio) registerTrainedModel(ctx context.Context, model trainedModel, gpt, sovits string) (string, error) {
	if err := checkTrainedModel(model); err != nil {
		return "", err
	}
	root := s.path("finetunes")
	if err := s.ensureManagedDirectory(root); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".model-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := os.Mkdir(filepath.Join(stage, "weights"), 0700); err != nil {
		return "", err
	}
	model.GPT, model.SoVITS = gptWeight, sovitsWeight
	var total int64
	for _, pair := range [][2]string{{gpt, model.GPT}, {sovits, model.SoVITS}} {
		src, err := cleanOpen(pair[0])
		if err != nil {
			return "", err
		}
		info, err := src.Stat()
		if err != nil || info.Size() < 4 || info.Size() >= finetuneMaxBytes-total {
			src.Close()
			return "", errors.New("配對權重檔案受損、缺失或過大。")
		}
		total += info.Size()
		dst, err := os.OpenFile(filepath.Join(stage, pair[1]), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			src.Close()
			return "", err
		}
		_, err = io.CopyN(dst, checkingReader{ctx, src}, info.Size())
		if err = errors.Join(err, src.Close(), dst.Close()); err != nil {
			return "", err
		}
	}
	if err := writeJSON(filepath.Join(stage, "model.json"), model); err != nil {
		return "", err
	}
	f, err := os.Create(filepath.Join(stage, "model.zip"))
	if err != nil {
		return "", err
	}
	if err = errors.Join(writeFinetuneArchive(ctx, f, stage, model), f.Close()); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := id()
	s.files.Lock()
	defer s.files.Unlock()
	if err := os.Rename(stage, s.path("finetunes", key)); err != nil {
		return "", err
	}
	return key, nil
}

func writeFinetuneArchive(ctx context.Context, dst io.Writer, folder string, model trainedModel) (err error) {
	archive := zip.NewWriter(dst)
	defer func() { err = errors.Join(err, archive.Close()) }()
	meta := finetuneArchive{trainedModel: model, Format: "timbredeck-finetune-v1", SHA256: map[string]string{}}
	meta.GPT, meta.SoVITS = gptWeight, sovitsWeight
	for _, pair := range [][2]string{{model.GPT, gptWeight}, {model.SoVITS, sovitsWeight}} {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: pair[1], Method: zip.Store})
		if err != nil {
			return err
		}
		input, err := cleanOpen(filepath.Join(folder, pair[0]))
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(io.MultiWriter(entry, hash), checkingReader{ctx, input})
		if err = errors.Join(err, input.Close()); err != nil {
			return err
		}
		meta.SHA256[pair[1]] = hex.EncodeToString(hash.Sum(nil))
	}
	file, err := archive.Create("model.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(file).Encode(meta)
}

func (s *studio) importTrainedModel(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name != "" && !validTrainingName(name, 80) {
		fail(w, 422, "模型名稱最多 80 字。")
		return
	}
	path, cleanup, err := s.stageTrainingUpload(w, r, finetuneMaxBytes+2<<20)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	key, model, err := s.restoreTrainedModel(ctx, path, name)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	send(w, 201, map[string]string{"id": key, "name": model.Name})
}

func (s *studio) restoreTrainedModel(ctx context.Context, path, name string) (string, trainedModel, error) {
	var meta finetuneArchive
	a, err := zip.OpenReader(path)
	if err != nil {
		return "", meta.trainedModel, errors.New("請選擇由 TimbreDeck 匯出的 GPT-SoVITS 微調模型 ZIP。")
	}
	defer a.Close()
	files, err := zipMembers(&a.Reader, 3, finetuneMaxBytes)
	if err != nil {
		return "", meta.trainedModel, err
	}
	if err = readZipJSON(files["model.json"], 64<<10, &meta); err != nil {
		return "", meta.trainedModel, err
	}
	if meta.Format != "timbredeck-finetune-v1" || meta.GPT != gptWeight || meta.SoVITS != sovitsWeight ||
		checkTrainedModel(meta.trainedModel) != nil || len(meta.SHA256) != 2 {
		return "", meta.trainedModel, errors.New("模型 ZIP 不是相容的 GPT-SoVITS v2Pro 配對權重。")
	}
	if name == "" {
		name = strings.TrimSpace(meta.Name)
	}
	if !validTrainingName(name, 80) {
		return "", meta.trainedModel, errors.New("請為模型填寫有效名稱。")
	}
	meta.Name = name
	stage, err := os.MkdirTemp(s.path("cache"), ".finetune-verify-*")
	if err != nil {
		return "", meta.trainedModel, err
	}
	defer os.RemoveAll(stage)
	gpt, sovits := filepath.Join(stage, "gpt.ckpt"), filepath.Join(stage, "sovits.pth")
	for _, pair := range [][2]string{{gptWeight, gpt}, {sovitsWeight, sovits}} {
		entry := files[pair[0]]
		want := meta.SHA256[pair[0]]
		if entry == nil || entry.UncompressedSize64 < 4 || !hexID(want, 64) {
			return "", meta.trainedModel, errors.New("模型 ZIP 缺少權重或 SHA-256。")
		}
		hash := sha256.New()
		if err := extractZipFile(ctx, entry, pair[1], hash); err != nil {
			return "", meta.trainedModel, err
		}
		if !strings.EqualFold(want, hex.EncodeToString(hash.Sum(nil))) {
			return "", meta.trainedModel, errors.New("模型 ZIP 的權重校驗未通過。")
		}
	}
	outGPT, outSoVITS := filepath.Join(stage, "verified.ckpt"), filepath.Join(stage, "verified.pth")
	if err := s.sanitizeWeights(ctx, stage, gpt, sovits, outGPT, outSoVITS); err != nil {
		return "", meta.trainedModel, err
	}
	key, err := s.registerTrainedModel(ctx, meta.trainedModel, outGPT, outSoVITS)
	return key, meta.trainedModel, err
}

func (s *studio) sanitizeWeights(ctx context.Context, stage string, paths ...string) error {
	src, err := assets.ReadFile("checkpoint_verify.py")
	if err != nil {
		return err
	}
	settings, err := s.paths()
	if err != nil {
		return err
	}
	for _, engine := range []string{"gpt-sovits", "cosyvoice3"} {
		root := s.inspectSelected(engine, settings).Path
		python := filepath.Join(root, "runtime", "python.exe")
		if root == "" || !installedFile(python) {
			continue
		}
		cmd := exec.CommandContext(ctx, python, append([]string{"-c", string(src)}, paths...)...)
		cmd.Dir = stage
		cmd.Env = append(synthesisEnv(cmd, s.path("cache"), root), "CUDA_VISIBLE_DEVICES=-1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 78 {
			continue
		}
		why := strings.TrimSpace(string(out))
		if len([]rune(why)) > 400 {
			why = string([]rune(why)[:400])
		}
		return fmt.Errorf("微調權重安全驗證失敗：%s", why)
	}
	return errors.New("匯入需要已連接且有 PyTorch 2.6 或更新版本的 GPT-SoVITS 或 CosyVoice 執行環境。")
}

func (s *studio) renameTrainedModel(w http.ResponseWriter, r *http.Request) {
	var q struct{ Name string }
	if !decode(w, r, &q) {
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if !validTrainingName(q.Name, 80) {
		fail(w, 422, "請填寫模型名稱（最多 80 字）。")
		return
	}
	key := r.PathValue("id")
	s.files.Lock()
	defer s.files.Unlock()
	m, err := s.loadTrainedModel(key)
	if err != nil {
		fail(w, 404, "找不到微調模型。")
		return
	}
	folder := s.path("finetunes", key)
	if !installedFile(filepath.Join(folder, "model.json")) {
		folder = s.path("training", key, "run")
	}
	m.GPT, err = filepath.Rel(folder, m.GPT)
	if err == nil {
		m.SoVITS, err = filepath.Rel(folder, m.SoVITS)
	}
	if err == nil {
		m.Name = q.Name
		err = writeJSON(filepath.Join(folder, "model.json"), m)
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, 200, map[string]string{"id": key, "name": q.Name})
}

func (s *studio) serveTrainingZIP(w http.ResponseWriter, r *http.Request, path, name string) {
	if !s.managedPath(path) {
		http.NotFound(w, r)
		return
	}
	f, err := cleanOpen(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeContent(w, r, name, time.Time{}, f)
}

func (s *studio) downloadTrainedModel(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	model, err := s.loadTrainedModel(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := s.path("finetunes", key, "model.zip")
	name := "TimbreDeck-v2Pro-" + key + ".zip"
	if installedFile(path) {
		s.serveTrainingZIP(w, r, path, name)
		return
	}
	// Legacy on-disk training projects can also be exported without changing them.
	folder := s.path("training", key, "run")
	model.GPT, _ = filepath.Rel(folder, model.GPT)
	model.SoVITS, _ = filepath.Rel(folder, model.SoVITS)
	if model.Name == "" {
		p, _ := s.loadTraining(key)
		model.Name = p.Name
	}
	f, err := os.CreateTemp(s.path("cache"), ".legacy-model-*.zip")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := writeFinetuneArchive(r.Context(), f, folder, model); err != nil {
		fail(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeContent(w, r, name, time.Time{}, f)
}
