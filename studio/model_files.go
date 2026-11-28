package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func modelFolder(engine, variant string) string {
	switch engine {
	case "gpt-sovits":
		return "GPT_SoVITS/pretrained_models"
	case "cosyvoice3":
		return "pretrained_models/Fun-CosyVoice3-0.5B"
	case "voxcpm2":
		return "pretrained_models/VoxCPM2"
	case "indextts2":
		return "checkpoints"
	case "qwen3-tts":
		return "pretrained_models/Qwen3-TTS-12Hz-1.7B-" + variant
	}
	return ""
}

func runtimeRequirements(engine string) []string {
	var files []string
	for _, file := range required(engine) {
		if !strings.HasPrefix(file, modelFolder(engine, "")+"/") {
			files = append(files, file)
		}
	}
	return files
}

func modelMissing(engine, variant, root string) []string {
	if engine == "gpt-sovits" && gptV5(variant) {
		base := modelFolder(engine, variant) + "/"
		return missingFiles(root, []string{
			base + "s1v3.ckpt", base + "gsv-v5-pretrained/s2G" + variant + ".pth", base + "gsv-v5-pretrained/vocoder.pth",
			base + "chinese-roberta-wwm-ext-large/config.json",
			base + "chinese-roberta-wwm-ext-large/model.safetensors|" + base + "chinese-roberta-wwm-ext-large/pytorch_model.bin",
			base + "chinese-hubert-base/config.json",
			base + "chinese-hubert-base/model.safetensors|" + base + "chinese-hubert-base/pytorch_model.bin",
		})
	}
	if engine == "qwen3-tts" {
		model := filepath.Join(root, modelFolder(engine, variant))
		missing := missingFiles(model, []string{
			"config.json", "generation_config.json", "tokenizer_config.json",
			"speech_tokenizer/config.json", "speech_tokenizer/model.safetensors",
		})
		if !modelWeightsAvailable(model) {
			missing = append(missing, "model.safetensors")
		}
		for i := range missing {
			missing[i] = modelFolder(engine, variant) + "/" + missing[i]
		}
		return missing
	}
	var requirements []string
	for _, file := range required(engine) {
		if strings.HasPrefix(file, modelFolder(engine, variant)+"/") {
			requirements = append(requirements, file)
		}
	}
	missing := missingFiles(root, requirements)
	if engine == "indextts2" {
		missing = append(missing, missingIndexCaches(root)...)
	}
	return missing
}

func (s *studio) runtimePath(engine, selection string) string {
	if filepath.IsAbs(selection) {
		return selection
	}
	return s.path("runtimes", engine, selection)
}

func (s *studio) inspect(engine string, paths map[string]string) installation {
	root := s.runtimePath(engine, paths[engine])
	v := installation{Path: root, engineCapabilities: engineCatalog[engine], External: filepath.IsAbs(paths[engine])}
	if !supported(engine) {
		return v
	}
	v.Missing = missingFiles(root, runtimeRequirements(engine))
	v.EngineInstalled = len(v.Missing) == 0
	var missingModel []string
	for _, variant := range v.Variants {
		if engine == "gpt-sovits" && gptV5(variant) {
			continue // The existing v2Pro runtime stays separate.
		}
		missing := modelMissing(engine, variant, root)
		if len(missing) == 0 {
			v.AvailableVariants = append(v.AvailableVariants, variant)
		} else {
			missingModel = append(missingModel, missing...)
		}
	}
	if len(v.AvailableVariants) == 0 {
		v.Missing = append(v.Missing, missingModel...)
	}
	v.Ready = v.EngineInstalled && len(v.AvailableVariants) > 0
	return v
}

func gptV5Runtime(root string) bool {
	return filepath.IsAbs(root) && len(missingFiles(root, []string{"runtime/python.exe",
		"GPT_SoVITS/TTS_infer_pack/TTS.py", "GPT_SoVITS/module/models_v5.py", "tools/portable_runtime.py"})) == 0
}

func packFolders(parent string) []string {
	entries, _ := os.ReadDir(parent)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && installedPackName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

func (s *studio) inspectSelected(engine string, paths map[string]string) installation {
	v := s.inspect(engine, paths)
	v.variantPaths = map[string]string{}
	v.modelPaths = map[string]string{}
	available := v.AvailableVariants
	v.AvailableVariants = nil
	var alternatives []installation
	if engine == "qwen3-tts" {
		for _, name := range packFolders(s.path("runtimes", engine)) {
			candidate := s.inspect(engine, map[string]string{engine: name})
			if candidate.Ready && candidate.Path != v.Path {
				alternatives = append(alternatives, candidate)
			}
		}
	}
	if engine == "gpt-sovits" {
		v.GPTV5Path = paths[gptV5Link]
		v.GPTV5Installed = gptV5Runtime(v.GPTV5Path)
	}
	for _, variant := range v.Variants {
		runtimeRoot, runtimeReady := v.Path, v.EngineInstalled
		bundled := slices.Contains(available, variant)
		if engine == "gpt-sovits" && gptV5(variant) {
			runtimeRoot, runtimeReady = v.GPTV5Path, v.GPTV5Installed
			if !runtimeReady {
				continue
			}
			bundled = len(modelMissing(engine, variant, runtimeRoot)) == 0
		}
		runtime, models := "", ""
		parent := s.path("models", engine, variant)
		var selection string
		_ = readJSON(filepath.Join(parent, "selected.json"), &selection)
		if installedPackName.MatchString(selection) {
			candidate := filepath.Join(parent, selection)
			if len(modelMissing(engine, variant, candidate)) == 0 {
				models = candidate
			}
		}
		if models == "" && bundled {
			models = runtimeRoot
		}
		if models != "" {
			if runtimeReady {
				runtime = runtimeRoot
			}
		}
		if models == "" {
			for _, id := range packFolders(parent) {
				candidate := filepath.Join(parent, id)
				if len(modelMissing(engine, variant, candidate)) == 0 {
					models = candidate
					if runtimeReady {
						runtime = runtimeRoot
					}
					break
				}
			}
		}
		if runtime == "" {
			for _, candidate := range alternatives {
				if slices.Contains(candidate.AvailableVariants, variant) {
					runtime, models = candidate.Path, candidate.Path
					break
				}
			}
		}
		if models != "" {
			v.AvailableVariants = append(v.AvailableVariants, variant)
			v.modelPaths[variant] = models
		}
		if runtime != "" {
			v.variantPaths[variant] = runtime
		}
	}
	v.EngineInstalled = v.EngineInstalled || v.GPTV5Installed
	v.Ready = len(v.variantPaths) > 0
	if v.Ready {
		v.Missing = nil
	}
	return v
}
