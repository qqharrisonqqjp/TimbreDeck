package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func wavDuration(path string) (float64, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	bad := errors.New("請選用完整的 PCM 或浮點 WAV 音檔。")
	st, e := f.Stat()
	if e != nil {
		return 0, e
	}
	var h [12]byte
	if _, e := io.ReadFull(f, h[:]); e != nil || string(h[:4]) != "RIFF" || string(h[8:]) != "WAVE" {
		return 0, bad
	}
	end := int64(binary.LittleEndian.Uint32(h[4:8])) + 8
	if end > st.Size() {
		return 0, bad
	}
	var rate, block, size uint32
	for pos := int64(12); pos+8 <= end; {
		var c [8]byte
		if _, e = f.ReadAt(c[:], pos); e != nil {
			return 0, bad
		}
		n := binary.LittleEndian.Uint32(c[4:])
		pos += 8
		if int64(n) > end-pos {
			return 0, bad
		}
		switch string(c[:4]) {
		case "fmt ":
			var b [16]byte
			if n < 16 {
				return 0, bad
			}
			if _, e = f.ReadAt(b[:], pos); e != nil {
				return 0, bad
			}
			fmtID := binary.LittleEndian.Uint16(b[:])
			channels := uint32(binary.LittleEndian.Uint16(b[2:]))
			bits := uint32(binary.LittleEndian.Uint16(b[14:]))
			if (fmtID != 1 && fmtID != 3) || channels == 0 || channels > 2 || bits == 0 || bits%8 != 0 || bits > 64 {
				return 0, bad
			}
			rate = binary.LittleEndian.Uint32(b[4:])
			block = uint32(binary.LittleEndian.Uint16(b[12:]))
			if rate < 8000 || rate > 384000 || block != channels*bits/8 {
				return 0, bad
			}
		case "data":
			size = n
		}
		pos += int64(n) + int64(n%2)
	}
	if rate == 0 || block == 0 || size < block || size%block != 0 {
		return 0, bad
	}
	return float64(size) / float64(block) / float64(rate), nil
}

func (s *studio) reference(w http.ResponseWriter, r *http.Request) {
	f, e := os.CreateTemp(s.path("voices"), ".import-*")
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	_, e = io.Copy(io.MultiWriter(f, h), http.MaxBytesReader(w, r.Body, 32<<20))
	closeErr := f.Close()
	if e != nil {
		fail(w, 400, "未能讀取錄音，單檔上限為 32 MiB。")
		return
	}
	if closeErr != nil {
		fail(w, 500, closeErr.Error())
		return
	}
	secs, e := wavDuration(f.Name())
	if e != nil {
		fail(w, 422, e.Error())
		return
	}
	key := hex.EncodeToString(h.Sum(nil))
	path := s.path("voices", key+".wav")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if e = os.Rename(f.Name(), path); e != nil {
			fail(w, 500, e.Error())
			return
		}
	}
	send(w, 201, map[string]any{"id": key, "seconds": secs})
}
func (s *studio) referenceAudio(w http.ResponseWriter, r *http.Request) {
	k := r.PathValue("id")
	if !hexID(k, 64) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeFile(w, r, s.path("voices", k+".wav"))
}

type request struct {
	Engine      string  `json:"engine"`
	Mode        string  `json:"mode"`
	Text        string  `json:"text"`
	Transcript  string  `json:"transcript"`
	Instruction string  `json:"instruction"`
	Reference   string  `json:"reference"`
	Language    string  `json:"language"`
	RefLang     string  `json:"refLang"`
	Speed       float64 `json:"speed"`
	Preview     bool    `json:"preview"`
}
type recording struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Seconds float64   `json:"seconds"`
	Input   request   `json:"input"`
}

