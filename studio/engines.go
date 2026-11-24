package main

import "slices"

type synthesisMode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Reference   bool   `json:"reference"`
	Transcript  bool   `json:"transcript"`
	Instruction bool   `json:"instruction"`
	Languages   bool   `json:"languages"`
	Variant     string `json:"variant,omitempty"`
	Speaker     bool   `json:"speaker,omitempty"`
	Style       bool   `json:"style,omitempty"`
	Emotion     string `json:"emotion,omitempty"`
}

type engineCapabilities struct {
	Variants        []string        `json:"variants"`
	Modes           []synthesisMode `json:"modes,omitempty"`
	MinReference    float64         `json:"minReference,omitempty"`
	MaxReference    float64         `json:"maxReference,omitempty"`
	LanguageOptions []string        `json:"languageOptions,omitempty"`
	Speakers        []string        `json:"speakers,omitempty"`
	Speed           bool            `json:"speed"`
}

var engineCatalog = map[string]engineCapabilities{
	"gpt-sovits": {
		Variants: []string{"v2Pro"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Reference: true, Transcript: true, Languages: true},
		},
		MinReference: 3, MaxReference: 10,
		LanguageOptions: []string{"yue", "zh", "en", "ja", "ko", "auto"}, Speed: true,
	},
	"cosyvoice3": {
		Variants: []string{"Fun-CosyVoice3-0.5B"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Reference: true, Transcript: true},
			{ID: "instruct", Label: "指令朗讀", Reference: true, Instruction: true},
			{ID: "cross", Label: "跨語言朗讀", Reference: true},
		},
		MaxReference: 30, Speed: true,
	},
	"voxcpm2": {
		Variants: []string{"VoxCPM2"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Reference: true, Style: true},
			{ID: "continue", Label: "錄音延續", Reference: true, Transcript: true},
			{ID: "design", Label: "聲線設計", Instruction: true},
		},
	},
	"indextts2": {
		Variants: []string{"IndexTTS-2"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "沿用原音色與情緒", Reference: true},
			{ID: "emotion-audio", Label: "參考情緒錄音", Reference: true, Emotion: "audio"},
			{ID: "emotion-text", Label: "文字控制情緒", Reference: true, Instruction: true, Emotion: "text"},
			{ID: "emotion-vector", Label: "調整情緒比例", Reference: true, Emotion: "vector"},
		},
		MaxReference: 15,
	},
	"qwen3-tts": {
		Variants:        []string{"Base", "CustomVoice", "VoiceDesign"},
		LanguageOptions: []string{"auto", "zh", "en", "ja", "ko", "de", "fr", "ru", "pt", "es", "it"},
		Speakers:        []string{"Vivian", "Serena", "Uncle_Fu", "Dylan", "Eric", "Ryan", "Aiden", "Ono_Anna", "Sohee"},
		Modes: []synthesisMode{
			{ID: "clone", Label: "錄音複製", Variant: "Base", Reference: true, Transcript: true, Languages: true},
			{ID: "simple-clone", Label: "只參考音色", Variant: "Base", Reference: true, Languages: true},
			{ID: "preset", Label: "預設聲線", Variant: "CustomVoice", Speaker: true, Style: true, Languages: true},
			{ID: "design", Label: "聲線設計", Variant: "VoiceDesign", Instruction: true, Languages: true},
		},
	},
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

func (engine engineCapabilities) allowsLanguage(language string) bool {
	return slices.Contains(engine.LanguageOptions, language)
}
