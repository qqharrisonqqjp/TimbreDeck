package main

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

//go:embed clean_worker.py
var cleanWorkerSource []byte

const (
	cleanOutputDir  = "clean-outputs"
	cleanClipLimit  = 24
	cleanGapLimit   = 3000
	cleanTimeLimit  = 180
	cleanSampleRate = 32000
	cleanClipBytes  = 32 << 20
	cleanTotalBytes = 128 << 20
	cleanDiskBytes  = 256 << 20
	cleanWorkPrefix = ".clean-job-"
)

type cleanRequest struct {
	References []string `json:"references"`
	Mode       string   `json:"mode"`
	GapMS      int      `json:"gapMs"`
}

type cleanTask struct {
	Device     string   `json:"-"`
	Mode       string   `json:"mode"`
	Inputs     []string `json:"inputs"`
	Output     string   `json:"output"`
	GapMS      int      `json:"gapMs"`
	EngineRoot string   `json:"engineRoot"`
	Assets     string   `json:"assets"`
}

type cleanRecording struct {
	ID         string    `json:"id"`
	Mode       string    `json:"mode"`
	Count      int       `json:"count"`
	GapMS      int       `json:"gapMs"`
	Seconds    float64   `json:"seconds"`
	SampleRate uint32    `json:"sampleRate"`
	Created    time.Time `json:"created"`
}

type cleanRunner func(context.Context, cleanTask, string, string) error

func supportedCleanMode(mode string) bool {
	return slices.Contains(cleanModes, mode)
}

var cleanModes = []string{"denoise", "uvr", "both", "mossformer", "lavasr", "lavasr-denoise"}

func cleanOpen(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("不是一般檔案")
	}
	return os.Open(path)
}

// The generic WAV parser accepts auxiliary chunks; bound the number of scans in
// both the synchronous request and worker-result validation.
func cleanWAV(path string, maxBytes int64) (waveData, int64, error) {
	f, err := cleanOpen(path)
	if err != nil {
		return waveData{}, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return waveData{}, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 44 || info.Size() > maxBytes {
		return waveData{}, 0, errors.New("WAV 檔案長度無效")
	}
	var h [8]byte
	if _, err := f.ReadAt(h[:], 0); err != nil {
		return waveData{}, 0, err
	}
	end := int64(binary.LittleEndian.Uint32(h[4:])) + 8
	if end != info.Size() {
		return waveData{}, 0, errors.New("WAV 檔案長度不符")
	}
	for pos, chunks := int64(12), 0; pos+8 <= end; chunks++ {
		if chunks == 256 {
			return waveData{}, 0, errors.New("WAV 標頭區塊過多")
		}
		if _, err := f.ReadAt(h[:], pos); err != nil {
			return waveData{}, 0, err
		}
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		pos += 8 + size + size%2
	}
	wav, err := parseWAV(f)
	if err == nil && ((wav.format == 1 && wav.bits > 32) ||
		(wav.format == 3 && wav.bits != 32 && wav.bits != 64)) {
		err = errors.New("不支援的 WAV 位元深度")
	}
	return wav, info.Size(), err
}

func (s *studio) cleanInputs(q cleanRequest) ([]string, uint64, error) {
	if !supportedCleanMode(q.Mode) {
		return nil, 0, errors.New("請選擇有效的去噪模式。")
	}
	if len(q.References) < 1 || len(q.References) > cleanClipLimit {
		return nil, 0, fmt.Errorf("請選擇 1 至 %d 個 WAV 音檔。", cleanClipLimit)
	}
	if q.GapMS < 0 || q.GapMS > cleanGapLimit {
		return nil, 0, fmt.Errorf("音檔間的靜音須在 0 至 %d 毫秒之間。", cleanGapLimit)
	}
	inputs := make([]string, 0, len(q.References))
	frames := uint64((len(q.References)-1)*q.GapMS) * cleanSampleRate / 1000
	var bytes int64
	for i, ref := range q.References {
		if !hexID(ref, 64) || ref != strings.ToLower(ref) {
			return nil, 0, fmt.Errorf("第 %d 個錄音編號無效。", i+1)
		}
		path := s.path("voices", ref+".wav")
		if !s.managedPath(path) {
			return nil, 0, fmt.Errorf("找不到第 %d 個錄音。", i+1)
		}
		wav, fileBytes, err := cleanWAV(path, cleanClipBytes)
		if err != nil {
			return nil, 0, fmt.Errorf("第 %d 個錄音不是有效的 WAV：%v。", i+1, err)
		}
		if wav.rate > 192000 {
			return nil, 0, fmt.Errorf("第 %d 個錄音的取樣率上限為 192 kHz。", i+1)
		}
		// Match the worker's nearest-frame rounding for each resampled clip.
		rate := uint64(wav.rate)
		length := (2*uint64(wav.size/wav.block)*cleanSampleRate + rate) / (2 * rate)
		if length == 0 {
			return nil, 0, fmt.Errorf("第 %d 個錄音太短。", i+1)
		}
		frames += length
		bytes += fileBytes
		if frames > cleanTimeLimit*cleanSampleRate || bytes > cleanTotalBytes {
			return nil, 0, fmt.Errorf("所選錄音（包含靜音）的總長度上限為 %d 秒，輸入總大小上限為 %d MiB。", cleanTimeLimit, cleanTotalBytes>>20)
		}
		inputs = append(inputs, path)
	}
	return inputs, frames, nil
}

func cleanAssetsPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("TIMBREDECK_CLEAN_ASSETS")); path != "" {
		return filepath.Abs(path)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(filepath.Dir(executable), "assets"))
}

