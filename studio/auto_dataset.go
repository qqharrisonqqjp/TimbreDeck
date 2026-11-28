package main

import (
	"archive/zip"
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
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	autoDatasetDir        = "auto-datasets"
	autoDatasetWorkPrefix = ".auto-dataset-"
	autoDatasetMaxClips   = 256
	autoDatasetMaxZip     = 32 << 20
	autoDatasetRate       = cleanSampleRate
)

type autoDatasetRequest struct {
	References       []string         `json:"references"`
	Language         string           `json:"language"`
	Speaker          string           `json:"speaker"`
	CleanMode        string           `json:"cleanMode,omitempty"`
	Denoise          bool             `json:"denoise"`
	ASRModel         string           `json:"asrModel,omitempty"`
	Split            *bool            `json:"split,omitempty"`
	Transcribe       *bool            `json:"transcribe,omitempty"`
	SkipUnrecognized *bool            `json:"skipUnrecognized,omitempty"`
	SourceNames      []string         `json:"sourceNames,omitempty"`
	Training         *trainingOptions `json:"training,omitempty"`
	ReleaseResident  string           `json:"releaseResident,omitempty"`
}

func trueByDefault(value *bool) bool { return value == nil || *value }

type autoDatasetRecord struct {
	ID        string    `json:"id"`
	Speaker   string    `json:"speaker"`
	SpeakerID string    `json:"speakerId"`
	Language  string    `json:"language"`
	Count     int       `json:"count"`
	Skipped   int       `json:"skipped"`
	Seconds   float64   `json:"seconds"`
	Created   time.Time `json:"created"`
	Denoise   bool      `json:"denoise"`
	Provider  string    `json:"provider"`
	Pending   int       `json:"pending,omitempty"`
}

type autoDatasetASR func(context.Context, []string, string) ([]asrResult, error)

func (s *studio) startAutoDataset(w http.ResponseWriter, r *http.Request) {
	s.startAutoDatasetWith(w, r, nil, runCleanWorker)
}

