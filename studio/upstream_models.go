package main

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const hfHost = "huggingface.co"
const upstreamMetadata = ".timbredeck-upstream.json"

type upstreamModel struct {
	Engine  string   `json:"engine"`
	Variant string   `json:"variant"`
	Version string   `json:"version"`
	Hash    string   `json:"sha256"`
	Bytes   int64    `json:"bytes"`
	Sources []string `json:"sources"`
}

type upstreamFile struct {
	Path   string
	URL    string
	Bytes  int64
	Digest string
}

type upstreamPlan struct {
	upstreamModel
	Files []upstreamFile
	Refs  []struct{ Path, Revision string }
}

type upstreamRepo struct {
	repo    string
	include []string
	cache   bool
}

func officialRepos(engine, variant string) []upstreamRepo {
	license := []string{"LICENSE", "LICENSE.txt", "LICENSE_ZH.txt"}
	switch engine {
	case "gpt-sovits":
		if gptV5(variant) {
			return []upstreamRepo{{"lj1995/GPT-SoVITS", append(license,
				"s1v3.ckpt", "gsv-v5-pretrained/s2G"+variant+".pth", "gsv-v5-pretrained/vocoder.pth",
				"chinese-roberta-wwm-ext-large/", "chinese-hubert-base/"), false}}
		}
		return []upstreamRepo{{"lj1995/GPT-SoVITS", append(license,
			"s1v3.ckpt", "v2Pro/s2Gv2Pro.pth", "v2Pro/s2Dv2Pro.pth",
			"sv/pretrained_eres2netv2w24s4ep4.ckpt", "chinese-roberta-wwm-ext-large/", "chinese-hubert-base/"), false}}
	case "cosyvoice3":
		return []upstreamRepo{{"FunAudioLLM/Fun-CosyVoice3-0.5B-2512", append(license,
			"cosyvoice3.yaml", "llm.pt", "flow.pt", "hift.pt", "campplus.onnx",
			"speech_tokenizer_v3.onnx", "speech_tokenizer_v3.onnx.data", "spk2info.pt", "CosyVoice-BlankEN/"), false}}
	case "voxcpm2":
		return []upstreamRepo{{"openbmb/VoxCPM2", append(license,
			"config.json", "model.safetensors", "pytorch_model.bin", "audiovae.safetensors", "audiovae.pth",
			"tokenizer.json", "tokenizer.model", "tokenizer_config.json", "special_tokens_map.json", "added_tokens.json"), false}}
	case "indextts2":
		return []upstreamRepo{
			{"IndexTeam/IndexTTS-2", append(license, "config.yaml", "gpt.pth", "s2mel.pth", "bpe.model",
				"wav2vec2bert_stats.pt", "feat1.pt", "feat2.pt", "qwen0.6bemo4-merge/", "pinyin.vocab", "glossary.yaml"), false},
			{"facebook/w2v-bert-2.0", []string{"config.json", "preprocessor_config.json", "model.safetensors", "pytorch_model.bin"}, true},
			{"amphion/MaskGCT", []string{"semantic_codec/model.safetensors"}, true},
			{"funasr/campplus", []string{"campplus_cn_common.bin"}, true},
			{"nvidia/bigvgan_v2_22khz_80band_256x", []string{"config.json", "bigvgan_generator.pt"}, true},
		}
	case "qwen3-tts":
		return []upstreamRepo{{"Qwen/Qwen3-TTS-12Hz-1.7B-" + variant, append(license,
			"config.json", "generation_config.json", "model.safetensors", "model.safetensors.index.json", "model-*.safetensors",
			"tokenizer.json", "tokenizer_config.json", "vocab.json", "merges.txt", "preprocessor_config.json",
			"special_tokens_map.json", "added_tokens.json", "speech_tokenizer/"), false}}
	}
	return nil
}

type repoEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

func upstreamClient() *http.Client {
	client := *http.DefaultClient
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		host := r.URL.Hostname()
		if r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Port() != "" || len(via) >= 8 ||
			(host != hfHost && !strings.HasSuffix(host, ".hf.co") && !strings.HasSuffix(host, ".huggingface.co")) {
			return errors.New("官方模型下載被轉址至無法驗證的位置。")
		}
		return nil
	}
	return &client
}

func hfURL(path string) string {
	return (&url.URL{Scheme: "https", Host: hfHost, Path: path}).String()
}

func hubJSON(ctx context.Context, client *http.Client, address string, result any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("無法連接 Hugging Face 官方模型來源：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("讀取官方模型資料失敗（HTTP %d）。", resp.StatusCode)
	}
	const maxMetadata = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil || len(data) > maxMetadata {
		return nil, errors.New("官方模型資料不完整或過大。")
	}
	return resp.Header, json.Unmarshal(data, result)
}

