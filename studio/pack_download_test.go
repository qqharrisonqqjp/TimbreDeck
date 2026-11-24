package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type releaseFixture map[string][]byte

func (releases releaseFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	data, ok := releases[r.URL.String()]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func TestDownloadSplitPacks(t *testing.T) {
	const engine = "gpt-sovits"
	const host = "127.0.0.1:4321"
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := s.routes(host)
	call := func(method, route, body string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "http://"+host+"/api/"+route, strings.NewReader(body))
		r.Header.Set("X-TimbreDeck", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, route, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	old := http.DefaultClient
	releases := releaseFixture{}
	http.DefaultClient = &http.Client{Transport: releases}
	t.Cleanup(func() { http.DefaultClient = old })
	for _, kind := range []string{"runtime", "model"} {
		p := manifest{Engine: engine, Kind: kind, Version: "test-download"}
		files := runtimeRequirements(engine)
		if kind == "model" {
			p.Variant = "v2Pro"
			files = modelMissing(engine, p.Variant, t.TempDir())
		}
		var data bytes.Buffer
		z := zip.NewWriter(&data)
		for _, name := range files {
			out, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(out, "1"); err != nil {
				t.Fatal(err)
			}
			p.Expanded++
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		prefix := packAsset(engine, kind, p.Variant)
		partName := prefix + "-" + p.Version + ".zip.001"
		digest := sha256.Sum256(data.Bytes())
		p.Hash = hex.EncodeToString(digest[:])
		p.Parts = []part{{Name: partName, Size: int64(data.Len()), Hash: p.Hash}}
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		releases[packURL+"packs-v1/"+prefix+".json"] = body
		releases[packURL+"packs-v1/"+partName] = data.Bytes()
		query := engine + "?kind=" + kind + "&variant=" + p.Variant
		if info := call("GET", "pack/"+query, "", 200); !bytes.Contains(info, []byte(p.Hash)) {
			t.Fatal(string(info))
		}
		call("POST", "install/"+query, `{"hash":"outdated"}`, 409)
		call("POST", "install/"+query, `{"hash":"`+p.Hash+`"}`, 202)
		deadline := time.Now().Add(5 * time.Second)
		for {
			var j job
			if err := json.Unmarshal(call("GET", "job", "", 200), &j); err != nil {
				t.Fatal(err)
			}
			if j.Status == "done" {
				break
			}
			if j.Status == "failed" || time.Now().After(deadline) {
				t.Fatal(j)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if state := call("GET", "state", "", 200); !bytes.Contains(state, []byte(`"ready":true`)) {
		t.Fatal(string(state))
	}
}
