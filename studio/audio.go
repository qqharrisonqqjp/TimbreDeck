package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

func wavDuration(path string) (float64, error) {
	seconds, _, err := wavInfo(path)
	return seconds, err
}

type waveData struct {
	rate, block, size      uint32
	format, channels, bits uint16
	offset                 int64
}

func wavInfo(path string) (float64, uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	wav, err := parseWAV(f)
	if err != nil {
		return 0, 0, err
	}
	return float64(wav.size/wav.block) / float64(wav.rate), wav.rate, nil
}

func parseWAV(f *os.File) (waveData, error) {
	bad := errors.New("請選用完整的 PCM 或浮點 WAV 音檔。")
	st, e := f.Stat()
	if e != nil {
		return waveData{}, e
	}
	var h [12]byte
	if _, e := io.ReadFull(f, h[:]); e != nil || string(h[:4]) != "RIFF" || string(h[8:]) != "WAVE" {
		return waveData{}, bad
	}
	end := int64(binary.LittleEndian.Uint32(h[4:8])) + 8
	if end > st.Size() {
		return waveData{}, bad
	}
	var wav waveData
	for pos := int64(12); pos+8 <= end; {
		var c [8]byte
		if _, e = f.ReadAt(c[:], pos); e != nil {
			return waveData{}, bad
		}
		n := binary.LittleEndian.Uint32(c[4:])
		pos += 8
		if int64(n) > end-pos {
			return waveData{}, bad
		}
		switch string(c[:4]) {
		case "fmt ":
			var b [16]byte
			if n < 16 {
				return waveData{}, bad
			}
			if _, e = f.ReadAt(b[:], pos); e != nil {
				return waveData{}, bad
			}
			fmtID := binary.LittleEndian.Uint16(b[:])
			channels := uint32(binary.LittleEndian.Uint16(b[2:]))
			bits := uint32(binary.LittleEndian.Uint16(b[14:]))
			if (fmtID != 1 && fmtID != 3) || channels == 0 || channels > 2 || bits == 0 || bits%8 != 0 || bits > 64 {
				return waveData{}, bad
			}
			wav.rate = binary.LittleEndian.Uint32(b[4:])
			wav.block = uint32(binary.LittleEndian.Uint16(b[12:]))
			if wav.rate < 8000 || wav.rate > 384000 || wav.block != channels*bits/8 {
				return waveData{}, bad
			}
			wav.format = fmtID
			wav.channels = uint16(channels)
			wav.bits = uint16(bits)
		case "data":
			wav.size = n
			wav.offset = pos
		}
		pos += int64(n) + int64(n%2)
	}
	if wav.rate == 0 || wav.block == 0 || wav.size < wav.block || wav.size%wav.block != 0 {
		return waveData{}, bad
	}
	return wav, nil
}

func (s *studio) reference(w http.ResponseWriter, r *http.Request) {
	f, e := os.CreateTemp(s.path("voices"), ".import-*")
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	_, e = io.Copy(io.MultiWriter(f, h), http.MaxBytesReader(w, r.Body, 32<<20))
	closeErr := f.Close()
	if e != nil {
		fail(w, 400, "未能讀取錄音，單檔上限為 32 MiB。")
		return
	}
	if closeErr != nil {
		fail(w, 500, closeErr.Error())
		return
	}
	source := f.Name()
	key := hex.EncodeToString(h.Sum(nil))
	original := key
	secs, e := wavDuration(source)
	if e != nil {
		pending, err := os.CreateTemp(s.path("voices"), ".converted-*")
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		source = pending.Name()
		pending.Close()
		defer os.Remove(source)
		secs, key, e = transcodeReference(r.Context(), f.Name(), source)
		if e != nil {
			fail(w, 422, e.Error())
			return
		}
	}
	path := s.path("voices", key+".wav")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if e = os.Rename(source, path); e != nil {
			if installedFile(path) {
				send(w, 201, map[string]any{"id": key, "seconds": secs, "sourceSha256": original})
				return
			}
			fail(w, 500, e.Error())
			return
		}
	}
	send(w, 201, map[string]any{"id": key, "seconds": secs, "sourceSha256": original})
}
func (s *studio) referenceAudio(w http.ResponseWriter, r *http.Request) {
	k := r.PathValue("id")
	if !hexID(k, 64) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeFile(w, r, s.path("voices", k+".wav"))
}

