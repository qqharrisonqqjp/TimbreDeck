package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed ui/* worker.py
var assets embed.FS

type studio struct {
	root   string
	mu     sync.Mutex
	active *job
	cancel context.CancelFunc
	// Serializes settings writes, including updates from the pack installer.
	files sync.Mutex
}
type job struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type settings struct {
	Engine        string                     `json:"engine"`
	Text          string                     `json:"text"`
	Reference     string                     `json:"reference"`
	ReferenceName string                     `json:"referenceName"`
	Drafts        map[string]json.RawMessage `json:"drafts"`
}
type installation struct {
	engineCapabilities
	Path    string   `json:"path"`
	Ready   bool     `json:"ready"`
	Missing []string `json:"missing,omitempty"`
}

func id() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func hexID(s string, n int) bool { _, err := hex.DecodeString(s); return len(s) == n && err == nil }
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	send(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "資料格式或長度不正確。")
		return false
	}
	return true
}
func openStudio(root string) (*studio, error) {
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	for _, dir := range []string{"config", "voices", "outputs", "logs", "cache", "runtimes"} {
		if e = os.MkdirAll(filepath.Join(abs, dir), 0700); e != nil {
			return nil, e
		}
	}
	return &studio{root: abs}, nil
}
func (s *studio) path(parts ...string) string { return filepath.Join(s.root, filepath.Join(parts...)) }
func (s *studio) paths() (map[string]string, error) {
	m := map[string]string{}
	e := readJSON(s.path("config", "engines.json"), &m)
	if os.IsNotExist(e) {
		e = nil
	}
	return m, e
}
func required(engine string) []string {
	if engine == "gpt-sovits" {
		return []string{
			"runtime/python.exe",
			"GPT_SoVITS/TTS_infer_pack/TTS.py",
			"GPT_SoVITS/pretrained_models/v2Pro/s2Gv2Pro.pth",
			"GPT_SoVITS/pretrained_models/s1v3.ckpt",
			"GPT_SoVITS/pretrained_models/sv/pretrained_eres2netv2w24s4ep4.ckpt",
			"GPT_SoVITS/pretrained_models/chinese-roberta-wwm-ext-large/config.json",
			"GPT_SoVITS/pretrained_models/chinese-hubert-base/config.json",
		}
	}
	return []string{
		"runtime/python.exe",
		"cosyvoice/cli/cosyvoice.py",
		"third_party/Matcha-TTS/matcha/models/components/decoder.py",
		"pretrained_models/Fun-CosyVoice3-0.5B/cosyvoice3.yaml",
		"pretrained_models/Fun-CosyVoice3-0.5B/llm.pt",
		"pretrained_models/Fun-CosyVoice3-0.5B/CosyVoice-BlankEN/config.json",
		"pretrained_models/Fun-CosyVoice3-0.5B/flow.pt",
		"pretrained_models/Fun-CosyVoice3-0.5B/hift.pt",
		"pretrained_models/Fun-CosyVoice3-0.5B/campplus.onnx",
		"pretrained_models/Fun-CosyVoice3-0.5B/speech_tokenizer_v3.onnx",
	}
}

// Source archives can contain Git LFS pointers in place of actual weights.
func installedFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	if info.Size() > 1024 {
		return true
	}
	data, err := os.ReadFile(path)
	return err == nil && !strings.HasPrefix(string(data), "version https://git-lfs.github.com/spec/v1")
}
func (s *studio) inspect(engine string, paths map[string]string) installation {
	p := paths[engine]
	if !filepath.IsAbs(p) {
		p = s.path("runtimes", engine, p)
	}
	v := installation{Path: p, engineCapabilities: engineCatalog[engine]}
	if !supported(engine) {
		return v
	}
	for _, f := range required(engine) {
		if !installedFile(filepath.Join(p, f)) {
			v.Missing = append(v.Missing, f)
		}
	}
	v.Ready = len(v.Missing) == 0
	return v
}
func (s *studio) config(w http.ResponseWriter, r *http.Request) {
	s.files.Lock()
	defer s.files.Unlock()
	p := s.path("config", "drafts.json")
	if r.Method == "PUT" {
		var d settings
		if !decode(w, r, &d) {
			return
		}
		if !known(d.Engine) || (d.Reference != "" && !hexID(d.Reference, 64)) {
			fail(w, 400, "草稿資料有誤。")
			return
		}
		if e := writeJSON(p, d); e != nil {
			fail(w, 500, e.Error())
			return
		}
		send(w, 200, d)
		return
	}
	var d settings
	if e := readJSON(p, &d); e != nil && !os.IsNotExist(e) {
		fail(w, 500, e.Error())
		return
	}
	paths, e := s.paths()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	inst := map[string]installation{}
	for k := range engineCatalog {
		inst[k] = s.inspect(k, paths)
	}
	send(w, 200, map[string]any{"draft": d, "engines": inst})
}
func (s *studio) setPath(w http.ResponseWriter, r *http.Request) {
	var in struct{ Engine, Path string }
	if !decode(w, r, &in) {
		return
	}
	if !supported(in.Engine) || (in.Path != "" && !filepath.IsAbs(in.Path)) {
		fail(w, 400, "請輸入已解壓引擎的完整資料夾路徑。")
		return
	}
	s.files.Lock()
	defer s.files.Unlock()
	paths, e := s.paths()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	if in.Path == "" {
		delete(paths, in.Engine)
	} else {
		paths[in.Engine] = filepath.Clean(in.Path)
	}
	v := s.inspect(in.Engine, paths)
	if !v.Ready && in.Path != "" {
		fail(w, 422, "未找到必要檔案："+strings.Join(v.Missing, "、"))
		return
	}
	if e := writeJSON(s.path("config", "engines.json"), paths); e != nil {
		fail(w, 500, e.Error())
		return
	}
	send(w, 200, v)
}
func (s *studio) reserve(engine, kind string) (context.Context, job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && (s.active.Status == "running" || s.active.Status == "cancelling") {
		return nil, job{}, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := job{ID: id(), Engine: engine, Kind: kind, Status: "running"}
	s.active = &j
	s.cancel = cancel
	return ctx, j, true
}
func (s *studio) detail(msg string) { s.mu.Lock(); defer s.mu.Unlock(); s.active.Detail = msg }
func (s *studio) finish(e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel()
	s.cancel = nil
	switch {
	case errors.Is(e, context.Canceled):
		s.active.Status = "cancelled"
		s.active.Detail = "已取消"
	case e != nil:
		s.active.Status = "failed"
		s.active.Detail = e.Error()
	default:
		s.active.Status = "done"
		s.active.Detail = ""
	}
}
func (s *studio) routes(host string) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/state", s.config)
	m.HandleFunc("PUT /api/draft", s.config)
	m.HandleFunc("POST /api/engine", s.setPath)
	m.HandleFunc("POST /api/reference", s.reference)
	m.HandleFunc("POST /api/synthesize", s.synthesize)
	m.HandleFunc("GET /api/history", s.history)
	m.HandleFunc("GET /api/audio/{id}", s.audio)
	m.HandleFunc("GET /api/reference/{id}", s.referenceAudio)
	m.HandleFunc("GET /api/job", func(w http.ResponseWriter, r *http.Request) { s.mu.Lock(); defer s.mu.Unlock(); send(w, 200, s.active) })
	m.HandleFunc("POST /api/cancel/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active == nil || s.cancel == nil || s.active.ID != r.PathValue("id") {
			fail(w, 409, "作業已結束。")
			return
		}
		s.active.Status = "cancelling"
		s.cancel()
		send(w, 200, s.active)
	})
	m.HandleFunc("GET /api/pack/{engine}", s.packInfo)
	m.HandleFunc("POST /api/install/{engine}", s.install)
	ui, _ := fs.Sub(assets, "ui")
	m.Handle("GET /", http.FileServer(http.FS(ui)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) {
			http.Error(w, "Local access only", 403)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-TimbreDeck") != "1" {
			fail(w, 403, "請在本機工作室操作。")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		m.ServeHTTP(w, r)
	})
}
func main() {
	exe, e := os.Executable()
	if e != nil {
		log.Fatal(e)
	}
	root := flag.String("data", filepath.Dir(exe), "portable workspace")
	headless := flag.Bool("no-browser", false, "print local URL without opening a browser")
	flag.Parse()
	s, e := openStudio(*root)
	if e != nil {
		log.Fatal(e)
	}
	lock, e := lockWorkspace(s.path("config", "studio.lock"))
	if e != nil {
		log.Fatal("無法鎖定工作資料夾，請確認聲台未在執行，並檢查寫入權限。 ", e)
	}
	defer lock.Close()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		log.Fatal(e)
	}
	url := "http://" + l.Addr().String()
	fmt.Println("TimbreDeck 聲台", url)
	fmt.Println("請保留此視窗。按 Ctrl+C 結束工作室。")
	if !*headless {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
		if e = cmd.Start(); e != nil {
			log.Print(e)
		} else {
			go cmd.Wait()
		}
	}
	srv := &http.Server{Handler: s.routes(l.Addr().String()), ReadHeaderTimeout: 10 * time.Second}
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-sig.Done()
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		srv.Close()
	}()
	if e = srv.Serve(l); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
