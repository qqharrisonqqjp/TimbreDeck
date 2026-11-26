package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed asr_worker.py
var asrSource []byte

const (
	asrBatchLimit   = 256
	asrCacheLimit   = 256
	asrFileLimit    = 32 << 20
	asrBatchBytes   = 128 << 20
	asrClipSeconds  = 180
	asrBatchSeconds = 600
)

var (
	asrBusy        = errors.New("本機語音辨識已有多項作業，請稍後再試。")
	asrUnavailable = errors.New("本機語音辨識引擎或模型尚未安裝。")
	asrActive      = make(chan struct{}, 1)
	asrQueue       = make(chan struct{}, 4)
	asrCache       = struct {
		sync.Mutex
		results map[[32]byte]asrResult
		order   [][32]byte
	}{results: make(map[[32]byte]asrResult)}
)

type asrResult struct {
	Text              string `json:"text"`
	Provider          string `json:"provider"`
	Language          string `json:"language"`
	RequestedLanguage string `json:"requestedLanguage"`
	Warning           string `json:"warning,omitempty"`
	Cached            bool   `json:"cached"`
}

type asrRequest struct {
	Reference string `json:"reference"`
	Language  string `json:"language"`
}

type asrWorkerRequest struct {
	Paths    []string `json:"paths"`
	Language string   `json:"language"`
	Assets   string   `json:"assets"`
	Output   string   `json:"output"`
}

func validASRLanguage(code string) bool {
	switch code {
	case "auto", "yue", "zh", "en", "ja", "ko", "fr", "de", "es", "it", "pt", "nl", "ru", "ar", "hi", "th", "vi", "id":
		return true
	}
	return false
}

func (s *studio) asrHandler(w http.ResponseWriter, r *http.Request) {
	var request asrRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Language == "" {
		request.Language = "auto"
	}
	if !validASRLanguage(request.Language) {
		fail(w, http.StatusBadRequest, "不支援此辨識語言。")
		return
	}
	key := request.Reference
	var path string
	switch {
	case hexID(key, 64) && key == strings.ToLower(key):
		path = s.path("voices", key+".wav")
	case hexID(key, 32) && key == strings.ToLower(key):
		if _, ok := s.readCleanRecording(key); ok {
			path = s.path(cleanOutputDir, key, "clean.wav")
		}
	}
	if path == "" || !s.managedPath(path) {
		fail(w, http.StatusNotFound, "找不到要辨識的錄音。")
		return
	}
	if _, _, err := cleanWAV(path, asrFileLimit); err != nil {
		fail(w, http.StatusUnprocessableEntity, "錄音並非支援的 WAV 音檔。")
		return
	}
	results, err := s.recognizeFiles(r.Context(), []string{path}, request.Language)
	if err != nil {
		status := http.StatusUnprocessableEntity
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(err, asrBusy):
			status = http.StatusTooManyRequests
		case errors.Is(err, asrUnavailable):
			status = http.StatusServiceUnavailable
		}
		fail(w, status, err.Error())
		return
	}
	send(w, http.StatusOK, results[0])
}

func (s *studio) asrRuntime() (string, string, error) {
	root := strings.TrimSpace(os.Getenv("TIMBREDECK_ASR_ENGINE_ROOT"))
	if root == "" {
		const engine = "gpt-sovits"
		s.files.Lock()
		paths, err := s.paths()
		s.files.Unlock()
		if err != nil {
			return "", "", asrUnavailable
		}
		selection := paths[engine]
		if selection != "" && !filepath.IsAbs(selection) && !installedPackName.MatchString(selection) {
			return "", "", asrUnavailable
		}
		root = s.runtimePath(engine, selection)
	}
	if !filepath.IsAbs(root) || !installedFile(filepath.Join(root, "runtime", "python.exe")) {
		return "", "", asrUnavailable
	}
	models := strings.TrimSpace(os.Getenv("TIMBREDECK_ASR_ASSETS"))
	if models == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", "", asrUnavailable
		}
		models = filepath.Join(filepath.Dir(executable), "assets")
	}
	if !filepath.IsAbs(models) || !installedFile(filepath.Join(models, "faster-whisper-small", "model.bin")) {
		return "", "", asrUnavailable
	}
	return filepath.Clean(root), filepath.Clean(models), nil
}

// A bounded content cache keys on input bytes, the exact worker revision, both
// model revisions, the requested language, and the owning workspace.
func (s *studio) asrCacheVersion(engine, models, language string) []byte {
	var version strings.Builder
	fmt.Fprintf(&version, "%s\x00%s\x00%s\x00%s\x00%x", s.root, engine, models, language, sha256.Sum256(asrSource))
	for _, rel := range []string{"faster-whisper-small/model.bin", "faster-whisper-small/tokenizer.json", "faster-whisper-small/config.json", "faster-whisper-small/vocabulary.txt", "sensevoice-onnx/model.int8.onnx", "sensevoice-onnx/tokens.txt"} {
		if stat, err := os.Stat(filepath.Join(models, filepath.FromSlash(rel))); err == nil {
			fmt.Fprintf(&version, "\x00%s:%d:%d", rel, stat.Size(), stat.ModTime().UnixNano())
		}
	}
	return []byte(version.String())
}

type asrContextReader struct {
	context.Context
	io.Reader
}

