package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

type agentSession struct {
	URL      string `json:"url"`
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
}

var agentLoopback = regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]{0,4}$`)

type agentEngine struct {
	engineCapabilities
	Ready             bool     `json:"ready"`
	AvailableVariants []string `json:"availableVariants"`
}

func (s *studio) agentInfo(w http.ResponseWriter, r *http.Request) {
	s.files.Lock()
	defer s.files.Unlock()
	paths, e := s.paths()
	if e != nil {
		fail(w, 500, "讀取引擎設定失敗")
		return
	}
	out := map[string]agentEngine{}
	for k := range engineCatalog {
		v := s.inspectSelected(k, paths)
		out[k] = agentEngine{engineCapabilities: v.engineCapabilities, Ready: v.Ready, AvailableVariants: v.AvailableVariants}
	}
	ready := []string{}
	if dir, err := cleanAssetsPath(); err == nil {
		root := s.runtimePath("gpt-sovits", paths["gpt-sovits"])
		for _, mode := range cleanModes {
			if _, err := prepareCleanRuntime(mode, root, dir); err == nil {
				ready = append(ready, mode)
			}
		}
	}
	send(w, 200, map[string]any{
		"product": "TimbreDeck", "apiVersion": 1, "pid": os.Getpid(), "instance": s.instance,
		"actions": []string{"status", "capabilities", "asr", "synthesize", "clean", "dataset", "sts",
			"sts-models", "jobs-by-id", "dataset-options", "resident-control", "voice-metadata", "output-metadata"},
		"engines": out, "build": buildInfo(), "cleanModes": cleanModes, "readyCleanModes": ready,
	})
}
func (s *studio) runningAgentSession() string {
	var m agentSession
	if readJSON(s.path("cache", "agent-session.json"), &m) != nil || !hexID(m.Instance, 32) || m.PID <= 0 || !agentLoopback.MatchString(m.URL) {
		return ""
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	c := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequest("GET", m.URL+"/api/agent/info", nil)
	if e != nil {
		return ""
	}
	req.Header.Set("X-TimbreDeck-Agent-Instance", m.Instance)
	resp, e := c.Do(req)
	if e != nil {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Product, Instance string
		Pid, ApiVersion   int
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&v) != nil || v.Product != "TimbreDeck" || v.ApiVersion != 1 || v.Instance != m.Instance || v.Pid != m.PID {
		return ""
	}
	return m.URL
}
