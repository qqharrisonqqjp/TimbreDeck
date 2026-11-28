// Windows argv-based bridge. Both GUI and AI resolve the same local layout.
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
	URL      string
	Instance string
	PID      int
}

type layout struct {
	Root        string
	Stage       string
	Data        string
	Studio      string
	Tools       string
	ASRAssets   string
	CleanAssets string
	STSAssets   string
}

var loop = regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]{0,4}$`)

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func existsDirectory(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }

func resolve(root, override string) (layout, error) {
	out := layout{Root: root}
	switch {
	case regular(filepath.Join(root, "source", "local-active.txt")):
		out.Stage = filepath.Join(root, "source")
		out.Tools = filepath.Join(out.Stage, "tools")
	case regular(filepath.Join(root, "Start.exe")):
		out.Stage = root
		out.Tools = filepath.Join(root, "tools")
	default:
		out.Stage = filepath.Join(root, "asr-update")
		out.Tools = filepath.Join(out.Stage, "src", "tools")
	}
	out.Studio = filepath.Join(out.Stage, "Start.exe")
	alt := filepath.Join(out.Stage, "Start-STS.exe")
	if regular(filepath.Join(out.Stage, "sts-active.txt")) && regular(alt) {
		out.Studio = alt
	}
	out.Data = override
	if out.Data == "" {
		if out.Stage == root {
			out.Data = filepath.Join(root, "StudioData")
		} else {
			out.Data = filepath.Join(root, "acceptance-80ebf58", "more", "release", "StudioData")
		}
	}
	if !filepath.IsAbs(out.Data) {
		return out, errors.New("TD_STUDIO_DATA requires an absolute path")
	}
	choose := func(name string, paths ...string) string {
		if override := os.Getenv(name); override != "" && filepath.IsAbs(override) {
			return override
		}
		for _, p := range paths {
			if existsDirectory(p) {
				return p
			}
		}
		return paths[len(paths)-1]
	}
	assets := filepath.Join(out.Stage, "assets")
	out.ASRAssets = choose("TIMBREDECK_ASR_ASSETS", filepath.Join(root, "asr-update", "assets"), filepath.Join(root, "assets", "asr"), assets)
	out.CleanAssets = choose("TIMBREDECK_CLEAN_ASSETS", filepath.Join(root, "noise-update", "assets"), filepath.Join(root, "assets", "clean"), assets)
	out.STSAssets = choose("TIMBREDECK_STS_ASSETS", filepath.Join(root, "sts-models"), filepath.Join(root, "assets", "sts"))
	return out, nil
}
func sessionURL(data string) string {
	b, e := os.ReadFile(filepath.Join(data, "cache", "agent-session.json"))
	if e != nil || len(b) > 4096 {
		return ""
	}
	var m session
	if json.Unmarshal(b, &m) != nil || !loop.MatchString(m.URL) || len(m.Instance) != 32 || m.PID <= 0 {
		return ""
	}
	if _, e = hex.DecodeString(m.Instance); e != nil {
		return ""
	}
	c := http.Client{Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil}, Timeout: 2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := http.NewRequest("GET", m.URL+"/api/agent/info", nil)
	r.Header.Set("X-TimbreDeck-Agent-Instance", m.Instance)
	resp, e := c.Do(r)
	if e != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Product, Instance string
		ApiVersion, PID   int
	}
	if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&v) == nil &&
		v.Product == "TimbreDeck" && v.ApiVersion == 1 && v.Instance == m.Instance && v.PID == m.PID {
		return m.URL
	}
	return ""
}
func ensure(l layout, idle bool) (string, error) {
	if url := sessionURL(l.Data); url != "" {
		return url, nil
	}
	args := []string{"-no-browser", "-data", l.Data}
	if idle {
		args = append(args, "-agent-idle", "10m")
	}
	c := exec.Command(l.Studio, args...)
	c.Dir = l.Stage
	c.Env = append(os.Environ(), "PYTHONPATH=", "PYTHONHOME=", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUTF8=1", "OMP_NUM_THREADS=4",
		"TIMBREDECK_ASR_ASSETS="+l.ASRAssets, "TIMBREDECK_CLEAN_ASSETS="+l.CleanAssets, "TIMBREDECK_STS_ASSETS="+l.STSAssets,
		"MPLCONFIGDIR="+filepath.Join(l.Data, "cache", "mpl"), "NUMBA_CACHE_DIR="+filepath.Join(l.Data, "cache", "numba"), "MODELSCOPE_CACHE="+filepath.Join(l.Data, "cache", "modelscope"))
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if e := c.Start(); e != nil {
		return "", e
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	for i := 0; i < 36; i++ {
		if url := sessionURL(l.Data); url != "" {
			return url, nil
		}
		select {
		case <-done:
			if url := sessionURL(l.Data); url != "" {
				return url, nil
			}
			return "", errors.New("工作室未能啟動；若另一版本正在使用同一資料夾，請先正常結束該版本。")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return "", errors.New("工作室仍未就緒，請稍後查詢 status。")
}
func run() int {
	app, e := os.Executable()
	if e != nil {
		return 1
	}
	l, e := resolve(filepath.Dir(app), os.Getenv("TD_STUDIO_DATA"))
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 2
	}
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "launcher-info":
			if len(args) != 1 {
				return 2
			}
			if json.NewEncoder(os.Stdout).Encode(l) != nil {
				return 1
			}
			return 0
		case "ensure-running", "open-studio":
			visible := args[0] == "open-studio"
			printOnly := visible && len(args) == 2 && args[1] == "--print-url"
			if len(args) != 1 && !printOnly {
				return 2
			}
			url, e := ensure(l, true)
			if e == nil && visible && !printOnly {
				e = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Start()
			}
			if e != nil {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": e.Error()})
				return 1
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "ready": true, "url": url})
			return 0
		}
	}
	file := "td_agent.py"
	inbox, exports := filepath.Join(l.Root, "AI-Inbox"), filepath.Join(l.Root, "AI-Exports")
	if len(args) > 0 && args[0] == "mcp" {
		file = "td_mcp.py"
		for _, dir := range []string{inbox, exports} {
			if e := os.MkdirAll(dir, 0700); e != nil {
				fmt.Fprintln(os.Stderr, e)
				return 1
			}
		}
		args = append([]string{"--input-root", inbox, "--output-dir", exports}, args[1:]...)
	}
	py := filepath.Join(os.Getenv("APPDATA"), "uv", "python", "cpython-3.11-windows-x86_64-none", "python.exe")
	if !regular(py) {
		py = "python.exe"
	}
	c := exec.Command(py, append([]string{"-B", "-X", "utf8", filepath.Join(l.Tools, file)}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = append(os.Environ(), "TD_STUDIO_DATA="+l.Data, "TD_AI_OUTPUT="+exports, "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
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
