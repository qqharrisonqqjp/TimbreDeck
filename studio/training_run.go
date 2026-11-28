package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	trainingtools "github.com/qqharrisonqqjp/TimbreDeck/tools"
)

type trainingOptions struct {
	Name            string `json:"name"`
	Epochs          int    `json:"epochs"`
	Batch           int    `json:"batch"`
	ReleaseResident string `json:"releaseResident,omitempty"`
}

func (o *trainingOptions) validate() error {
	o.Name = strings.TrimSpace(o.Name)
	if o.Epochs == 0 {
		o.Epochs = 8
	}
	if o.Batch == 0 {
		o.Batch = 2
	}
	if o.Epochs < 1 || o.Epochs > 100 || o.Batch < 1 || o.Batch > 8 || !validTrainingName(o.Name, 80) {
		return errors.New("請填寫模型名稱；訓練輪數 1–100，批次大小 1–8。")
	}
	return nil
}

type trainingRuntime struct{ root, weights string }

func (s *studio) getTrainingRuntime() (trainingRuntime, error) {
	var runtime trainingRuntime
	paths, err := s.paths()
	if err != nil {
		return runtime, err
	}
	installation := s.inspectSelected("gpt-sovits", paths)
	runtime = trainingRuntime{installation.variantPaths["v2Pro"], installation.modelPaths["v2Pro"]}
	if runtime.root == "" || runtime.weights == "" ||
		len(missingFiles(runtime.root, []string{"runtime/python.exe", "GPT_SoVITS/s1_train.py", "GPT_SoVITS/s2_train.py",
			"GPT_SoVITS/prepare_datasets/1-get-text.py", "GPT_SoVITS/prepare_datasets/2-get-hubert-wav32k.py",
			"GPT_SoVITS/prepare_datasets/2-get-sv.py", "GPT_SoVITS/prepare_datasets/3-get-semantic.py"})) != 0 ||
		len(missingFiles(runtime.weights, []string{"GPT_SoVITS/pretrained_models/s1v3.ckpt",
			"GPT_SoVITS/pretrained_models/v2Pro/s2Gv2Pro.pth", "GPT_SoVITS/pretrained_models/v2Pro/s2Dv2Pro.pth"})) != 0 {
		return runtime, errors.New("請先連接包含訓練工具的 GPT-SoVITS v2Pro 引擎，並安裝 v2Pro 基礎訓練權重。")
	}
	return runtime, nil
}

// Called with the job lock held. Do not implicitly evict a user-pinned model.
func (s *studio) releaseForTraining(w http.ResponseWriter, confirmed string) bool {
	if s.busy() {
		fail(w, 409, "已有作業進行中，請等完成後再試。")
		return false
	}
	return s.prepareAccelerator(w, confirmed, nil)
}

