package main

import (
	"encoding/json"
	"testing"
)

func TestVerifiedDevice(t *testing.T) {
	for engine, modules := range requiredSynthesisModules {
		t.Run(engine, func(t *testing.T) {
			r := recording{ID: "job1", Input: request{Engine: engine, Mode: "clone"}}
			setup := func() executionEvidence {
				var x executionEvidence
				if err := json.Unmarshal([]byte(`{"job":"job1","mode":"clone","platform":"win32","torch_version":"2.8.0","cuda_version":"12.8","waveform_devices":["cuda:0"],"cuda_devices":{"cuda:0":{"name":"Test GPU"}}}`), &x); err != nil {
					t.Fatal(err)
				}
				x.Engine = engine
				x.Modules = map[string][]string{}
				for _, name := range modules {
					x.Modules[name] = []string{"cuda:0"}
				}
				return x
			}
			if got := setup().verifiedDevice(r); got != "cuda:0 · Test GPU" {
				t.Fatalf("device %q", got)
			}
			bad := map[string]func(*executionEvidence){
				"unobserved waveform": func(x *executionEvidence) { x.Waveform = nil },
				"CPU waveform":        func(x *executionEvidence) { x.Waveform = []string{"cpu"} },
				"mixed output":        func(x *executionEvidence) { x.Waveform = []string{"cuda:0", "cpu"} },
				"other job":           func(x *executionEvidence) { x.Job = "job2" },
				"other mode":          func(x *executionEvidence) { x.Mode = "other" },
				"other engine":        func(x *executionEvidence) { x.Engine = "other" },
				"other platform":      func(x *executionEvidence) { x.Platform = "linux" },
				"unknown GPU":         func(x *executionEvidence) { x.Devices = nil },
				"no runtime":          func(x *executionEvidence) { x.CUDA = "" },
			}
			for key, mod := range bad {
				x := setup()
				mod(&x)
				if x.verifiedDevice(r) != "" {
					t.Errorf("credited %s", key)
				}
			}
			for _, name := range modules {
				for _, d := range [][]string{nil, {"cpu"}, {"cuda:1"}, {"cuda:0", "cpu"}} {
					x := setup()
					x.Modules[name] = d
					if x.verifiedDevice(r) != "" {
						t.Errorf("credited %s=%v", name, d)
					}
				}
			}
		})
	}
}