func enhancedClean(mode string) bool {
	return mode == "mossformer" || mode == "lavasr" || mode == "lavasr-denoise"
}
func prepareCleanRuntime(mode, root, assets string) (string, error) {
	if enhancedClean(mode) {
		root = filepath.Join(assets, "enhance")
	}
	if !installedFile(filepath.Join(root, "runtime", "python.exe")) {
		return "", errors.New("此清理模式未安裝本機執行環境，請先安裝相應引擎。")
	}
	return root, checkCleanAssets(mode, assets)
}
func checkCleanAssets(mode, dir string) error {
	if enhancedClean(mode) {
		name := "lavasr"
		files := []string{"lavasr/enhancer_v2/config.yaml", "lavasr/enhancer_v2/pytorch_model.bin", "lavasr/denoiser/denoiser.bin"}
		if mode == "mossformer" {
			name = "clearvoice"
			files = []string{"checkpoints/MossFormer2_SE_48K/last_best_checkpoint", "checkpoints/MossFormer2_SE_48K/last_best_checkpoint.pt"}
		}
		root := filepath.Join(dir, "enhance")
		if len(missingFiles(root, append(files, name+".ready"))) != 0 {
			return errors.New("此清理模式所需的本機模型未安裝，請先完成安裝。")
		}
		return nil
	}
	var files []string
	if mode != "uvr" {
		const folder = "speech_frcrn_ans_cirm_16k/"
		files = append(files, folder+"configuration.json", folder+"pytorch_model.bin")
	}
	if mode != "denoise" {
		files = append(files, "HP2_all_vocals.pth")
	}
	if missing := missingFiles(dir, files); len(missing) > 0 {
		return fmt.Errorf("未找到去噪模型檔案：%s。", strings.Join(missing, "、"))
	}
	return nil
}

func (s *studio) clean(w http.ResponseWriter, r *http.Request) {
	s.startClean(w, r, runCleanWorker)
}