func hubEntries(ctx context.Context, client *http.Client, repo, revision string) ([]repoEntry, error) {
	tree := "/api/models/" + repo + "/tree/" + revision
	address := hfURL(tree) + "?recursive=true"
	var result []repoEntry
	for range 16 {
		var entries []repoEntry
		header, err := hubJSON(ctx, client, address, &entries)
		if err != nil {
			return nil, err
		}
		result = append(result, entries...)
		var next string
		for _, link := range strings.Split(header.Get("Link"), ",") {
			target, attributes, ok := strings.Cut(strings.TrimSpace(link), ">")
			if ok && strings.Contains(attributes, `rel="next"`) {
				next = strings.TrimPrefix(target, "<")
				break
			}
		}
		if next == "" {
			return result, nil
		}
		base, _ := url.Parse(address)
		current, err := base.Parse(next)
		if err != nil || current.Scheme != "https" || current.Host != hfHost || current.User != nil || current.Path != tree {
			return nil, errors.New("模型分頁位置無效。")
		}
		address = current.String()
	}
	return nil, errors.New("官方模型檔案清單過大。")
}

func chooseUpstreamFiles(entries []repoEntry, patterns []string) []repoEntry {
	var chosen []repoEntry
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		for _, p := range patterns {
			if entry.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(entry.Path, p)) ||
				(p == "model-*.safetensors" && !strings.Contains(entry.Path, "/") &&
					strings.HasPrefix(entry.Path, "model-") && strings.HasSuffix(entry.Path, ".safetensors")) {
				base := filepath.Base(entry.Path)
				switch base {
				case ".gitattributes", "README.md", "tf_model.h5", "flax_model.msgpack":
					continue
				}
				if strings.HasPrefix(base, ".") {
					continue
				}
				chosen = append(chosen, entry)
				seen[entry.Path] = true
				break
			}
		}
	}
	return slices.DeleteFunc(chosen, func(f repoEntry) bool {
		return (strings.HasSuffix(f.Path, "pytorch_model.bin") &&
			seen[strings.TrimSuffix(f.Path, "pytorch_model.bin")+"model.safetensors"]) ||
			(f.Path == "audiovae.pth" && seen["audiovae.safetensors"])
	})
}

func fetchUpstreamPlan(ctx context.Context, engine, variant string) (upstreamPlan, error) {
	plan := upstreamPlan{upstreamModel: upstreamModel{Engine: engine, Variant: variant}}
	if !validPackTarget(engine, "model", variant) {
		return plan, errors.New("模型選擇無效。")
	}
	ctx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	client := upstreamClient()
	root := modelFolder(engine, variant)
	seen := map[string]bool{}
	for _, source := range officialRepos(engine, variant) {
		var info struct{ SHA string }
		if _, err := hubJSON(ctx, client, hfURL("/api/models/"+source.repo), &info); err != nil {
			return plan, err
		}
		if !hexID(info.SHA, 40) {
			return plan, errors.New("無法鎖定官方模型版本。")
		}
		if plan.Version == "" {
			plan.Version = "hf-" + info.SHA[:12]
		}
		plan.Sources = append(plan.Sources, source.repo+"@"+info.SHA)
		entries, err := hubEntries(ctx, client, source.repo, info.SHA)
		if err != nil {
			return plan, err
		}
		files := chooseUpstreamFiles(entries, source.include)
		if len(files) == 0 {
			return plan, fmt.Errorf("官方模型 %s 缺少所需檔案。", source.repo)
		}
		dest := root
		if source.cache {
			cache := root + "/hf_cache/models--" + strings.ReplaceAll(source.repo, "/", "--")
			dest = cache + "/snapshots/" + info.SHA
			plan.Refs = append(plan.Refs, struct{ Path, Revision string }{cache + "/refs/main", info.SHA})
		}
		for _, file := range files {
			target := dest + "/" + file.Path
			if !filepath.IsLocal(filepath.FromSlash(file.Path)) || strings.ContainsAny(file.Path, ":\\") ||
				strings.Contains("/"+file.Path+"/", "/../") || strings.HasSuffix(file.Path, "/") ||
				seen[strings.ToLower(target)] {
				return plan, errors.New("官方模型檔案路徑無效或重複。")
			}
			size, digest := file.Size, strings.ToLower(file.OID)
			if file.LFS != nil {
				size, digest = file.LFS.Size, strings.ToLower(file.LFS.OID)
			}
			if (file.LFS != nil && !hexID(digest, 64)) || (file.LFS == nil && !hexID(digest, 40)) ||
				size < 0 || size > 50_000_000_000 || plan.Bytes+size > 250_000_000_000 || len(plan.Files) >= 1200 {
				return plan, errors.New("官方模型缺少可驗證的檔案雜湊或容量。")
			}
			seen[strings.ToLower(target)] = true
			plan.Bytes += size
			plan.Files = append(plan.Files, upstreamFile{target,
				hfURL("/" + source.repo + "/resolve/" + info.SHA + "/" + file.Path), size, digest})
		}
	}
	if plan.Bytes <= 0 {
		return plan, errors.New("找不到可下載的官方模型。")
	}
	slices.SortFunc(plan.Files, func(a, b upstreamFile) int { return strings.Compare(a.Path, b.Path) })
	data, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	h := sha256.Sum256(data)
	plan.Hash = hex.EncodeToString(h[:])
	return plan, nil
}

