package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const packURL = "https://github.com/qqharrisonqqjp/TimbreDeck/releases/download/"

var assetName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}$`)

type part struct {
	Name string `json:"name"`
	Size int64  `json:"bytes"`
	Hash string `json:"sha256"`
}
type manifest struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	Expanded int64  `json:"expanded"`
	Hash     string `json:"sha256"`
	Parts    []part `json:"parts"`
}

func (p manifest) size() int64 {
	var n int64
	for _, v := range p.Parts {
		n += v.Size
	}
	return n
}
func (p manifest) check(engine string) error {
	if p.Engine != engine || !supported(engine) || !assetName.MatchString(p.Version) || !hexID(p.Hash, 64) || len(p.Parts) == 0 || len(p.Parts) > 100 || p.Expanded <= 0 || p.Expanded > 250_000_000_000 {
		return errors.New("引擎包清單無效。")
	}
	for i, v := range p.Parts {
		if v.Name != fmt.Sprintf("%s-%s.zip.%03d", engine, p.Version, i+1) || v.Size <= 0 || v.Size > 1_800_000_000 || !hexID(v.Hash, 64) {
			return errors.New("引擎包分卷資料無效。")
		}
	}
	return nil
}
func fetchManifest(ctx context.Context, engine string) (manifest, error) {
	var p manifest
	if !supported(engine) {
		return p, errors.New("此引擎尚未接通。")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", packURL+"packs-v1/"+engine+".json", nil)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return p, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return p, errors.New("這款引擎尚未發佈可下載的 Windows 引擎包。")
	}
	if resp.StatusCode != 200 {
		return p, fmt.Errorf("讀取發佈清單失敗（HTTP %d）。", resp.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&p); e != nil {
		return p, e
	}
	return p, p.check(engine)
}
func (s *studio) packInfo(w http.ResponseWriter, r *http.Request) {
	p, e := fetchManifest(r.Context(), r.PathValue("engine"))
	if e != nil {
		fail(w, 424, e.Error())
		return
	}
	free, e := freeSpace(s.root)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	send(w, 200, map[string]any{"bytes": p.size(), "expanded": p.Expanded, "free": free, "hash": p.Hash, "version": p.Version})
}
func (s *studio) install(w http.ResponseWriter, r *http.Request) {
	engine := r.PathValue("engine")
	var in struct{ Hash string }
	if !decode(w, r, &in) {
		return
	}
	p, e := fetchManifest(r.Context(), engine)
	if e != nil {
		fail(w, 424, e.Error())
		return
	}
	if in.Hash != p.Hash {
		fail(w, 409, "發佈版本已變更，請重新查看容量。")
		return
	}
	ctx, j, ok := s.reserve(engine, "install")
	if !ok {
		fail(w, 409, "已有作業進行中。")
		return
	}
	go func() { s.finish(s.installPack(ctx, p, packURL+"packs-v1/")) }()
	send(w, 202, j)
}
func verify(ctx context.Context, path, hash string) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, checkingReader{ctx, f})
	return e == nil && hex.EncodeToString(h.Sum(nil)) == hash
}

type checkingReader struct {
	ctx context.Context
	r   io.Reader
}

func (c checkingReader) Read(b []byte) (int, error) {
	if e := c.ctx.Err(); e != nil {
		return 0, e
	}
	return c.r.Read(b)
}
func downloadPart(ctx context.Context, url, path string, v part) error {
	var offset int64
	if st, e := os.Stat(path); e == nil {
		offset = st.Size()
	}
	if offset == v.Size && verify(ctx, path, v.Hash) {
		return nil
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if offset >= v.Size {
		offset = 0
	}
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return e
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE
	switch resp.StatusCode {
	case 200:
		offset = 0
		flags |= os.O_TRUNC
	case 206:
		if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, v.Size-1, v.Size) {
			return errors.New("分卷續傳範圍不符。")
		}
		flags |= os.O_APPEND
	default:
		return fmt.Errorf("下載分卷失敗（HTTP %d）。", resp.StatusCode)
	}
	f, e := os.OpenFile(path, flags, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(f, io.LimitReader(resp.Body, v.Size-offset+1))
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if n != v.Size-offset {
		return errors.New("分卷下載不完整，可再次安裝以續傳。")
	}
	if !verify(ctx, path, v.Hash) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		os.Remove(path)
		return errors.New("分卷雜湊不符，已捨棄損壞分卷。")
	}
	return nil
}

// Read the multipart ZIP in place so installation never needs a second compressed copy.
type segment struct {
	f          *os.File
	start, end int64
}
type segments []segment

func (parts segments) ReadAt(b []byte, off int64) (int, error) {
	n := 0
	for _, p := range parts {
		if len(b) == 0 {
			break
		}
		if off < p.start || off >= p.end {
			continue
		}
		length := min(int64(len(b)), p.end-off)
		got, e := p.f.ReadAt(b[:length], off-p.start)
		n += got
		if e != nil {
			return n, e
		}
		b = b[got:]
		off += int64(got)
	}
	if len(b) > 0 {
		return n, io.EOF
	}
	return n, nil
}
func expand(ctx context.Context, source io.ReaderAt, size int64, dst string, limit int64) error {
	z, e := zip.NewReader(source, size)
	if e != nil {
		return e
	}
	var total int64
	for _, f := range z.File {
		if e := ctx.Err(); e != nil {
			return e
		}
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) || strings.ContainsAny(f.Name, "\\:") || strings.Contains("/"+f.Name+"/", "/../") {
			return errors.New("引擎包路徑無效。")
		}
		path := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			if e = os.MkdirAll(path, 0700); e != nil {
				return e
			}
			continue
		}
		if !f.Mode().IsRegular() || f.UncompressedSize64 > uint64(limit-total) {
			return errors.New("引擎包檔案類型或容量無效。")
		}
		total += int64(f.UncompressedSize64)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		input, e := f.Open()
		if e != nil {
			return e
		}
		out, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if e != nil {
			input.Close()
			return e
		}
		_, copyErr := io.Copy(out, checkingReader{ctx, input})
		input.Close()
		e = out.Close()
		if copyErr != nil {
			return copyErr
		}
		if e != nil {
			return e
		}
	}
	if total != limit {
		return errors.New("引擎包展開大小不符。")
	}
	return nil
}
func (s *studio) installPack(ctx context.Context, p manifest, baseURL string) error {
	cache := s.path("cache", "packs", p.Hash)
	if e := os.MkdirAll(cache, 0700); e != nil {
		return e
	}
	need := p.Expanded + p.size()
	for _, v := range p.Parts {
		if st, e := os.Stat(filepath.Join(cache, v.Name)); e == nil {
			need -= min(st.Size(), v.Size)
		}
	}
	free, e := freeSpace(s.root)
	if e != nil {
		return e
	}
	if free < need {
		return errors.New("磁碟可用空間不足。")
	}
	parts := segments{}
	defer func() {
		for _, p := range parts {
			p.f.Close()
		}
	}()
	var off int64
	for i, v := range p.Parts {
		s.detail(fmt.Sprintf("下載及校驗第 %d / %d 卷", i+1, len(p.Parts)))
		path := filepath.Join(cache, v.Name)
		if e = downloadPart(ctx, baseURL+v.Name, path, v); e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		parts = append(parts, segment{f, off, off + v.Size})
		off += v.Size
	}
	s.detail("校驗整包並解壓")
	h := sha256.New()
	if _, e = io.Copy(h, checkingReader{ctx, io.NewSectionReader(parts, 0, off)}); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != p.Hash {
		return errors.New("整包校驗不符。")
	}
	parent := s.path("runtimes", p.Engine)
	if e = os.MkdirAll(parent, 0700); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(parent, ".install-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	if e = expand(ctx, parts, off, stage, p.Expanded); e != nil {
		return e
	}
	if !s.inspect(p.Engine, map[string]string{p.Engine: stage}).Ready {
		return errors.New("引擎包缺少必要檔案。")
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	s.files.Lock()
	defer s.files.Unlock()
	paths, e := s.paths()
	if e != nil {
		return e
	}
	name := "pack-" + p.Hash[:16]
	dest := filepath.Join(parent, name)
	if !s.inspect(p.Engine, map[string]string{p.Engine: dest}).Ready {
		if _, e := os.Stat(dest); e == nil {
			name += "-" + id()[:8]
			dest = filepath.Join(parent, name)
		}
		if e = os.Rename(stage, dest); e != nil {
			return e
		}
	}
	paths[p.Engine] = name
	if e = writeJSON(s.path("config", "engines.json"), paths); e != nil {
		return e
	}
	for _, part := range parts {
		part.f.Close()
	}
	parts = nil
	os.RemoveAll(cache)
	return nil
}
