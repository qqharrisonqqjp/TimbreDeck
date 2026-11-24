package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func toneFixture() []byte {
	data := make([]byte, 8000*3*2+44)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	return data
}
func TestPortableSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "聲台 demo")
	s, e := openStudio(root)
	if e != nil {
		t.Fatal(e)
	}
	h := s.routes("127.0.0.1:4321")
	call := func(method, path string, body []byte, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:4321"+path, bytes.NewReader(body))
		r.Header.Set("X-TimbreDeck", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	wav := toneFixture()
	a := call("POST", "/api/reference", wav, 201)
	var info struct {
		ID      string
		Seconds float64
	}
	json.Unmarshal(a.Body.Bytes(), &info)
	if info.Seconds != 3 || !bytes.Equal(call("GET", "/api/reference/"+info.ID, nil, 200).Body.Bytes(), wav) {
		t.Fatal("reference round trip")
	}
	call("POST", "/api/reference", wav[:len(wav)-1], 422)
	call("PUT", "/api/draft", []byte(`{"engine":"cosyvoice3","text":"今晚。","drafts":{"cosyvoice3":{"mode":"instruct"}}}`), 200)
	s, e = openStudio(root)
	if e != nil {
		t.Fatal(e)
	}
	h = s.routes("127.0.0.1:4321")
	if !strings.Contains(call("GET", "/api/state", nil, 200).Body.String(), "今晚。") {
		t.Fatal("missing saved draft")
	}
	q := request{Engine: "gpt-sovits", Mode: "clone", Language: "yue", RefLang: "yue", Text: "今晚。第二句。", Reference: info.ID, Speed: 1, Transcript: "參考。", Preview: true}
	if err := s.validate(&q); err != nil || q.Text != "今晚。" {
		t.Fatal(q, err)
	}
	b, _ := json.Marshal(q)
	call("POST", "/api/synthesize", b, 409)
	q.Engine = "cosyvoice3"
	q.Mode = "instruct"
	if s.validate(&q) == nil {
		t.Fatal("missing instruction accepted")
	}
	for _, engine := range []string{"gpt-sovits", "cosyvoice3"} {
		if s.inspect(engine, nil).Ready {
			t.Fatal("source-only engine accepted")
		}
	}
	ctx, j, ok := s.reserve("cosyvoice3", "synthesis")
	if !ok {
		t.Fatal("reserve")
	}
	if _, _, ok := s.reserve("gpt-sovits", "synthesis"); ok {
		t.Fatal("double reserve")
	}
	call("POST", "/api/cancel/"+j.ID, nil, 200)
	s.finish(ctx.Err())
	if s.active.Status != "cancelled" {
		t.Fatal(s.active)
	}
	r := httptest.NewRequest("POST", "http://other.invalid/api/reference", bytes.NewReader(wav))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestResumablePart(t *testing.T) {
	content := []byte("portable engine file")
	digest := sha256.Sum256(content)
	v := part{"part", int64(len(content)), hex.EncodeToString(digest[:])}
	path := filepath.Join(t.TempDir(), "part")
	if e := os.WriteFile(path, content[:5], 0600); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=5-" {
			t.Error("resume header")
		}
		http.ServeContent(w, r, "file", time.Time{}, bytes.NewReader(content))
	}))
	defer server.Close()
	if e := downloadPart(context.Background(), server.URL, path, v); e != nil {
		t.Fatal(e)
	}
	if !verify(context.Background(), path, v.Hash) {
		t.Fatal("digest mismatch")
	}
	if e := downloadPart(context.Background(), "", path, v); e != nil {
		t.Fatal("should reuse verified part", e)
	}
	os.Remove(path)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(bytes.Repeat([]byte("x"), len(content))) }))
	defer bad.Close()
	if downloadPart(context.Background(), bad.URL, path, v) == nil {
		t.Fatal("accepted corrupt part")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("corrupt part retained")
	}
}
func TestSplitZip(t *testing.T) {
	makeZip := func(name string) []byte {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		f, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		io.WriteString(f, "weights")
		if e = w.Close(); e != nil {
			t.Fatal(e)
		}
		return buf.Bytes()
	}
	data := makeZip("models/voice.bin")
	parts := segments{}
	temp := t.TempDir()
	cut := int64(23)
	for i, chunk := range [][]byte{data[:cut], data[cut:]} {
		f, e := os.CreateTemp(temp, "part")
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		if _, e = f.Write(chunk); e != nil {
			t.Fatal(e)
		}
		start := int64(i) * cut
		parts = append(parts, segment{f, start, start + int64(len(chunk))})
	}
	if e := expand(context.Background(), parts, int64(len(data)), temp, 7); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(temp, "models", "voice.bin"))
	if string(b) != "weights" {
		t.Fatal("corrupt extraction")
	}
	unsafe := makeZip("../outside")
	if expand(context.Background(), bytes.NewReader(unsafe), int64(len(unsafe)), temp, 7) == nil {
		t.Fatal("bad archive accepted")
	}
}
func TestWorkspaceLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	a, e := lockWorkspace(path)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := lockWorkspace(path); e == nil {
		b.Close()
		t.Fatal("double lock")
	}
	a.Close()
	b, e := lockWorkspace(path)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
	if n, e := freeSpace(dir); e != nil || n <= 0 {
		t.Fatal(n, e)
	}
}