func verifyUpstreamFile(ctx context.Context, path string, expected upstreamFile) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h = sha256.New()
	if len(expected.Digest) == 40 {
		h = sha1.New() // Git blob IDs also hash the canonical blob header.
		fmt.Fprintf(h, "blob %d\x00", expected.Bytes)
	}
	size, err := io.Copy(h, checkingReader{ctx, f})
	return err == nil && size == expected.Bytes && hex.EncodeToString(h.Sum(nil)) == expected.Digest
}

func (s *studio) ownedUpstream(root, engine, variant, key string) (upstreamModel, bool) {
	var p upstreamModel
	if !installedPackName.MatchString(key) || !s.managedPath(root) ||
		readJSON(filepath.Join(root, upstreamMetadata), &p) != nil || !hexID(p.Hash, 64) ||
		p.Engine != engine || p.Variant != variant || !assetName.MatchString(p.Version) {
		return p, false
	}
	return p, strings.HasPrefix(key, "pack-"+p.Hash[:16])
}

func (s *studio) installUpstreamModel(ctx context.Context, plan upstreamPlan) error {
	parent := s.path("models", plan.Engine, plan.Variant)
	name := "pack-" + plan.Hash[:16]
	destination := filepath.Join(parent, name)
	if installed, owned := s.ownedUpstream(destination, plan.Engine, plan.Variant, name); owned &&
		installed.Hash == plan.Hash && len(modelMissing(plan.Engine, plan.Variant, destination)) == 0 {
		s.files.Lock()
		defer s.files.Unlock()
		return writeJSON(filepath.Join(parent, "selected.json"), name)
	}
	stage := filepath.Join(parent, ".upstream-"+plan.Hash)
	if err := s.ensureManagedDirectory(stage); err != nil {
		return err
	}
	free, err := freeSpace(parent)
	if err != nil {
		return err
	}
	need := plan.Bytes
	for _, entry := range plan.Files {
		path := filepath.Join(stage, entry.Path)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() || !s.managedPath(path) {
				return errors.New("模型暫存檔案位置無效。")
			}
			need -= min(info.Size(), entry.Bytes)
		}
	}
	if free < need {
		return errors.New("磁碟可用空間不足。")
	}
	client := upstreamClient()
	for i, entry := range plan.Files {
		s.detail(fmt.Sprintf("下載官方模型檔案 %d / %d", i+1, len(plan.Files)))
		dest := filepath.Join(stage, entry.Path)
		if err := s.ensureManagedDirectory(filepath.Dir(dest)); err != nil {
			return err
		}
		if err := downloadVerifiedFile(ctx, client, entry.URL, dest, entry.Bytes, func() bool {
			return verifyUpstreamFile(ctx, dest, entry)
		}); err != nil {
			return fmt.Errorf("%s：%w", filepath.Base(dest), err)
		}
	}
	for _, pointer := range plan.Refs {
		dest := filepath.Join(stage, pointer.Path)
		if err := s.ensureManagedDirectory(filepath.Dir(dest)); err != nil {
			return err
		}
		if _, err := os.Lstat(dest); err == nil && !s.managedPath(dest) {
			return errors.New("模型快取版本位置無效。")
		}
		if err := os.WriteFile(dest, []byte(pointer.Revision), 0600); err != nil {
			return err
		}
	}
	if missing := modelMissing(plan.Engine, plan.Variant, stage); len(missing) > 0 {
		return fmt.Errorf("官方模型缺少必要檔案：%s", strings.Join(missing, "、"))
	}
	if plan.Engine == "gpt-sovits" {
		folder := filepath.Join(stage, modelFolder(plan.Engine, plan.Variant))
		for _, sub := range []string{"chinese-roberta-wwm-ext-large", "chinese-hubert-base"} {
			if len(missingFiles(filepath.Join(folder, sub), []string{"model.safetensors|pytorch_model.bin"})) > 0 {
				return fmt.Errorf("官方模型缺少 %s 的權重。", sub)
			}
		}
	}
	if err := writeJSON(filepath.Join(stage, upstreamMetadata), plan.upstreamModel); err != nil {
		return err
	}
	s.files.Lock()
	defer s.files.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		name += "-" + id()[:8]
		destination = filepath.Join(parent, name)
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	return writeJSON(filepath.Join(parent, "selected.json"), name)
}