func (s *studio) startAutoDatasetWith(w http.ResponseWriter, r *http.Request, recognize autoDatasetASR, clean cleanRunner) {
	var q autoDatasetRequest
	if !decode(w, r, &q) {
		return
	}
	if !language(q.Language) || q.Language == "auto" {
		fail(w, 422, "請指定訓練資料的語言。")
		return
	}
	if q.CleanMode != "" {
		if q.CleanMode != "denoise" && !enhancedClean(q.CleanMode) {
			fail(w, 422, "訓練素材清理模式無效。")
			return
		}
		q.Denoise = true
	} else if q.Denoise {
		q.CleanMode = "denoise"
	}
	q.Speaker = strings.TrimSpace(q.Speaker)
	if recognize == nil {
		if trueByDefault(q.Transcribe) && q.ASRModel != "" {
			_, root, err := s.asrRuntime()
			if err != nil || !asrModelAvailable(root, q.ASRModel) {
				fail(w, 422, "所選 ASR 模型未安裝或仍未整合。")
				return
			}
		}
		recognize = func(ctx context.Context, paths []string, lang string) ([]asrResult, error) {
			return s.recognizeWithModel(ctx, paths, lang, q.ASRModel)
		}
	}
	if q.Speaker == "" {
		q.Speaker = "speaker-1"
	}
	if utf8.RuneCountInString(q.Speaker) > 64 || strings.IndexFunc(q.Speaker, unicode.IsControl) != -1 ||
		strings.ContainsRune(q.Speaker, '|') {
		fail(w, 422, "說話者名稱最多 64 字，且不可包含控制字元或直線符號。")
		return
	}
	if len(q.SourceNames) != 0 && len(q.SourceNames) != len(q.References) {
		fail(w, 422, "錄音檔名數目不符。")
		return
	}
	var trainer trainingRuntime
	if q.Training != nil {
		if q.Training.Name == "" {
			q.Training.Name = q.Speaker
		}
		if err := q.Training.validate(); err != nil {
			fail(w, 422, err.Error())
			return
		}
		var err error
		trainer, err = s.getTrainingRuntime()
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		if err := s.trainingSpace(); err != nil {
			fail(w, 507, err.Error())
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy() {
		fail(w, 409, "已有作業進行中。")
		return
	}
	inputs, frames, err := s.cleanInputs(cleanRequest{q.References, "denoise", 0})
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	var task cleanTask
	const engine = "gpt-sovits"
	if q.Denoise {
		s.files.Lock()
		paths, err := s.paths()
		s.files.Unlock()
		if err != nil {
			fail(w, 500, "未能讀取引擎設定。")
			return
		}
		selection := paths[engine]
		if selection != "" && !filepath.IsAbs(selection) && !installedPackName.MatchString(selection) {
			fail(w, 409, "GPT-SoVITS 執行環境設定無效。")
			return
		}
		root := s.runtimePath(engine, selection)
		modelDir, err := cleanAssetsPath()
		if err != nil {
			fail(w, 500, "未能找到去噪模型資料夾。")
			return
		}
		root, err = prepareCleanRuntime(q.CleanMode, root, modelDir)
		if err != nil {
			fail(w, 409, err.Error())
			return
		}
		task = cleanTask{Mode: q.CleanMode, Inputs: inputs, EngineRoot: root, Assets: modelDir}
	}
	dir := s.path(autoDatasetDir)
	if err := s.ensureManagedDirectory(dir); err != nil {
		fail(w, 500, "未能建立訓練集輸出資料夾。")
		return
	}
	if free, err := freeSpace(dir); err != nil || free < cleanDiskBytes {
		fail(w, 507, "訓練集工作區需要至少 256 MiB 可用空間。")
		return
	}
	kind := "dataset"
	if q.Training != nil {
		kind = "auto-training"
		if q.ReleaseResident == "" {
			q.ReleaseResident = q.Training.ReleaseResident
		}
	}
	if q.Training != nil && !s.releaseForTraining(w, q.ReleaseResident) {
		return
	}
	if q.Denoise && q.Training == nil {
		task.Device = s.chooseCleaningDevice()
	}
	ctx, j, _ := s.reserveLocked(engine, kind)
	go s.buildAutoDataset(ctx, j.ID, q, inputs, frames, task, recognize, clean, trainer)
	send(w, 202, j)
}

type autoDatasetSpan struct{ start, end int }

func autoResampledFrames(w waveData) int {
	rate := uint64(w.rate)
	return int((2*uint64(w.size/w.block)*autoDatasetRate + rate) / (2 * rate))
}

func (s *studio) buildAutoDataset(parent context.Context, key string, q autoDatasetRequest, inputs []string, frames uint64, task cleanTask, recognize autoDatasetASR, clean cleanRunner, trainer trainingRuntime) {
	ctx := parent
	ctx, cancel := context.WithTimeout(ctx, 50*time.Minute)
	defer cancel()
	base := s.path(autoDatasetDir)
	s.pruneAutoDatasetStaging()
	work, err := os.MkdirTemp(base, autoDatasetWorkPrefix)
	if err != nil {
		s.finish(err)
		return
	}
	defer func() {
		_ = os.RemoveAll(work)
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("訓練集作業超時。")
		}
		s.finish(err)
	}()
	result := filepath.Join(work, "result")
	if err = os.Mkdir(result, 0700); err != nil {
		return
	}

	s.detail("正在準備訓練音訊…")
	if q.Denoise {
		s.detail("正在去除背景噪音…")
		task.Output = filepath.Join(work, "denoised.wav")
		if err = clean(ctx, task, work, s.path("logs", key+".log")); err != nil {
			return
		}
	}
	full, sourceLengths, err := loadAutoDatasetAudio(ctx, inputs, frames, task.Output)
	if err != nil {
		return
	}
	s.detail("正在切分語音片段…")
	var spans []autoDatasetSpan
	var sources []datasetSource
	var offset int
	for i, n := range sourceLengths {
		source := datasetSource{Group: id(), Name: fmt.Sprintf("錄音 %d", i+1)}
		if i < len(q.SourceNames) && validTrainingName(q.SourceNames[i], 255) {
			source.Name = q.SourceNames[i]
		}
		chunks := []autoDatasetSpan{{0, n}}
		if trueByDefault(q.Split) {
			chunks = splitAutoDatasetSpeech(full[offset : offset+n])
		}
		for _, span := range chunks {
			spans = append(spans, autoDatasetSpan{offset + span.start, offset + span.end})
			row := source
			row.StartMS = int64(span.start) * 1000 / autoDatasetRate
			sources = append(sources, row)
		}
		offset += n
	}
	if len(spans) == 0 {
		err = errors.New("錄音未偵測到可辨識的語音片段。")
		return
	}
	if len(spans) > autoDatasetMaxClips {
		err = errors.New("偵測的片段過多，請減少錄音後再試。")
		return
	}

	cache := s.path("cache")
	if !s.managedPath(cache) {
		err = errors.New("語音辨識暫存資料夾無效。")
		return
	}
	wavDir, err := os.MkdirTemp(cache, autoDatasetWorkPrefix)
	if err != nil {
		return
	}
	defer os.RemoveAll(wavDir)
	paths := make([]string, len(spans))
	for i, span := range spans {
		paths[i] = filepath.Join(wavDir, id()+".wav")
		if err = saveAutoWAV(ctx, paths[i], full[span.start:span.end]); err != nil {
			return
		}
	}
	tokens := make([]asrResult, len(paths))
	if trueByDefault(q.Transcribe) {
		s.detail(fmt.Sprintf("正在辨識 %d 個語音片段…", len(spans)))
		tokens, err = recognize(ctx, paths, q.Language)
		if err != nil {
			return
		}
		if len(tokens) != len(paths) {
			err = errors.New("辨識結果與語音片段數量不符。")
			return
		}
	}
	s.detail("正在封裝訓練集…")
	speakerHash := sha256.Sum256([]byte("TimbreDeck speaker\x00" + q.Speaker))
	record := autoDatasetRecord{
		ID: key, Speaker: q.Speaker, SpeakerID: hex.EncodeToString(speakerHash[:16]),
		Language: q.Language, Denoise: q.Denoise, Created: time.Now().UTC(),
		Seconds: float64(len(full)) / autoDatasetRate,
	}
	keepPending := !trueByDefault(q.Transcribe) || !trueByDefault(q.SkipUnrecognized)
	if len(q.SourceNames) == 0 && q.Training == nil && !keepPending && q.Split == nil {
		sources = nil // Preserve the legacy CLI archive shape.
	}
	if err = packAutoDataset(ctx, filepath.Join(result, "dataset.zip"), paths, tokens, full, &record, sources, keepPending); err != nil {
		return
	}
	if err = writeJSON(filepath.Join(result, "record.json"), record); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	s.mu.Lock()
	err = ctx.Err()
	if err == nil {
		err = os.Rename(result, filepath.Join(base, key))
	}
	s.mu.Unlock()
	if err != nil || q.Training == nil {
		return
	}
	project, err := s.importDataset(parent, filepath.Join(base, key, "dataset.zip"), q.Speaker)
	if err != nil {
		return
	}
	if record.Pending > 0 {
		err = errors.New("已保存資料集及手動專案；有片段未有台詞。請先校對，再按「開始訓練」。")
		return
	}
	if err = trainable(project, q.Training.Batch); err != nil {
		return
	}
	err = s.trainProject(parent, key, project, *q.Training, trainer)
}

func loadAutoDatasetAudio(ctx context.Context, inputs []string, want uint64, denoised string) ([]int16, []int, error) {
	full := make([]int16, 0, want)
	lengths := make([]int, 0, len(inputs))
	var total int
	for i, path := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		w, _, err := cleanWAV(path, cleanClipBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("未能讀取第 %d 個 WAV：%w", i+1, err)
		}
		length := autoResampledFrames(w)
		total += length
		if length == 0 || w.rate > 192000 || uint64(total) > want {
			return nil, nil, errors.New("錄音長度或取樣率在處理期間變更。")
		}
		lengths = append(lengths, length)
		if denoised == "" {
			raw, err := readAutoWAVData(ctx, path, w)
			if err != nil {
				return nil, nil, err
			}
			out, err := resampleAutoDataset(ctx, raw, w, length)
			if err != nil {
				return nil, nil, fmt.Errorf("未能轉換第 %d 個 WAV：%w", i+1, err)
			}
			full = append(full, out...)
		}
	}
	if uint64(total) != want {
		return nil, nil, errors.New("錄音長度在處理期間變更。")
	}
	if denoised == "" {
		return full, lengths, nil
	}
	w, _, err := cleanWAV(denoised, cleanClipBytes)
	if err != nil || w.format != 1 || w.bits != 16 || w.channels != 1 || w.rate != autoDatasetRate ||
		int(w.size/w.block) != total {
		return nil, nil, errors.New("去噪引擎沒有輸出有效長度的 WAV。")
	}
	raw, err := readAutoWAVData(ctx, denoised, w)
	if err != nil {
		return nil, nil, err
	}
	full = full[:total]
	for i := range full {
		full[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return full, lengths, ctx.Err()
}

func readAutoWAVData(ctx context.Context, path string, w waveData) ([]byte, error) {
	f, err := cleanOpen(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, w.size)
	_, err = io.ReadFull(checkingReader{ctx, io.NewSectionReader(f, w.offset, int64(w.size))}, data)
	return data, err
}

func autoDatasetSample(src []byte, w waveData) float64 {
	if w.format == 3 {
		if w.bits == 32 {
			return float64(math.Float32frombits(binary.LittleEndian.Uint32(src)))
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(src))
	}
	switch w.bits {
	case 8:
		return float64(int(src[0])-128) / 128
	case 16:
		return float64(int16(binary.LittleEndian.Uint16(src))) / 32768
	case 24:
		bits := int32(src[0]) | int32(src[1])<<8 | int32(src[2])<<16
		return float64(bits<<8>>8) / 8388608
	default:
		return float64(int32(binary.LittleEndian.Uint32(src))) / 2147483648
	}
}

// Convert mono/stereo PCM or floating-point WAV with a low-pass windowed-sinc
// resampler. The normalized output length matches the optional denoiser.
func resampleAutoDataset(ctx context.Context, src []byte, w waveData, outFrames int) ([]int16, error) {
	inFrames, block, stride := int(w.size/w.block), int(w.block), int(w.bits/8)
	out := make([]int16, outFrames)
	if w.rate == autoDatasetRate && w.format == 1 && w.bits == 16 && w.channels == 1 {
		for i := range out {
			out[i] = int16(binary.LittleEndian.Uint16(src[i*2:]))
		}
		return out, ctx.Err()
	}
	mono := make([]float32, inFrames)
	for i := range mono {
		if i%16384 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		start := i * block
		level := autoDatasetSample(src[start:], w)
		if w.channels == 2 {
			level = 0.5*level + 0.5*autoDatasetSample(src[start+stride:], w)
		}
		if math.IsNaN(level) || math.IsInf(level, 0) {
			return nil, errors.New("WAV 含無效的浮點樣本")
		}
		mono[i] = float32(max(-1., min(1., level)))
	}
	toPCM := func(v float64) int16 {
		return int16(max(-32768., min(32767., math.Round(v*32768.))))
	}
	if w.rate == autoDatasetRate {
		for i, sample := range mono {
			out[i] = toPCM(float64(sample))
		}
		return out, ctx.Err()
	}
	const phases = 128
	cutoff := min(1., float64(autoDatasetRate)/float64(w.rate))
	radius := int(math.Ceil(12. / cutoff))
	kernels := make([][]float64, phases)
	for i := range kernels {
		kernel := make([]float64, 2*radius)
		var sum float64
		for j := range kernel {
			x := float64(j-radius+1) - float64(i)/phases
			t := math.Pi * cutoff * x
			value := cutoff
			if t != 0 {
				value *= math.Sin(t) / t
			}
			value *= .5 + .5*math.Cos(math.Pi*x/float64(radius))
			kernel[j] = value
			sum += value
		}
		for j := range kernel {
			kernel[j] /= sum
		}
		kernels[i] = kernel
	}
	ratio := float64(w.rate) / autoDatasetRate
	for i := range out {
		if i%4096 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		source := float64(i) * ratio
		base := int(source)
		kernel := kernels[min(phases-1, int((source-float64(base))*phases))]
		index := base - radius + 1
		var sum float64
		if index >= 0 && index+len(kernel) <= len(mono) {
			for j, weight := range kernel {
				sum += weight * float64(mono[index+j])
			}
		} else {
			for j, weight := range kernel {
				sum += weight * float64(mono[max(0, min(len(mono)-1, index+j))])
			}
		}
		out[i] = toPCM(sum)
	}
	return out, ctx.Err()
}

// Split around pauses, retain 100 ms of context, and bound continuous speech
// at 12 seconds. A quiet local boundary is preferred to a fixed hard cut.
func splitAutoDatasetSpeech(samples []int16) []autoDatasetSpan {
	const window = autoDatasetRate / 50
	const pad = autoDatasetRate / 10
	const silence = 350 * autoDatasetRate / 1000
	const minSpeech = autoDatasetRate / 4
	const maxClip = 12 * autoDatasetRate
	const gate = 260 // -42 dBFS
	energy := make([]int64, (len(samples)+window-1)/window)
	var spans []autoDatasetSpan
	first, last := -1, -1
	emit := func() {
		if first < 0 || last-first < minSpeech {
			return
		}
		start, end := max(0, first-pad), min(len(samples), last+pad)
		for end-start > maxClip {
			// Leave at least half a second for a trailing clip.
			limit := min(start+maxClip, end-autoDatasetRate/2) / window
			cut := limit
			for i := limit - 1; i >= limit-50; i-- {
				if energy[i] < energy[cut] {
					cut = i
				}
			}
			spans = append(spans, autoDatasetSpan{start, cut * window})
			start = cut * window
		}
		spans = append(spans, autoDatasetSpan{start, end})
	}
	for i := range energy {
		start, end := i*window, min(len(samples), (i+1)*window)
		for _, sample := range samples[start:end] {
			energy[i] += int64(sample) * int64(sample)
		}
		energy[i] /= int64(end - start)
	}
	for i, value := range energy {
		if value < gate*gate {
			continue
		}
		start := i * window
		if first < 0 || start-last >= silence {
			emit()
			first = start
		}
		last = min(len(samples), start+window)
	}
	emit()
	return spans
}

func writeAutoWAV(ctx context.Context, dst io.Writer, samples []int16) error {
	var header [44]byte
	copy(header[:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+2*len(samples)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], autoDatasetRate)
	binary.LittleEndian.PutUint32(header[28:], 2*autoDatasetRate)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(2*len(samples)))
	if _, err := dst.Write(header[:]); err != nil {
		return err
	}
	var buf [16 << 10]byte
	for len(samples) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(len(samples), len(buf)/2)
		for i, sample := range samples[:n] {
			binary.LittleEndian.PutUint16(buf[2*i:], uint16(sample))
		}
		if _, err := dst.Write(buf[:2*n]); err != nil {
			return err
		}
		samples = samples[n:]
	}
	return nil
}