func fixturePack(t *testing.T, engine, version, contents string) (manifest, map[string][]byte) {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := required(engine)
	for _, name := range files {
		f, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	data := archive.Bytes()
	chunks := [][]byte{data[:25], data[25:]}
	byName := map[string][]byte{}
	p := manifest{Engine: engine, Version: version, Expanded: int64(len(files) * len(contents)), Hash: digest(data)}
	for i, b := range chunks {
		name := fmt.Sprintf("%s-%s.zip.%03d", engine, p.Version, i+1)
		byName[name] = b
		p.Parts = append(p.Parts, part{name, int64(len(b)), digest(b)})
	}
	return p, byName
}

func TestInstallAndPreservePreviousRuntime(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine := "gpt-sovits"
	p, byName := fixturePack(t, engine, "1-test", "x")
	expected := "pack-" + p.Hash[:16]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(byName[strings.TrimPrefix(r.URL.Path, "/")])
	}))
	defer server.Close()
	for _, corrupt := range []bool{false, true} {
		ctx, _, ok := s.reserve(engine, "install")
		if !ok {
			t.Fatal("reserve")
		}
		if corrupt {
			p.Hash = strings.Repeat("0", 64)
		}
		if err = p.check(engine); err != nil {
			t.Fatal(err)
		}
		err = s.installPack(ctx, p, server.URL+"/")
		s.finish(err)
		if (err != nil) != corrupt {
			t.Fatal("unexpected result", corrupt, err)
		}
		paths, err := s.paths()
		if err != nil {
			t.Fatal(err)
		}
		if paths[engine] != expected || !s.inspect(engine, paths).Ready {
			t.Fatal("previous working installation lost", paths)
		}
	}
}

func TestImportAndSwitchLocalPacks(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const host = "127.0.0.1:4321"
	const engine = "gpt-sovits"
	h := s.routes(host)
	call := func(method, path string, data any, status int) []byte {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://"+host+"/api/"+path, bytes.NewReader(body))
		r.Header.Set("X-TimbreDeck", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	importVersion := func(p manifest, parts map[string][]byte, expected string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "聲台 packs")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(dir, engine+".json")
		if err := writeJSON(manifestPath, p); err != nil {
			t.Fatal(err)
		}
		for name, data := range parts {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var submitted job
		if err := json.Unmarshal(call("POST", "import-pack/"+engine, map[string]string{"path": manifestPath}, 202), &submitted); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			var current job
			if err := json.Unmarshal(call("GET", "job", nil, 200), &current); err != nil {
				t.Fatal(err)
			}
			if current.ID != submitted.ID {
				t.Fatal("unexpected job", current)
			}
			if current.Status == "running" {
				continue
			}
			if current.Status != expected {
				t.Fatal("unexpected status", current)
			}
			return
		}
		t.Fatal("installation did not finish")
	}
	first, firstParts := fixturePack(t, engine, "1-test", "1")
	second, secondParts := fixturePack(t, engine, "2-test", "2")
	importVersion(first, firstParts, "done")
	importVersion(second, secondParts, "done")
	corrupted := second
	corrupted.Parts = append([]part(nil), second.Parts...)
	corrupted.Parts[0].Hash = strings.Repeat("0", 64)
	importVersion(corrupted, secondParts, "failed")
	paths, err := s.paths()
	if err != nil || paths[engine] != "pack-"+second.Hash[:16] {
		t.Fatal("failed import changed active runtime", paths, err)
	}
	list := string(call("GET", "installed-packs/"+engine, nil, 200))
	if !strings.Contains(list, `"version":"1-test","active":false`) || !strings.Contains(list, `"version":"2-test","active":true`) {
		t.Fatal("missing version or active selection", list)
	}
	id := "pack-" + first.Hash[:16]
	call("POST", "installed-packs/"+engine, map[string]string{"id": id}, 200)
	paths, err = s.paths()
	if err != nil || paths[engine] != id {
		t.Fatal("previous runtime was not selected", paths, err)
	}
}

func TestLFSPlaceholder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.pt")
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 123456\n"
	if err := os.WriteFile(path, []byte(pointer), 0600); err != nil {
		t.Fatal(err)
	}
	if installedFile(path) {
		t.Fatal("Git LFS pointer accepted as installed weights")
	}
}
