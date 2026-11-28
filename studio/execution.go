package main

import "regexp"

// Evidence belongs to a single completed job, not the presence of a CUDA driver.
type executionEvidence struct {
	Job, Engine, Mode, Model, Platform string
	Waveform                           []string            `json:"waveform_devices"`
	Modules                            map[string][]string `json:"module_devices"`
	Devices                            map[string]struct {
		Name string `json:"name"`
	} `json:"cuda_devices"`
	Torch   string        `json:"torch_version"`
	CUDA    string        `json:"cuda_version"`
	Timings *workerTiming `json:"timings,omitempty"`
}

type workerTiming struct {
	Load     *float64 `json:"load_seconds"`
	Generate *float64 `json:"generate_seconds"`
	Reused   bool     `json:"reused"`
}

type generationTiming struct {
	Load     *float64 `json:"coldStartSeconds,omitempty"`
	Generate *float64 `json:"generationSeconds,omitempty"`
	Total    float64  `json:"totalSeconds"`
	Resident bool     `json:"resident,omitempty"`
}

func summarizeTiming(parts []executionEvidence, elapsed float64) *generationTiming {
	out := &generationTiming{Total: elapsed}
	var load, generation float64
	allGeneration := len(parts) > 0
	for index, x := range parts {
		if x.Timings == nil {
			allGeneration = false
			continue
		}
		if index == 0 {
			out.Resident = x.Timings.Reused
		}
		if x.Timings.Load != nil {
			load += *x.Timings.Load
			out.Load = &load
		}
		if x.Timings.Generate == nil {
			allGeneration = false
		} else {
			generation += *x.Timings.Generate
		}
	}
	if allGeneration {
		out.Generate = &generation
	}
	return out
}

var cudaIndex = regexp.MustCompile(`^cuda:[0-9]+$`)
var requiredSynthesisModules = map[string][]string{"gpt-sovits": {"t2s_model", "vits_model"}, "cosyvoice3": {"llm", "flow", "hift"}, "voxcpm2": {"tts"}, "indextts2": {"gpt", "semantic_model", "semantic_codec", "s2mel", "bigvgan"}, "qwen3-tts": {"tts", "tokenizer"}}

func (x executionEvidence) verifiedDevice(r recording) string {
	if x.Job != r.ID || x.Engine != r.Input.Engine || x.Mode != r.Input.Mode || x.Platform != "win32" || len(x.Waveform) != 1 || !cudaIndex.MatchString(x.Waveform[0]) || x.Torch == "" || x.CUDA == "" {
		return ""
	}
	dev := x.Waveform[0]
	modules := requiredSynthesisModules[x.Engine]
	if x.Engine == "gpt-sovits" && gptV5(r.Input.Variant) {
		if x.Model != r.Input.Variant {
			return ""
		}
		modules = []string{"t2s_model", "vits_model", "vocoder"}
	}
	if x.Engine == "cosyvoice3" && x.Mode == "voice-convert" {
		modules = []string{"flow", "hift"}
	}
	if len(modules) == 0 || x.Devices[dev].Name == "" {
		return ""
	}
	for _, m := range modules {
		d := x.Modules[m]
		if len(d) != 1 || d[0] != dev {
			return ""
		}
	}
	return dev + " · " + x.Devices[dev].Name
}
