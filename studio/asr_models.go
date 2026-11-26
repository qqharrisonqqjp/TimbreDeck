package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

type asrOption struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	Status string `json:"status"`
}

var asrOptions = []asrOption{
	{"", "自動（推薦）", "粵語及普通話優先 SenseVoice；其餘用 Faster Whisper Small。", "builtin"},
	{"faster-whisper-small", "Faster Whisper Small", "多語、CPU INT8；本機後備。選粵語時會標明改用中文模式。", "builtin"},
	{"sensevoice-onnx", "SenseVoice-Small", "量化檔約 239 MB；支援粵、普、英、日、韓。此介面只輸出文字。", "builtin"},
	{"faster-whisper-large-v3", "Faster Whisper Large v3", "完整 Whisper v3 解碼器；耗用較多記憶體，同一素材可在擂台比較。", "optional"},
	{"faster-whisper-large-v3-turbo", "Faster Whisper Large v3 Turbo", "較少解碼層；取捨為轉錄速度與部分語言品質，不作翻譯。", "optional"},
	{"qwen3-asr-0.6b", "Qwen3-ASR 0.6B", "較輕的多語及中文方言版本；用 CPU 逐段辨識以免搶佔 TTS 顯存。", "optional"},
	{"qwen3-asr-1.7b", "Qwen3-ASR 1.7B", "同系列較大型權重；CPU 較耗資源，可用擂臺試同一段錄音。", "optional"},
	{"fun-asr-nano", "Fun-ASR-Nano (800M)", "主攻中、英、日；中文包含粵語等方言。此處用 Sherpa ONNX INT8 轉換。", "optional"},
	{"fun-asr-mlt-nano", "Fun-ASR-MLT-Nano (800M)", "多語，包括多種東南亞語言。此處用社群 ONNX INT8 轉換。", "optional"},
	{"hon9kon9ize", "hon9kon9ize 粵語 Whisper", "未確認可部署的模型來源／格式，暫不可選。", "planned"},
}

func asrModelAvailable(root, id string) bool {
	var files []string
	switch id {
	case "":
		return true
	case "sensevoice-onnx":
		files = []string{"model.int8.onnx", "tokens.txt"}
	case "faster-whisper-small":
		files = []string{"model.bin", "config.json", "tokenizer.json"}
	case "faster-whisper-large-v3", "faster-whisper-large-v3-turbo":
		files = []string{"model.bin", "config.json", "tokenizer.json", "timbredeck-asr.json"}
	case "qwen3-asr-0.6b", "qwen3-asr-1.7b":
		if !installedFile(filepath.Join(root, "qwen-asr-python", "qwen_asr", "__init__.py")) {
			return false
		}
		files = []string{"config.json", "timbredeck-asr.json"}
	case "fun-asr-nano", "fun-asr-mlt-nano":
		files = []string{"encoder_adaptor.int8.onnx", "llm.int8.onnx", "embedding.int8.onnx", "Qwen3-0.6B/tokenizer.json", "timbredeck-asr.json"}
	default:
		return false
	}
	for _, f := range files {
		if !installedFile(filepath.Join(root, id, f)) {
			return false
		}
	}
	return true
}
func (s *studio) qwenASRRuntime() string {
	s.files.Lock()
	paths, err := s.paths()
	s.files.Unlock()
	if err != nil {
		return ""
	}
	chosen := paths["qwen3-tts"]
	if chosen != "" && !filepath.IsAbs(chosen) && !installedPackName.MatchString(chosen) {
		return ""
	}
	engine := s.runtimePath("qwen3-tts", chosen)
	if installedFile(filepath.Join(engine, "runtime", "python.exe")) {
		return engine
	}
	return ""
}
func (s *studio) asrModels(w http.ResponseWriter, r *http.Request) {
	_, root, e := s.asrRuntime()
	out := append([]asrOption(nil), asrOptions...)
	for i, v := range out {
		if v.Status != "planned" {
			if e == nil && asrModelAvailable(root, v.ID) && (!strings.HasPrefix(v.ID, "qwen3-asr-") || s.qwenASRRuntime() != "") {
				out[i].Status = "ready"
			} else {
				out[i].Status = "missing"
			}
		}
	}
	send(w, 200, out)
}