func firstSentence(s string) string {
	for i, c := range s {
		if strings.ContainsRune("。！？!?\n", c) || (c == '.' && (i+1 == len(s) || s[i+1] == ' ')) {
			return strings.TrimSpace(s[:i+len(string(c))])
		}
	}
	return s
}
func language(s string) bool {
	return s == "yue" || s == "zh" || s == "en" || s == "ja" || s == "ko" || s == "auto"
}
func (s *studio) validate(q *request) error {
	q.Text = strings.TrimSpace(q.Text)
	q.Transcript = strings.TrimSpace(q.Transcript)
	q.Instruction = strings.TrimSpace(q.Instruction)
	if !supported(q.Engine) || !hexID(q.Reference, 64) || q.Text == "" || len([]rune(q.Text)) > 10000 {
		return errors.New("請選擇已接通的引擎，匯入錄音，並輸入最多 10,000 字的台詞。")
	}
	if q.Speed < 0.5 || q.Speed > 2 || math.IsNaN(q.Speed) {
		return errors.New("語速範圍為 0.5 至 2 倍。")
	}
	secs, e := wavDuration(s.path("voices", q.Reference+".wav"))
	if e != nil {
		return errors.New("找不到有效的參考錄音，請重新匯入。")
	}
	switch q.Engine {
	case "gpt-sovits":
		if q.Mode != "clone" || !language(q.Language) || !language(q.RefLang) {
			return errors.New("語言或工作模式無效。")
		}
		if secs < 3 || secs > 10 {
			return errors.New("GPT-SoVITS 需要 3 至 10 秒的參考錄音。")
		}
	case "cosyvoice3":
		if q.Mode != "clone" && q.Mode != "instruct" && q.Mode != "cross" {
			return errors.New("未知的 CosyVoice 模式。")
		}
		if q.Mode == "instruct" && q.Instruction == "" {
			return errors.New("請填寫講法指示。")
		}
	}
	if q.Mode == "clone" && q.Transcript == "" {
		return errors.New("請填寫參考錄音的逐字稿。")
	}
	if q.Preview {
		q.Text = firstSentence(q.Text)
	}
	return nil
}
func (s *studio) synthesize(w http.ResponseWriter, r *http.Request) {
	var q request
	if !decode(w, r, &q) {
		return
	}
	if e := s.validate(&q); e != nil {
		fail(w, 422, e.Error())
		return
	}
	s.files.Lock()
	paths, e := s.paths()
	v := s.inspect(q.Engine, paths)
	s.files.Unlock()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	if !v.Ready {
		fail(w, 409, "請先在模型管理連接完整引擎。")
		return
	}
	ctx, j, ok := s.reserve(q.Engine, "synthesis")
	if !ok {
		fail(w, 409, "已有作業進行中。")
		return
	}
	go func() { s.finish(s.render(ctx, j.ID, v.Path, q)) }()
	send(w, 202, j)
}
func (s *studio) render(ctx context.Context, key, root string, q request) error {
	dir, e := os.MkdirTemp(s.path("outputs"), ".render-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "speech.wav")
	ref := s.path("voices", q.Reference+".wav")
	src, _ := assets.ReadFile("worker.py")
	payload, e := json.Marshal(map[string]any{"request": q, "ref": ref, "output": dst})
	if e != nil {
		return e
	}
	logFile, e := os.Create(s.path("logs", key+".log"))
	if e != nil {
		return e
	}
	defer logFile.Close()
	py := filepath.Join(root, "runtime", "python.exe")
	cmd := exec.CommandContext(ctx, py, "-c", string(src))
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	env := cmd.Environ()
	cmd.Env = append(env, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PYTHONNOUSERSITE=1", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HOME="+s.path("cache", "huggingface"), "TORCH_HOME="+s.path("cache", "torch"), "PATH="+filepath.Dir(py)+string(os.PathListSeparator)+root+string(os.PathListSeparator)+os.Getenv("PATH"))
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("引擎未能完成合成。請查看 logs/%s.log（%v）。", key, e)
	}
	secs, e := wavDuration(dst)
	if e != nil {
		return errors.New("引擎沒有輸出有效 WAV。")
	}
	if e = writeJSON(filepath.Join(dir, "record.json"), recording{key, time.Now().UTC(), secs, q}); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return os.Rename(dir, s.path("outputs", key))
}
func (s *studio) history(w http.ResponseWriter, r *http.Request) {
	dirs, e := os.ReadDir(s.path("outputs"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	items := []recording{}
	for _, d := range dirs {
		if !d.IsDir() || !hexID(d.Name(), 32) {
			continue
		}
		var v recording
		if readJSON(s.path("outputs", d.Name(), "record.json"), &v) == nil && v.ID == d.Name() {
			items = append(items, v)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created.After(items[j].Created) })
	send(w, 200, items)
}
func (s *studio) audio(w http.ResponseWriter, r *http.Request) {
	k := r.PathValue("id")
	if !hexID(k, 32) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=TimbreDeck-"+k+".wav")
	}
	http.ServeFile(w, r, s.path("outputs", k, "speech.wav"))
}
