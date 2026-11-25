package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
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

type rangeReleaseFixture struct {
	releaseFixture
	ranges []string
}

func (f *rangeReleaseFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if value := r.Header.Get("Range"); value != "" {
		part := f.releaseFixture[r.URL.String()]
		var offset int
		if _, err := fmt.Sscanf(value, "bytes=%d-", &offset); err != nil || offset > len(part) {
			return nil, fmt.Errorf("bad range %s", value)
		}
		f.ranges = append(f.ranges, value)
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {
			fmt.Sprintf("bytes %d-%d/%d", offset, len(part)-1, len(part)),
		}}, Body: io.NopCloser(bytes.NewReader(part[offset:]))}, nil
	}
	return f.releaseFixture.RoundTrip(r)
}

func addHubFixture(t *testing.T, responses releaseFixture, repo, commit string, paths []string) {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"sha": commit})
	responses[hfURL("/api/models/"+repo)] = data
	var listing []repoEntry
	for _, path := range paths {
		body := []byte("fixture " + path)
		entry := repoEntry{Path: path, Type: "file", Size: int64(len(body))}
		digest := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(body))), body...))
		entry.OID = hex.EncodeToString(digest[:])
		if strings.HasSuffix(path, ".safetensors") {
			lfsDigest := sha256.Sum256(body)
			entry.LFS = &struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			}{hex.EncodeToString(lfsDigest[:]), entry.Size}
		}
		listing = append(listing, entry)
		responses[hfURL("/"+repo+"/resolve/"+commit+"/"+path)] = body
	}
	data, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	responses[hfURL("/api/models/"+repo+"/tree/"+commit)+"?recursive=true"] = data
}

func TestFallbackOfficialModelDownload(t *testing.T) {
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
	responses := &rangeReleaseFixture{releaseFixture: releaseFixture{}}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: responses}
	t.Cleanup(func() { http.DefaultClient = old })
	const repo = "Qwen/Qwen3-TTS-12Hz-1.7B-Base"
	revision := strings.Repeat("a", 40)
	addHubFixture(t, responses.releaseFixture, repo, revision, []string{
		"config.json", "generation_config.json", "tokenizer_config.json", "model.safetensors",
		"speech_tokenizer/config.json", "speech_tokenizer/model.safetensors", "unrelated.py",
	})
	const query = "qwen3-tts?kind=model&variant=Base"
	var offer struct {
		Hash   string
		Source string
		Bytes  int64
	}
	if err := json.Unmarshal(call("GET", "pack/"+query, "", 200), &offer); err != nil ||
		!strings.Contains(offer.Source, "Hugging Face") || !hexID(offer.Hash, 64) || offer.Bytes == 0 {
		t.Fatal(offer, err)
	}
	call("POST", "install/"+query, `{"hash":"changed"}`, 409)

	start := func() {
		call("POST", "install/"+query, `{"hash":"`+offer.Hash+`"}`, 202)
	}
	wait := func(expected string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var job job
			if err := json.Unmarshal(call("GET", "job", "", 200), &job); err != nil {
				t.Fatal(err)
			}
			if job.Status == expected {
				return
			}
			if job.Status != "running" || time.Now().After(deadline) {
				t.Fatal(job)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	target := hfURL("/" + repo + "/resolve/" + revision + "/model.safetensors")
	good := responses.releaseFixture[target]
	responses.releaseFixture[target] = bytes.Repeat([]byte{'x'}, len(good))
	start()
	wait("failed")
	if available := call("GET", "installed-models/qwen3-tts", "", 200); string(available) != "[]\n" {
		t.Fatal(string(available))
	}

	responses.releaseFixture[target] = good
	stage := s.path("models", "qwen3-tts", "Base", ".upstream-"+offer.Hash)
	staged := filepath.Join(stage, modelFolder("qwen3-tts", "Base"), "model.safetensors")
	if err := os.WriteFile(staged, good[:7], 0600); err != nil {
		t.Fatal(err)
	}
	start()
	wait("done")
	if len(responses.ranges) != 1 || responses.ranges[0] != "bytes=7-" {
		t.Fatal("missing resumed range", responses.ranges)
	}
	var installed []modelAsset
	if err := json.Unmarshal(call("GET", "installed-models/qwen3-tts", "", 200), &installed); err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0].Provider != "Hugging Face" || installed[0].Version != "hf-aaaaaaaaaaaa" {
		t.Fatal(installed)
	}
	modelRoot := filepath.Join(installed[0].Path, modelFolder("qwen3-tts", "Base"))
	if _, err := os.Stat(filepath.Join(modelRoot, "unrelated.py")); !os.IsNotExist(err) {
		t.Fatal("unrelated file downloaded", err)
	}
	call("DELETE", "installed-models/qwen3-tts/Base/"+installed[0].ID+"?source=model", "", 200)
	if _, err := os.Stat(modelRoot); !os.IsNotExist(err) {
		t.Fatal("independent model was not removed", err)
	}
}

func TestIndexTTSDownloadsOfflineCaches(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	responses := releaseFixture{}
	http.DefaultClient = &http.Client{Transport: responses}
	t.Cleanup(func() { http.DefaultClient = old })
	type repo struct {
		name     string
		expected []string
	}
	repos := []repo{
		{"IndexTeam/IndexTTS-2", []string{"config.yaml", "gpt.pth", "s2mel.pth", "bpe.model",
			"wav2vec2bert_stats.pt", "feat1.pt", "feat2.pt", "qwen0.6bemo4-merge/config.json",
			"qwen0.6bemo4-merge/model.safetensors"}},
		{"facebook/w2v-bert-2.0", []string{"config.json", "preprocessor_config.json", "model.safetensors"}},
		{"amphion/MaskGCT", []string{"semantic_codec/model.safetensors"}},
		{"funasr/campplus", []string{"campplus_cn_common.bin"}},
		{"nvidia/bigvgan_v2_22khz_80band_256x", []string{"config.json", "bigvgan_generator.pt"}},
	}
	for i, repo := range repos {
		addHubFixture(t, responses, repo.name, strings.Repeat(fmt.Sprint(i+1), 40), repo.expected)
	}
	ctx, _, ok := s.reserve("indextts2", "install")
	if !ok {
		t.Fatal("could not reserve installer")
	}
	plan, err := fetchUpstreamPlan(ctx, "indextts2", "IndexTTS-2")
	if err == nil {
		err = s.installUpstreamModel(ctx, plan)
	}
	s.finish(err)
	if err != nil {
		t.Fatal(err)
	}
	parent := s.path("models", "indextts2", "IndexTTS-2")
	var selected string
	if err := readJSON(filepath.Join(parent, "selected.json"), &selected); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, selected)
	if len(plan.Sources) != 5 || len(plan.Refs) != 4 || len(modelMissing("indextts2", plan.Variant, root)) != 0 {
		t.Fatal("incomplete offline cache", plan.Sources, plan.Refs)
	}
	ref := filepath.Join(root, "checkpoints/hf_cache/models--amphion--MaskGCT/refs/main")
	if found, err := os.ReadFile(ref); err != nil || string(found) != strings.Repeat("3", 40) {
		t.Fatal(string(found), err)
	}
}