func (r asrContextReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (s *studio) recognizeFiles(ctx context.Context, paths []string, language string) ([]asrResult, error) {
	if !validASRLanguage(language) || len(paths) == 0 || len(paths) > asrBatchLimit {
		return nil, errors.New("辨識音檔數量或語言不正確。")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case asrQueue <- struct{}{}:
		defer func() { <-asrQueue }()
	default:
		return nil, asrBusy
	}
	select {
	case asrActive <- struct{}{}:
		defer func() { <-asrActive }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	engine, models, err := s.asrRuntime()
	if err != nil {
		return nil, err
	}
	if !s.managedPath(s.path("cache")) {
		return nil, errors.New("語音辨識暫存資料夾無效。")
	}
	work, err := os.MkdirTemp(s.path("cache"), ".asr-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	results := make([]asrResult, len(paths))
	positions := make(map[[32]byte][]int)
	var keys [][32]byte
	var clips []string
	var totalSeconds float64
	var totalBytes int64
	version := s.asrCacheVersion(engine, models, language)
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil || !filepath.IsAbs(path) || !s.managedPath(path) {
			return nil, fmt.Errorf("第 %d 個辨識音檔不在工作區。", i+1)
		}
		category, _, _ := strings.Cut(rel, string(filepath.Separator))
		switch category {
		case "voices", "training", cleanOutputDir, "cache":
		default:
			return nil, fmt.Errorf("第 %d 個辨識音檔並非已儲存錄音。", i+1)
		}
		if filepath.Ext(path) != ".wav" {
			return nil, fmt.Errorf("第 %d 個辨識音檔並非 WAV。", i+1)
		}
		wav, bytes, err := cleanWAV(path, asrFileLimit)
		if err != nil {
			return nil, fmt.Errorf("第 %d 個辨識音檔無效。", i+1)
		}
		seconds := float64(wav.size/wav.block) / float64(wav.rate)
		totalSeconds += seconds
		totalBytes += bytes
		if seconds > asrClipSeconds || totalSeconds > asrBatchSeconds || totalBytes > asrBatchBytes {
			return nil, errors.New("語音辨識批次超過音檔大小或時間上限。")
		}
		staged := filepath.Join(work, fmt.Sprintf("%03d.wav", i))
		key, err := stageASR(ctx, path, staged, bytes, version)
		if err != nil {
			return nil, err
		}
		asrCache.Lock()
		cached, hit := asrCache.results[key]
		asrCache.Unlock()
		if hit {
			cached.Cached = true
			results[i] = cached
			_ = os.Remove(staged)
			continue
		}
		if _, pending := positions[key]; !pending {
			keys = append(keys, key)
			clips = append(clips, staged)
		} else {
			_ = os.Remove(staged)
		}
		positions[key] = append(positions[key], i)
	}
	if len(clips) == 0 {
		return results, nil
	}
	got, err := runASR(ctx, work, engine, models, language, clips)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	asrCache.Lock()
	defer asrCache.Unlock()
	for i, entry := range got {
		key := keys[i]
		for _, dest := range positions[key] {
			results[dest] = entry
		}
		// A transient primary-engine failure must not lock in a lower-quality fallback.
		if entry.Warning != "" {
			continue
		}
		for len(asrCache.order) >= asrCacheLimit {
			delete(asrCache.results, asrCache.order[0])
			asrCache.order = asrCache.order[1:]
		}
		asrCache.results[key] = entry
		asrCache.order = append(asrCache.order, key)
	}
	return results, nil
}

func stageASR(ctx context.Context, path, staged string, expected int64, version []byte) ([32]byte, error) {
	var key [32]byte
	src, err := cleanOpen(path)
	if err != nil {
		return key, err
	}
	defer src.Close()
	dst, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return key, err
	}
	hash := sha256.New()
	_, _ = hash.Write(version)
	_, _ = hash.Write([]byte{0})
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(asrContextReader{ctx, src}, expected+1))
	closeErr := dst.Close()
	if err != nil || closeErr != nil {
		return key, errors.Join(err, closeErr)
	}
	if n != expected {
		return key, errors.New("讀取期間辨識音檔已變更。")
	}
	copy(key[:], hash.Sum(nil))
	return key, nil
}

func runASR(ctx context.Context, dir, engine, modelDir, language string, clips []string) ([]asrResult, error) {
	python := filepath.Join(engine, "runtime", "python.exe")
	script, output := filepath.Join(dir, "asr_worker.py"), filepath.Join(dir, "result.json")
	if err := os.WriteFile(script, asrSource, 0600); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(asrWorkerRequest{clips, language, modelDir, output})
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "asr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, "-B", script)
	cmd.Dir = engine
	cmd.Stdin = strings.NewReader(string(payload))
	writer := &cleanLogWriter{out: log, left: 64 << 10}
	cmd.Stderr, cmd.Stdout = writer, writer
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(cmd.Environ(),
		"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1",
		"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HOME="+filepath.Join(dir, "hf"),
		"TMPDIR="+dir, "TMP="+dir, "TEMP="+dir,
		"PATH="+filepath.Dir(python)+string(os.PathListSeparator)+engine+string(os.PathListSeparator)+os.Getenv("PATH"))
	runErr := cmd.Run()
	_ = log.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if runErr != nil {
		return nil, errors.New("辨識沒有完成：本機語音模型未能處理音檔。")
	}
	f, err := cleanOpen(output)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	var got []asrResult
	if d.Decode(&got) != nil || d.Decode(new(any)) != io.EOF || len(got) != len(clips) {
		return nil, errors.New("辨識結果無效。")
	}
	for i := range got {
		if (got[i].Provider != "sensevoice" && got[i].Provider != "faster-whisper") || got[i].RequestedLanguage != language ||
			len(got[i].Text) > 40000 || len(got[i].Warning) > 512 || !utf8.ValidString(got[i].Text) || got[i].Language == "" {
			return nil, errors.New("辨識結果不完整。")
		}
		got[i].Text = strings.ReplaceAll(strings.Join(strings.Fields(got[i].Text), " "), "|", "｜")
		got[i].Cached = false
	}
	return got, nil
}
