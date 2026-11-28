package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGPTV5SeparateRuntimeAndInferenceOnly(t *testing.T) {
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(root string, files ...string) {
		t.Helper()
		for _, f := range files {
			path := filepath.Join(root, strings.Split(strings.ReplaceAll(f, " 或 ", "|"), "|")[0])
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	v2, v5 := s.path("v2"), s.path("v5")
	put(v2, required("gpt-sovits")...)
	paths := map[string]string{"gpt-sovits": v2, gptV5Link: v2}
	if got := s.inspectSelected("gpt-sovits", paths); !slices.Equal(got.AvailableVariants, []string{"v2Pro"}) {
		t.Fatalf("V2 does not run V5: %+v", got.AvailableVariants)
	}
	put(v5, "runtime/python.exe", "GPT_SoVITS/TTS_infer_pack/TTS.py", "GPT_SoVITS/module/models_v5.py", "tools/portable_runtime.py")
	put(v5, modelMissing("gpt-sovits", "v5turbo", v5)...)
	paths[gptV5Link] = v5
	got := s.inspectSelected("gpt-sovits", paths)
	if got.variantPaths["v2Pro"] != v2 || got.variantPaths["v5turbo"] != v5 || got.variantPaths["v5dev"] != "" {
		t.Fatalf("incorrect variant discovery: %+v", got.variantPaths)
	}
	h := s.routes(cleanTestHost)
	key := strings.Repeat("b", 64)
	if err := os.WriteFile(s.path("voices", key+".wav"), toneFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	cleanCall(t, h, "POST", "/api/synthesize",
		[]byte("{\"engine\":\"gpt-sovits\",\"mode\":\"clone\",\"variant\":\"v5turbo\",\"reference\":\""+key+"\",\"language\":\"ja\",\"refLang\":\"ja\",\"text\":\"日本語。\",\"transcript\":\"日本語。\",\"speed\":1,\"modelProject\":\""+strings.Repeat("a", 32)+"\"}"), 422)
	cleanCall(t, h, "POST", "/api/tts/warm",
		[]byte("{\"engine\":\"gpt-sovits\",\"variant\":\"v5dev\",\"modelProject\":\""+strings.Repeat("a", 32)+"\"}"), 422)
}

func TestGPTV5NeedsCudaVocoderEvidence(t *testing.T) {
	r := recording{ID: "job", Input: request{Engine: "gpt-sovits", Mode: "clone", Variant: "v5turbo"}}
	e := executionEvidence{Job: "job", Engine: r.Input.Engine, Mode: r.Input.Mode, Model: "v5turbo", Platform: "win32",
		Torch: "2.8", CUDA: "12.8", Waveform: []string{"cuda:0"},
		Modules: map[string][]string{"t2s_model": {"cuda:0"}, "vits_model": {"cuda:0"}, "vocoder": {"cpu"}},
	}
	e.Devices = map[string]struct {
		Name string "json:\"name\""
	}{"cuda:0": {Name: "RTX 4090"}}
	if e.verifiedDevice(r) != "" {
		t.Fatal("CPU vocoder accepted")
	}
	e.Modules["vocoder"] = []string{"cuda:0"}
	if e.verifiedDevice(r) == "" {
		t.Fatal("matching V5 CUDA evidence rejected")
	}
	e.Model = "v5dev"
	if e.verifiedDevice(r) != "" {
		t.Fatal("wrong V5 variant accepted")
	}
}