type request struct {
	Engine           string `json:"engine"`
	Mode             string `json:"mode"`
	Text             string `json:"text"`
	Transcript       string `json:"transcript"`
	Instruction      string `json:"instruction"`
	Reference        string `json:"reference"`
	SourceSpeech     string `json:"sourceSpeech,omitempty"`
	STSStreaming     bool   `json:"stsStreaming,omitempty"`
	ttsResident      *ttsTarget
	VoiceID          string    `json:"voiceId,omitempty"`
	Variant          string    `json:"variant,omitempty"`
	ModelProject     string    `json:"modelProject,omitempty"`
	UseBaseModel     bool      `json:"useBaseModel,omitempty"`
	Speaker          string    `json:"speaker,omitempty"`
	EmotionReference string    `json:"emotionReference,omitempty"`
	EmotionStrength  *float64  `json:"emotionStrength,omitempty"`
	EmotionWeights   []float64 `json:"emotionWeights,omitempty"`
	Language         string    `json:"language"`
	RefLang          string    `json:"refLang"`
	Speed            float64   `json:"speed"`
	Preview          bool      `json:"preview"`
	SplitAt          int       `json:"splitAt,omitempty"`
	SplitGapMS       int       `json:"splitGapMs,omitempty"`
	SplitSeparate    bool      `json:"splitSeparate,omitempty"`
}
type recording struct {
	ID             string            `json:"id"`
	Created        time.Time         `json:"created"`
	Seconds        float64           `json:"seconds"`
	Input          request           `json:"input"`
	VoiceName      string            `json:"voiceName,omitempty"`
	SampleRate     uint32            `json:"sampleRate,omitempty"`
	Model          string            `json:"model,omitempty"`
	FineTuneName   string            `json:"fineTuneName,omitempty"`
	PackVersion    string            `json:"packVersion,omitempty"`
	RuntimeVersion string            `json:"runtimeVersion,omitempty"`
	AudioAvailable bool              `json:"audioAvailable"`
	VerifiedCUDA   string            `json:"verifiedCuda,omitempty"`
	Parts          int               `json:"parts,omitempty"`
	PartsAvailable bool              `json:"partsAvailable,omitempty"`
	Timing         *generationTiming `json:"timing,omitempty"`
}