func saveAutoWAV(ctx context.Context, file string, samples []int16) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = writeAutoWAV(ctx, f, samples)
	return errors.Join(err, f.Close())
}

func packAutoDataset(ctx context.Context, file string, paths []string, tokens []asrResult, full []int16, record *autoDatasetRecord, sources []datasetSource, keepPending bool) (err error) {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	archive := zip.NewWriter(f)
	defer func() { err = errors.Join(err, archive.Close()) }()
	create := func(name string) (io.Writer, error) {
		return archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	}
	var list, pending strings.Builder
	review := make(map[string]datasetSource)
	providers := map[string]bool{}
	for i, token := range tokens {
		if err := ctx.Err(); err != nil {
			return err
		}
		text := trainingTranscript(token.Text)
		if text == "" && !keepPending {
			record.Skipped++
			continue
		}
		if utf8.RuneCountInString(text) > 2048 {
			return errors.New("辨識文字異常過長，未建立訓練集。")
		}
		name, lines := "wavs/"+filepath.Base(paths[i]), &list
		if text == "" {
			name, lines = "review/"+filepath.Base(paths[i]), &pending
		}
		dst, err := create(name)
		if err != nil {
			return err
		}
		src, err := cleanOpen(paths[i])
		if err != nil {
			return err
		}
		info, err := src.Stat()
		if err != nil || info.Size() <= 44 || info.Size() > 44+2*cleanTimeLimit*autoDatasetRate {
			src.Close()
			return errors.New("訓練片段音訊檔無效。")
		}
		_, copyErr := io.CopyN(dst, checkingReader{ctx, src}, info.Size())
		if err := errors.Join(copyErr, src.Close()); err != nil {
			return err
		}
		fmt.Fprintf(lines, "%s|%s|%s|%s\n", name, record.SpeakerID, strings.ToUpper(record.Language), text)
		if i < len(sources) {
			review[name] = sources[i]
		}
		if text == "" {
			record.Pending++
			continue
		}
		if token.Provider != "" {
			providers[token.Provider] = true
		}
		record.Count++
	}
	if record.Count == 0 && record.Pending == 0 {
		return errors.New("辨識結果沒有可用的文字，未建立訓練集。")
	}
	names := make([]string, 0, len(providers))
	for provider := range providers {
		names = append(names, provider)
	}
	sort.Strings(names)
	record.Provider = strings.Join(names, ", ")
	dst, err := create("dataset.list")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(dst, list.String()); err != nil {
		return err
	}
	if pending.Len() > 0 {
		dst, err = create("pending.list")
		if err != nil {
			return err
		}
		if _, err = io.WriteString(dst, pending.String()); err != nil {
			return err
		}
	}
	if len(review) > 0 {
		dst, err = create("review.json")
		if err != nil {
			return err
		}
		if err = json.NewEncoder(dst).Encode(review); err != nil {
			return err
		}
	}
	dst, err = create("full.wav")
	if err != nil {
		return err
	}
	if err := writeAutoWAV(ctx, dst, full); err != nil {
		return err
	}
	dst, err = create("manifest.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(dst).Encode(record)
}

func (s *studio) readAutoDataset(key string) (autoDatasetRecord, bool) {
	var record autoDatasetRecord
	path := s.path(autoDatasetDir, key, "record.json")
	if !hexID(key, 32) || !s.managedPath(path) {
		return record, false
	}
	f, err := cleanOpen(path)
	if err != nil {
		return record, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > 16<<10 {
		return record, false
	}
	d := json.NewDecoder(io.LimitReader(f, 16<<10))
	if d.Decode(&record) != nil || d.Decode(new(any)) != io.EOF || record.ID != key ||
		!hexID(record.SpeakerID, 32) || record.Count < 0 || record.Pending < 0 ||
		record.Count+record.Pending < 1 || record.Count+record.Pending > autoDatasetMaxClips ||
		!language(record.Language) || record.Language == "auto" || record.Created.IsZero() ||
		record.Seconds <= 0 || record.Seconds > cleanTimeLimit {
		return record, false
	}
	return record, true
}

func (s *studio) autoDatasetHistory(w http.ResponseWriter, _ *http.Request) {
	records := []autoDatasetRecord{}
	path := s.path(autoDatasetDir)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		send(w, 200, records)
		return
	}
	if !s.managedPath(path) {
		fail(w, 500, "未能讀取訓練集歷史。")
		return
	}
	dirs, err := os.ReadDir(path)
	if err != nil {
		fail(w, 500, "未能讀取訓練集歷史。")
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if record, ok := s.readAutoDataset(d.Name()); ok {
			file := filepath.Join(path, record.ID, "dataset.zip")
			if st, err := os.Lstat(file); err == nil && st.Mode().IsRegular() && st.Size() <= autoDatasetMaxZip &&
				st.Size() > 0 && s.managedPath(file) {
				records = append(records, record)
			}
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Created.After(records[j].Created) })
	send(w, 200, records)
}

func (s *studio) downloadAutoDataset(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if _, ok := s.readAutoDataset(key); !ok {
		http.NotFound(w, r)
		return
	}
	path := s.path(autoDatasetDir, key, "dataset.zip")
	if !s.managedPath(path) {
		http.NotFound(w, r)
		return
	}
	f, err := cleanOpen(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 || st.Size() > autoDatasetMaxZip {
		http.NotFound(w, r)
		return
	}
	name := "TimbreDeck-Dataset-" + key + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *studio) pruneAutoDatasetStaging() {
	for _, category := range []string{autoDatasetDir, "cache"} {
		path := s.path(category)
		if !s.managedPath(path) {
			continue
		}
		dirs, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			suffix, ok := strings.CutPrefix(d.Name(), autoDatasetWorkPrefix)
			if !d.IsDir() || !ok || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
				continue
			}
			candidate := filepath.Join(path, d.Name())
			if s.managedPath(candidate) {
				_ = os.RemoveAll(candidate)
			}
		}
	}
}
