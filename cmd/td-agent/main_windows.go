// Windows argv-based bridge. Data and text are never evaluated as shell code.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

type session struct {
	URL      string `json:"url"`
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
}

var loop = regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]{0,4}$`)

func sessionOK(data string) bool {
	b, e := os.ReadFile(filepath.Join(data, "cache", "agent-session.json"))
	if e != nil || len(b) > 4096 {
		return false
	}
	var m session
	if json.Unmarshal(b, &m) != nil || !loop.MatchString(m.URL) || len(m.Instance) != 32 || m.PID <= 0 {
		return false
	}
	if _, e = hex.DecodeString(m.Instance); e != nil {
		return false
	}
	c := http.Client{Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil}, Timeout: 2 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest("GET", m.URL+"/api/agent/info", nil)
	r.Header.Set("X-TimbreDeck-Agent-Instance", m.Instance)
	resp, e := c.Do(r)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		Product, Instance string
		ApiVersion, PID   int
	}
	return resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&v) == nil && v.Product == "TimbreDeck" && v.ApiVersion == 1 && v.Instance == m.Instance && v.PID == m.PID
}
func ensure(root, data, stage string) error {
	if sessionOK(data) {
		return nil
	}
	c := exec.Command(filepath.Join(stage, "Start.exe"), "-no-browser", "-agent-idle", "10m", "-data", data)
	c.Dir = stage
	c.Env = append(os.Environ(), "PYTHONPATH=", "PYTHONHOME=", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUTF8=1", "OMP_NUM_THREADS=4", "TIMBREDECK_ASR_ASSETS="+filepath.Join(root, "asr-update", "assets"), "TIMBREDECK_CLEAN_ASSETS="+filepath.Join(root, "noise-update", "assets"), "MPLCONFIGDIR="+filepath.Join(data, "cache", "mpl"), "NUMBA_CACHE_DIR="+filepath.Join(data, "cache", "numba"), "MODELSCOPE_CACHE="+filepath.Join(data, "cache", "modelscope"))
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if e := c.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	for i := 0; i < 36; i++ {
		if sessionOK(data) {
			return nil
		}
		select {
		case <-done:
			if sessionOK(data) {
				return nil
			}
			return errors.New("工作室未能啟動；若舊版正在使用同一資料夾，請先結束舊版再試。")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("工作室仍未就緒，請稍後查詢 status。")
}
func run() int {
	app, e := os.Executable()
	if e != nil {
		return 1
	}
	root := filepath.Dir(app)
	stage, tools := filepath.Join(root, "asr-update"), filepath.Join(root, "asr-update", "src", "tools")
	if _, e := os.Stat(filepath.Join(root, "source", "local-active.txt")); e == nil {
		stage, tools = filepath.Join(root, "source"), filepath.Join(root, "source", "tools")
	}
	args := os.Args[1:]
	data := os.Getenv("TD_STUDIO_DATA")
	if data == "" {
		data = filepath.Join(root, "acceptance-80ebf58", "more", "release", "StudioData")
	}
	if !filepath.IsAbs(data) {
		fmt.Fprintln(os.Stderr, "TD_STUDIO_DATA requires an absolute path")
		return 2
	}
	if len(args) > 0 && args[0] == "ensure-running" {
		if len(args) > 1 {
			return 2
		}
		if e = ensure(root, data, stage); e != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": e.Error()})
			return 1
		}
		fmt.Println(`{"ok":true,"ready":true}`)
		return 0
	}
	file := "td_agent.py"
	if len(args) > 0 && args[0] == "mcp" {
		file = "td_mcp.py"
		args = append([]string{"--input-root", filepath.Join(root, "AI-Inbox"), "--output-dir", filepath.Join(root, "AI-Exports")}, args[1:]...)
	}
	py := filepath.Join(os.Getenv("APPDATA"), "uv", "python", "cpython-3.11-windows-x86_64-none", "python.exe")
	if _, e = os.Stat(py); e != nil {
		py = "python.exe"
	}
	c := exec.Command(py, append([]string{"-B", "-X", "utf8", filepath.Join(tools, file)}, args...)...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "TD_STUDIO_DATA="+data, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	if e = c.Run(); e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			return x.ExitCode()
		}
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	return 0
}
func main() { os.Exit(run()) }