func (s *studio) startClean(w http.ResponseWriter, r *http.Request, run cleanRunner) {
	var q cleanRequest
	if !decode(w, r, &q) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "已有作業進行中。")
		return
	}

	inputs, frames, err := s.cleanInputs(q)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	const engine = "gpt-sovits"
	s.files.Lock()
	paths, err := s.paths()
	s.files.Unlock()
	if err != nil {
		fail(w, 500, "未能讀取引擎設定。")
		return
	}
	selection := paths[engine]
	if selection != "" && !filepath.IsAbs(selection) && !installedPackName.MatchString(selection) {
		fail(w, 409, "GPT-SoVITS 執行環境設定無效。")
		return
	}
	root := s.runtimePath(engine, selection)
	modelDir, err := cleanAssetsPath()
	if err != nil {
		fail(w, 500, "未能找到去噪模型資料夾。")
		return
	}
	root, err = prepareCleanRuntime(q.Mode, root, modelDir)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	dir := s.path(cleanOutputDir)
	if err := s.ensureManagedDirectory(dir); err != nil {
		fail(w, 500, "未能建立去噪輸出資料夾。")
		return
	}
	if free, err := freeSpace(dir); err != nil || free < cleanDiskBytes {
		fail(w, 507, "去噪工作區需要至少 256 MiB 可用空間。")
		return
	}

	ctx, j, _ := s.reserveLocked(engine, "clean")
	p := cleanTask{Mode: q.Mode, Inputs: inputs, GapMS: q.GapMS, EngineRoot: root, Assets: modelDir, Device: s.chooseCleaningDevice()}
	go s.cleanJob(ctx, j.ID, p, frames, run)
	send(w, 202, j)
}

func (s *studio) cleanJob(ctx context.Context, key string, p cleanTask, frames uint64, run cleanRunner) {
	// Prevent a wedged runtime from monopolizing the shared work slot.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	work, err := os.MkdirTemp(s.path(cleanOutputDir), cleanWorkPrefix)
	if err != nil {
		s.finish(err)
		return
	}
	published := false
	defer func() {
		_ = os.RemoveAll(work)
		if !published {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				err = errors.New("去噪作業超時。")
			}
			s.finish(err)
		}
	}()
	dir := filepath.Join(work, "result")
	if err = os.Mkdir(dir, 0700); err != nil {
		return
	}
	p.Output = filepath.Join(dir, "clean.wav")
	if err = run(ctx, p, work, s.path("logs", key+".log")); err != nil {
		return
	}
	if !s.managedPath(p.Output) {
		err = errors.New("引擎沒有輸出有效 WAV。")
		return
	}
	wav, _, wavErr := cleanWAV(p.Output, cleanTotalBytes)
	if wavErr != nil || wav.rate != cleanSampleRate || wav.channels != 1 ||
		wav.format != 1 || wav.bits != 16 || uint64(wav.size/wav.block) != frames {
		err = errors.New("引擎沒有輸出有效長度的 WAV。")
		return
	}
	record := cleanRecording{ID: key, Mode: p.Mode, Count: len(p.Inputs), GapMS: p.GapMS, Seconds: float64(frames) / cleanSampleRate, SampleRate: wav.rate, Created: time.Now().UTC()}
	if err = writeJSON(filepath.Join(dir, "record.json"), record); err != nil {
		return
	}

	// The shared cancellation endpoint takes the same mutex. A cancellation is
	// either accepted before publication or sees that the job already ended.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return
	}
	if err = os.Rename(dir, s.path(cleanOutputDir, key)); err == nil {
		_ = os.RemoveAll(work)
		published = true
		s.finishLocked(nil)
	}
}

// Keep diagnostic output for debugging without letting a noisy dependency fill
// the drive. Both subprocess streams share this writer and one exec copy goroutine.
type cleanLogWriter struct {
	out  io.Writer
	left int
}

func (w *cleanLogWriter) Write(buf []byte) (int, error) {
	n := len(buf)
	if w.left == 0 {
		return n, nil
	}
	p := buf[:min(n, w.left)]
	wrote, err := w.out.Write(p)
	w.left -= wrote
	if err != nil {
		return wrote, err
	}
	return n, nil
}