type trainingRun struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Dataset bool      `json:"dataset"`
	ModelID string    `json:"modelId,omitempty"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Epochs  int       `json:"epochs"`
	Batch   int       `json:"batch"`
}

func (s *studio) startTraining(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Options  trainingOptions `json:"options"`
		Revision int             `json:"revision"`
	}
	if !decode(w, r, &q) {
		return
	}
	project, err := s.loadTraining(r.PathValue("id"))
	if err != nil || q.Revision != project.Revision {
		fail(w, 409, "請先儲存或重新載入訓練專案。")
		return
	}
	if q.Options.Name == "" {
		q.Options.Name = project.Name
	}
	if err := q.Options.validate(); err != nil {
		fail(w, 422, err.Error())
		return
	}
	if err := trainable(project, q.Options.Batch); err != nil {
		fail(w, 422, err.Error())
		return
	}
	runtime, err := s.getTrainingRuntime()
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	if err := s.trainingSpace(); err != nil {
		fail(w, 507, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.releaseForTraining(w, q.Options.ReleaseResident) {
		return
	}
	ctx, j, _ := s.reserveLocked("gpt-sovits", "training")
	go func() { s.finish(s.trainProject(ctx, j.ID, project, q.Options, runtime)) }()
	send(w, 202, j)
}

func trainable(project trainingProject, batch int) error {
	count := 0
	for _, clip := range project.Clips {
		if clip.Use != "train" {
			continue
		}
		if clip.Transcript == "" {
			return errors.New("有訓練片段欠缺台詞，請先校對。")
		}
		count++
	}
	if count < batch {
		return fmt.Errorf("需要至少 %d 個標記為「用於訓練」的片段；也可降低批次大小。", batch)
	}
	return nil
}

func (s *studio) trainingSpace() error {
	dir := s.path("training-runs")
	if err := s.ensureManagedDirectory(dir); err != nil {
		return err
	}
	if free, err := freeSpace(dir); err != nil || free < 6<<30 {
		return errors.New("GPU 訓練、特徵與檢查點至少需要 6 GiB 可用磁碟空間；長資料集及多輪訓練可能需要更多。")
	}
	return nil
}

func (s *studio) trainProject(ctx context.Context, key string, project trainingProject, options trainingOptions, runtime trainingRuntime) (err error) {
	dir := s.path("training-runs", key)
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	record := trainingRun{ID: key, Project: project.ID, Name: options.Name, Created: time.Now().UTC(),
		Status: "running", Epochs: options.Epochs, Batch: options.Batch}
	marker := filepath.Join(dir, "record.json")
	defer func() {
		record.Status = "done"
		if err != nil {
			record.Error = err.Error()
			record.Status = "failed"
			if errors.Is(err, context.Canceled) {
				record.Status = "cancelled"
				record.Error = "已取消"
			}
		}
		err = errors.Join(err, writeJSON(marker, record))
	}()
	stage, err := os.MkdirTemp(s.path("cache"), ".training-run-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	s.detail("正在封存校對後的訓練資料集…")
	if err = s.exportRunDataset(ctx, stage, dir, project); err != nil {
		return err
	}
	record.Dataset = true
	if err = writeJSON(marker, record); err != nil {
		return err
	}
	for _, file := range []string{"prepare_gpt_dataset.py", "train_gpt_model.py"} {
		bytes, e := trainingtools.Scripts.ReadFile(file)
		if e != nil {
			return e
		}
		if err = os.WriteFile(filepath.Join(stage, file), bytes, 0600); err != nil {
			return err
		}
	}
	log, err := os.Create(s.path("logs", key+".log"))
	if err != nil {
		return err
	}
	defer log.Close()
	features := filepath.Join(stage, "features")
	run := filepath.Join(stage, "run")
	s.detail("正在擷取語音特徵（文字、HuBERT、語者、語義）…")
	if err = s.runTrainer(ctx, runtime, stage, "prepare_gpt_dataset.py", log,
		runtime.root, filepath.Join(stage, "dataset"), features, "--gpu", "0", "--models", runtime.weights); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("擷取訓練特徵失敗，請查看日誌 %s.log（%w）。", key, err)
	}
	s.detail(fmt.Sprintf("正在 GPU 微調 GPT 與 SoVITS（%d 輪，批次 %d）…", options.Epochs, options.Batch))
	if err = s.runTrainer(ctx, runtime, stage, "train_gpt_model.py", log,
		runtime.root, features, run, "--gpu", "0", "--models", runtime.weights,
		"--epochs", strconv.Itoa(options.Epochs), "--batch", strconv.Itoa(options.Batch)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("GPU 訓練失敗；已保留資料集，請查看日誌 %s.log（%w）。", key, err)
	}
	var model trainedModel
	if err = readJSON(filepath.Join(run, "model.json"), &model); err != nil {
		return err
	}
	if err = checkTrainedModel(model); err != nil {
		return err
	}
	model.Name = options.Name
	s.detail("正在封裝與登記微調模型…")
	record.ModelID, err = s.registerTrainedModel(ctx, model,
		filepath.Join(run, model.GPT), filepath.Join(run, model.SoVITS))
	return err
}

func (s *studio) exportRunDataset(ctx context.Context, stage, dir string, project trainingProject) error {
	path := filepath.Join(stage, "dataset.zip")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err = errors.Join(s.writeTrainingDataset(ctx, f, project), f.Close()); err != nil {
		return err
	}
	dataset := filepath.Join(stage, "dataset")
	if err := os.MkdirAll(filepath.Join(dataset, "wavs"), 0700); err != nil {
		return err
	}
	if err := extractTrainingInputs(ctx, path, dataset); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, "dataset.zip"))
}

func extractTrainingInputs(ctx context.Context, path, dataset string) error {
	a, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer a.Close()
	files, err := zipMembers(&a.Reader, 312, maxTrainingDataset)
	if err != nil {
		return err
	}
	for name, entry := range files {
		if name != "dataset.list" {
			if !strings.HasPrefix(name, "wavs/") {
				continue
			}
			if !strings.HasSuffix(name, ".wav") || !hexID(strings.TrimSuffix(strings.TrimPrefix(name, "wavs/"), ".wav"), 32) {
				return errors.New("訓練 WAV 名稱無效。")
			}
		}
		if err := extractZipFile(ctx, entry, filepath.Join(dataset, filepath.FromSlash(name)), nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *studio) runTrainer(ctx context.Context, runtime trainingRuntime, stage, script string, log *os.File, args ...string) error {
	// The embedded Windows Python ignores PYTHONPATH; explicitly add the
	// staging directory so train_gpt_model can import prepare_gpt_dataset.
	const launcher = "import os,runpy,sys; script=sys.argv.pop(1); sys.path.insert(0,os.path.dirname(script)); sys.argv[0]=script; runpy.run_path(script,run_name='__main__')"
	args = append([]string{"-u", "-c", launcher, filepath.Join(stage, script)}, args...)
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.root, "runtime", "python.exe"), args...)
	cmd.Dir = stage
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = synthesisEnv(cmd, s.path("cache"), runtime.root)
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error {
		// The trainer spawns preparation/Lightning processes; stop only this job's process tree.
		e := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
		if e != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd.Run()
}

func (s *studio) trainingRuns(w http.ResponseWriter, _ *http.Request) {
	records := []trainingRun{}
	children, _ := os.ReadDir(s.path("training-runs"))
	for _, entry := range children {
		if !entry.IsDir() || !hexID(entry.Name(), 32) {
			continue
		}
		var record trainingRun
		path := s.path("training-runs", entry.Name(), "record.json")
		if !s.managedPath(path) || readJSON(path, &record) != nil || record.ID != entry.Name() {
			continue
		}
		if record.ModelID != "" {
			if _, err := s.loadTrainedModel(record.ModelID); err != nil {
				record.ModelID = ""
			}
		}
		s.mu.Lock()
		if record.Status == "running" && (!s.busy() || s.active.ID != record.ID) {
			record.Status = "interrupted"
		}
		s.mu.Unlock()
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Created.After(records[j].Created) })
	send(w, 200, records)
}

func (s *studio) trainingRunDataset(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !hexID(key, 32) {
		http.NotFound(w, r)
		return
	}
	s.serveTrainingZIP(w, r, s.path("training-runs", key, "dataset.zip"), "TimbreDeck-dataset-"+key+".zip")
}
