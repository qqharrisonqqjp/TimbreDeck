package main

type synthesisMode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Reference   bool   `json:"reference"`
	Transcript  bool   `json:"transcript"`
	Instruction bool   `json:"instruction"`
	Languages   bool   `json:"languages"`
}

type engineCapabilities struct {
	Variants     []string        `json:"variants"`
	Modes        []synthesisMode `json:"modes,omitempty"`
	MinReference float64         `json:"minReference,omitempty"`
	MaxReference float64         `json:"maxReference,omitempty"`
}

var engineCatalog = map[string]engineCapabilities{
	"gpt-sovits": {
		Variants: []string{"v2Pro"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Reference: true, Transcript: true, Languages: true},
		},
		MinReference: 3, MaxReference: 10,
	},
	"cosyvoice3": {
		Variants: []string{"Fun-CosyVoice3-0.5B"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Reference: true, Transcript: true},
			{ID: "instruct", Label: "指令朗讀", Reference: true, Instruction: true},
			{ID: "cross", Label: "跨語言朗讀", Reference: true},
		},
		MaxReference: 30,
	},
	"voxcpm2":   {Variants: []string{"VoxCPM2"}},
	"indextts2": {Variants: []string{"IndexTTS-2"}},
	"qwen3-tts": {Variants: []string{"Base", "CustomVoice", "VoiceDesign"}},
}

func supported(id string) bool { return len(engineCatalog[id].Modes) > 0 }

func known(id string) bool {
	_, found := engineCatalog[id]
	return found
}

func (engine engineCapabilities) mode(id string) (synthesisMode, bool) {
	for _, mode := range engine.Modes {
		if mode.ID == id {
			return mode, true
		}
	}
	return synthesisMode{}, false
}