func runCleanWorker(ctx context.Context, p cleanTask, work, logPath string) error {
	src := filepath.Join(work, "clean_worker.py")
	if err := os.WriteFile(src, cleanWorkerSource, 0600); err != nil {
		return err
	}
	temp := filepath.Join(work, "temp")
	if err := os.Mkdir(temp, 0700); err != nil {
		return err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	py := filepath.Join(p.EngineRoot, "runtime", "python.exe")
	cmd := exec.CommandContext(ctx, py, src)
	cmd.Dir = p.EngineRoot
	cmd.Stdin = strings.NewReader(string(payload))
	log := &cleanLogWriter{out: logFile, left: 1 << 20}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(cmd.Environ(),
		"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1",
		"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1",
		"TMPDIR="+temp, "TEMP="+temp, "TMP="+temp, "NUMBA_CACHE_DIR="+filepath.Join(temp, "numba"),
		"PATH="+filepath.Dir(py)+string(os.PathListSeparator)+p.EngineRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	if p.Device != "" {
		cmd.Env = append(cmd.Env, "TIMBREDECK_CLEAN_DEVICE="+p.Device)
	}
	if enhancedClean(p.Mode) && p.Device == "cpu" {
		cmd.Env = append(cmd.Env, "CUDA_VISIBLE_DEVICES=-1")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("去噪引擎未能完成處理。請查看 logs/%s（%v）。", filepath.Base(logPath), err)
	}
	return nil
}

func (s *studio) readCleanRecording(key string) (cleanRecording, bool) {
	var v cleanRecording
	meta := s.path(cleanOutputDir, key, "record.json")
	if !hexID(key, 32) || !s.managedPath(meta) {
		return v, false
	}
	f, err := cleanOpen(meta)
	if err != nil {
		return v, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return v, false
	}
	d := json.NewDecoder(io.LimitReader(f, 4096))
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v.ID != key ||
		!supportedCleanMode(v.Mode) || v.Count < 1 || v.Count > cleanClipLimit ||
		v.Seconds <= 0 || v.Seconds > cleanTimeLimit || v.GapMS < 0 || v.GapMS > cleanGapLimit ||
		v.SampleRate != cleanSampleRate || v.Created.IsZero() {
		return v, false
	}
	return v, true
}

func (s *studio) cleanHistory(w http.ResponseWriter, _ *http.Request) {
	items := []cleanRecording{}
	dir := s.path(cleanOutputDir)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		send(w, 200, items)
		return
	}
	if !s.managedPath(dir) {
		fail(w, 500, "未能讀取去噪歷史。")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail(w, 500, "未能讀取去噪歷史。")
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if v, ok := s.readCleanRecording(entry.Name()); ok {
			path := s.path(cleanOutputDir, v.ID, "clean.wav")
			info, err := os.Lstat(path)
			if err == nil && info.Mode().IsRegular() && info.Size() >= 44 &&
				info.Size() <= cleanTotalBytes && s.managedPath(path) {
				items = append(items, v)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created.After(items[j].Created) })
	send(w, 200, items)
}

func (s *studio) cleanAudio(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if _, ok := s.readCleanRecording(key); !ok {
		http.NotFound(w, r)
		return
	}
	path := s.path(cleanOutputDir, key, "clean.wav")
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
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 44 || info.Size() > cleanTotalBytes {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=TimbreDeck-Clean-"+key+".wav")
	}
	http.ServeContent(w, r, "clean.wav", info.ModTime(), f)
}

// The process holds the workspace lock when this runs. Discard abandoned
// staging directories from a forced exit, never published recordings.
func (s *studio) pruneCleanStaging() error {
	dir := s.path(cleanOutputDir)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	if !s.managedPath(dir) {
		return errors.New("去噪輸出資料夾位置無效")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var removed error
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), cleanWorkPrefix)
		if !entry.IsDir() || !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if s.managedPath(path) {
			removed = errors.Join(removed, os.RemoveAll(path))
		}
	}
	return removed
}