func firstSentence(s string) string {
	for i, c := range s {
		if strings.ContainsRune("。！？!?\n", c) || (c == '.' && (i+1 == len(s) || s[i+1] == ' ')) {
			return strings.TrimSpace(s[:i+len(string(c))])
		}
	}
	return s
}
func language(s string) bool {
	return s == "yue" || s == "zh" || s == "en" || s == "ja" || s == "ko" || s == "auto"
}
func (s *studio) validate(q *request) error {
	if q.SourceSpeech != "" {
		return errors.New("請使用 STS 工作區轉換來源錄音。")
	}
	q.Text = strings.TrimSpace(q.Text)
	q.Transcript = strings.TrimSpace(q.Transcript)
	q.Instruction = strings.TrimSpace(q.Instruction)
	capabilities := engineCatalog[q.Engine]
	mode, ok := capabilities.mode(q.Mode)
	if !ok {
		return errors.New("請選擇已接通的引擎及工作模式。")
	}
	if q.Text == "" || len([]rune(q.Text)) > 10000 {
		return errors.New("請輸入最多 10,000 字的台詞。")
	}
	if q.SplitAt != 0 && (q.SplitAt < 20 || q.SplitAt > 2000) || q.SplitGapMS < 0 || q.SplitGapMS > 5000 {
		return errors.New("切分長度須為 20–2000 字；片段間隔須為 0–5000 毫秒。")
	}
	if q.SplitSeparate && (q.SplitAt == 0 || q.Preview) {
		return errors.New("只可為全文切分輸出獨立片段。")
	}
	if q.SplitAt > 0 && !q.Preview && len(splitSynthesis(q.Text, q.SplitAt)) > 100 {
		return errors.New("一次最多可切成 100 段，請提高切分字數或縮短台詞。")
	}
	if !capabilities.Speed {
		if q.Speed != 0 && q.Speed != 1 {
			return errors.New("這款引擎使用原生語速。")
		}
		q.Speed = 1
	}
	if q.Speed < 0.5 || q.Speed > 2 || math.IsNaN(q.Speed) {
		return errors.New("語速範圍為 0.5 至 2 倍。")
	}
	if mode.Reference {
		if !hexID(q.Reference, 64) {
			return errors.New("請匯入參考錄音。")
		}
		seconds, err := wavDuration(s.path("voices", q.Reference+".wav"))
		if err != nil {
			return errors.New("找不到有效的參考錄音，請重新匯入。")
		}
		if capabilities.MinReference > 0 && (seconds < capabilities.MinReference || seconds > capabilities.MaxReference) {
			return fmt.Errorf("請匯入 %g 至 %g 秒的參考錄音。", capabilities.MinReference, capabilities.MaxReference)
		}
		if capabilities.MaxReference > 0 && seconds > capabilities.MaxReference {
			return fmt.Errorf("參考錄音最長為 %g 秒。", capabilities.MaxReference)
		}
	}
	if !mode.Reference {
		q.Reference, q.VoiceID = "", ""
	}
	if mode.Languages && !capabilities.allowsLanguage(q.Language) {
		return errors.New("語言無效。")
	}
	if q.Engine == "gpt-sovits" && !language(q.RefLang) {
		return errors.New("錄音語言無效。")
	}
	if mode.Instruction && q.Instruction == "" {
		return errors.New("請填寫講法指示。")
	}
	if mode.Transcript && q.Transcript == "" {
		return errors.New("請填寫參考錄音的逐字稿。")
	}
	if mode.Speaker && !slices.Contains(capabilities.Speakers, q.Speaker) {
		return errors.New("請選擇預設聲線。")
	}
	if mode.Variant != "" {
		if q.Variant != "" && mode.Variant != q.Variant {
			return errors.New("所選模式與模型不相容。")
		}
		q.Variant = mode.Variant
	} else if !slices.Contains(capabilities.Variants, q.Variant) {
		if q.Variant != "" {
			return errors.New("模型無效。")
		}
		q.Variant = capabilities.Variants[0]
	}
	switch mode.Emotion {
	case "audio":
		if !hexID(q.EmotionReference, 64) {
			return errors.New("請匯入情緒參考錄音。")
		}
		if seconds, err := wavDuration(s.path("voices", q.EmotionReference+".wav")); err != nil || seconds > 15 {
			return errors.New("請匯入最多 15 秒的有效情緒參考錄音。")
		}
	case "vector":
		if len(q.EmotionWeights) != 8 {
			return errors.New("請設定八項情緒比例。")
		}
		for _, value := range q.EmotionWeights {
			if value < 0 || value > 1 || math.IsNaN(value) {
				return errors.New("情緒比例必須介乎 0 至 1。")
			}
		}
	}
	if mode.Emotion != "" && q.EmotionStrength != nil {
		value := *q.EmotionStrength
		if value < 0 || value > 1 || math.IsNaN(value) {
			return errors.New("情緒強度必須介乎 0 至 1。")
		}
	}
	if q.Preview {
		q.Text = firstSentence(q.Text)
	}
	return nil
}
func (s *studio) synthesize(w http.ResponseWriter, r *http.Request) {
	var q request
	if !decode(w, r, &q) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "已有作業進行中。")
		return
	}
	var voice string
	mode, _ := engineCatalog[q.Engine].mode(q.Mode)
	if mode.Reference {
		var err error
		voice, err = s.resolveProfile(&q)
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
	}
	if e := s.validate(&q); e != nil {
		fail(w, 422, e.Error())
		return
	}
	var trained *trainedModel
	if q.ModelProject != "" {
		if q.Engine != "gpt-sovits" {
			fail(w, 422, "微調模型只適用於 GPT-SoVITS。")
			return
		}
		if q.Variant != "v2Pro" {
			fail(w, 422, "此套微調模型需要 GPT-SoVITS v2Pro。")
			return
		}
		model, err := s.loadTrainedModel(q.ModelProject)
		if err != nil {
			fail(w, 422, "找不到可用的 v2Pro 微調模型，請重新偵測。")
			return
		}
		trained = &model
	}
	s.files.Lock()
	paths, e := s.paths()
	v := s.inspectSelected(q.Engine, paths)
	s.files.Unlock()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	if !v.Ready {
		fail(w, 409, "請先在模型管理連接完整引擎。")
		return
	}
	root := v.variantPaths[q.Variant]
	if root == "" {
		fail(w, 409, "請先在模型管理連接 "+q.Variant+" 模型。")
		return
	}
	if p := s.residentLocked(); p != nil && p.pinned {
		t, err := newTTSTarget(q, root, v.modelPaths[q.Variant], trained)
		if err != nil || p.tts == nil || p.key != t.token {
			fail(w, 409, "已有其他常駐模型，請先停止或確認切換。")
			return
		}
		q.ttsResident = t
	} else if p != nil {
		s.stopSTSWorkerLocked()
	}
	ctx, j, _ := s.reserveLocked(q.Engine, "synthesis")
	go func() { s.finish(s.render(ctx, j.ID, root, v.modelPaths[q.Variant], q, voice, trained)) }()
	send(w, 202, j)
}
func (s *studio) render(ctx context.Context, key, root, modelRoot string, q request, voice string, trained *trainedModel) error {
	return s.renderWithStream(ctx, key, root, modelRoot, q, voice, trained, nil)
}
func (s *studio) renderWithStream(ctx context.Context, key, root, modelRoot string, q request, voice string, trained *trainedModel, stream *stsPreview) error {
	started := time.Now()
	dir, e := os.MkdirTemp(s.path("outputs"), ".render-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "speech.wav")
	splitRoot := ""
	if modelRoot != root {
		splitRoot = modelRoot
	}
	ref, emotionRef := "", ""
	if q.Reference != "" {
		ref = s.path("voices", q.Reference+".wav")
	}
	if hexID(q.EmotionReference, 64) {
		emotionRef = s.path("voices", q.EmotionReference+".wav")
	}
	src, _ := assets.ReadFile("worker.py")
	logFile, e := os.Create(s.path("logs", key+".log"))
	if e != nil {
		return e
	}
	defer logFile.Close()
	parts := []string{q.Text}
	if q.SplitAt > 0 && !q.Preview {
		parts = splitSynthesis(q.Text, q.SplitAt)
	}
	// Use the same Python process and loaded model for all segments of an
	// ordinary job. Explicitly pinned models still survive the job.
	if stream == nil && len(parts) > 1 && q.ttsResident == nil {
		target, err := newTTSTarget(q, root, modelRoot, trained)
		if err != nil {
			return err
		}
		q.ttsResident = target
		defer func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if p := s.stsWorker; p != nil && !p.pinned && p.key == target.token {
				s.stopSTSWorkerLocked()
				fmt.Fprintln(logFile, "Split worker released after the job:", p.log)
			}
		}()
	}
	diag := filepath.Join(dir, "execution.json")
	var outcome []executionEvidence
	var waves []string
	for i, part := range parts {
		if e = ctx.Err(); e != nil {
			return e
		}
		target, pdiag := dst, diag
		if len(parts) > 1 || q.SplitSeparate {
			target = filepath.Join(dir, fmt.Sprintf("part-%03d.wav", i+1))
			pdiag = filepath.Join(dir, fmt.Sprintf("part-%03d.json", i+1))
		}
		if len(parts) > 1 {
			s.updateSynthesisProgress(key, i, len(parts))
		}
		input := q
		input.Text = part
		payload, e := json.Marshal(map[string]any{
			"id": key, "request": input, "ref": ref, "emotionRef": emotionRef, "output": target,
			"trained":     trained,
			"modelRoot":   splitRoot,
			"diagnostics": pdiag,
			"sourceSpeech": func() string {
				if q.SourceSpeech == "" {
					return ""
				}
				return s.path("voices", q.SourceSpeech+".wav")
			}(),
		})
		if e != nil {
			return e
		}
		py := filepath.Join(root, "runtime", "python.exe")
		cmd := exec.CommandContext(ctx, py, "-c", string(src))
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(string(payload))
		cmd.Stdout = logFile
		if stream != nil {
			cmd.Stdout = stream
		}
		cmd.Stderr = logFile
		cmd.Env = synthesisEnv(cmd, s.path("cache"), root)
		if stream != nil && q.Engine != defaultSTS {
			e = s.runExternalSTS(ctx, root, q.Engine, payload, stream, logFile)
		} else if stream != nil {
			e = s.runSTSWorker(ctx, root, modelRoot, src, cmd.Env, payload, stream, logFile)
		} else if q.ttsResident != nil {
			e = s.runTTSProcess(ctx, q.ttsResident, payload, key, logFile)
		} else {
			e = cmd.Run()
		}
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("第 %d/%d 段未能完成合成。請查看 logs/%s.log（%v）。", i+1, len(parts), key, e)
		}
		var x executionEvidence
		_ = readJSON(pdiag, &x)
		outcome = append(outcome, x)
		waves = append(waves, target)
	}
	var secs float64
	var rate uint32
	if len(parts) > 1 || q.SplitSeparate {
		if q.SplitSeparate {
			secs, rate, e = packSpeechParts(ctx, filepath.Join(dir, "parts.zip"), waves)
		} else {
			e = joinSpeech(ctx, dst, waves, q.SplitGapMS)
		}
		if e != nil {
			return e
		}
		for i, path := range waves {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("part-%03d.json", i+1)))
		}
		if e = writeJSON(filepath.Join(dir, "segments.json"), map[string]any{"parts": parts, "gapMs": q.SplitGapMS, "execution": outcome}); e != nil {
			return e
		}
	}
	if !q.SplitSeparate {
		secs, rate, e = wavInfo(dst)
		if e != nil {
			return errors.New("引擎沒有輸出有效 WAV。")
		}
	}
	diagnostics := outcome[0]
	record := recording{ID: key, Created: time.Now().UTC(), Seconds: secs, Input: q, VoiceName: voice,
		SampleRate: rate, Model: diagnostics.Model, AudioAvailable: !q.SplitSeparate,
		Timing: summarizeTiming(outcome, time.Since(started).Seconds())}
	if trained != nil {
		record.FineTuneName = trained.Name
		if record.FineTuneName == "" {
			project, _ := s.loadTraining(q.ModelProject)
			record.FineTuneName = project.Name
		}
	}
	if q.SplitSeparate {
		record.Parts = len(parts)
	}
	record.VerifiedCUDA = diagnostics.verifiedDevice(record)
	for _, x := range outcome[1:] {
		if x.verifiedDevice(record) != record.VerifiedCUDA {
			record.VerifiedCUDA = ""
			break
		}
	}
	var pack manifest
	if readJSON(filepath.Join(modelRoot, packMetadata), &pack) == nil && pack.check(q.Engine) == nil {
		record.PackVersion = pack.Version
	} else if model, owned := s.ownedUpstream(modelRoot, q.Engine, q.Variant, filepath.Base(modelRoot)); owned {
		record.PackVersion = model.Version
	}
	if splitRoot != "" {
		var runtimePack manifest
		if readJSON(filepath.Join(root, packMetadata), &runtimePack) == nil && runtimePack.check(q.Engine) == nil {
			record.RuntimeVersion = runtimePack.Version
		}
	}
	if e = writeJSON(filepath.Join(dir, "record.json"), record); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return os.Rename(dir, s.path("outputs", key))
}
func (s *studio) history(w http.ResponseWriter, r *http.Request) {
	dirs, e := os.ReadDir(s.path("outputs"))
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	items := []recording{}
	for _, d := range dirs {
		if !d.IsDir() || !hexID(d.Name(), 32) {
			continue
		}
		if row, ok := s.outputRecord(d.Name()); ok {
			items = append(items, row)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Created.After(items[j].Created) })
	send(w, 200, items)
}
func (s *studio) outputRecord(key string) (recording, bool) {
	var row recording
	if !hexID(key, 32) || readJSON(s.path("outputs", key, "record.json"), &row) != nil || row.ID != key {
		return row, false
	}
	if row.VerifiedCUDA == "" {
		var result executionEvidence
		if readJSON(s.path("outputs", key, "execution.json"), &result) == nil {
			row.VerifiedCUDA = result.verifiedDevice(row)
		}
	}
	row.AudioAvailable = installedFile(s.path("outputs", key, "speech.wav"))
	row.PartsAvailable = installedFile(s.path("outputs", key, "parts.zip"))
	return row, true
}
func (s *studio) historyItem(w http.ResponseWriter, r *http.Request) {
	if row, ok := s.outputRecord(r.PathValue("id")); ok {
		send(w, 200, row)
		return
	}
	fail(w, 404, "找不到輸出紀錄。")
}
func (s *studio) audio(w http.ResponseWriter, r *http.Request) {
	k := r.PathValue("id")
	if !hexID(k, 32) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=TimbreDeck-"+k+".wav")
	}
	http.ServeFile(w, r, s.path("outputs", k, "speech.wav"))
}
