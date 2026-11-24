package main

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type trainingClip struct {
	ID         string `json:"id"`
	Reference  string `json:"reference"`
	SourceName string `json:"sourceName"`
	StartMS    int64  `json:"startMs"`
	EndMS      int64  `json:"endMs"`
	Language   string `json:"language"`
	Transcript string `json:"transcript"`
	Use        string `json:"use"`
}

type trainingProject struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Revision int            `json:"revision"`
	Clips    []trainingClip `json:"clips"`
}

const gptTrainingSourceRevision = "d7c2210da8c013e81a94bfc7b811a477c99fd506"

type trainedModel struct {
	Engine         string `json:"engine"`
	Model          string `json:"model"`
	SourceRevision string `json:"source_revision"`
	GPT            string `json:"gpt"`
	SoVITS         string `json:"sovits"`
}

func (s *studio) loadTrainedModel(projectID string) (trainedModel, error) {
	var model trainedModel
	if !hexID(projectID, 32) {
		return model, os.ErrNotExist
	}
	folder := s.path("training", projectID, "run")
	if err := readJSON(filepath.Join(folder, "model.json"), &model); err != nil {
		return model, err
	}
	if model.Engine != "gpt-sovits" || model.Model != "v2Pro" || model.SourceRevision != gptTrainingSourceRevision ||
		!filepath.IsLocal(model.GPT) || !filepath.IsLocal(model.SoVITS) ||
		filepath.Ext(model.GPT) != ".ckpt" || filepath.Ext(model.SoVITS) != ".pth" {
		return model, errors.New("微調模型資料與 GPT-SoVITS v2Pro 不相容。")
	}
	model.GPT = filepath.Join(folder, model.GPT)
	model.SoVITS = filepath.Join(folder, model.SoVITS)
	if !installedFile(model.GPT) || !installedFile(model.SoVITS) {
		return model, errors.New("微調模型缺少配對的權重。")
	}
	return model, nil
}

func (s *studio) trainedModels(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.path("training"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	type choice struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	choices := []choice{}
	for _, entry := range entries {
		project, err := s.loadTraining(entry.Name())
		if err != nil {
			continue
		}
		if _, err := s.loadTrainedModel(project.ID); err == nil {
			choices = append(choices, choice{ID: project.ID, Name: project.Name})
		}
	}
	send(w, 200, choices)
}

func (s *studio) loadTraining(key string) (trainingProject, error) {
	var project trainingProject
	if !hexID(key, 32) {
		return project, os.ErrNotExist
	}
	err := readJSON(s.path("training", key, "project.json"), &project)
	if err == nil && project.ID != key {
		err = errors.New("訓練專案編號不符。")
	}
	return project, err
}

func (s *studio) trainingProjects(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.path("training"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	projects := []trainingProject{}
	for _, entry := range entries {
		if entry.IsDir() {
			if project, err := s.loadTraining(entry.Name()); err == nil {
				projects = append(projects, project)
			}
		}
	}
	send(w, 200, projects)
}

func (s *studio) saveTraining(w http.ResponseWriter, r *http.Request) {
	var project trainingProject
	if !decode(w, r, &project) {
		return
	}
	project.Name = strings.TrimSpace(project.Name)
	if len([]rune(project.Name)) == 0 || len([]rune(project.Name)) > 80 || len(project.Clips) > 300 {
		fail(w, 422, "請填寫專案名稱。每個專案最多保存 300 個片段。")
		return
	}
	if project.Clips == nil {
		project.Clips = []trainingClip{}
	}
	seen := map[string]bool{}
	for i := range project.Clips {
		clip := &project.Clips[i]
		if clip.ID == "" {
			clip.ID = id()
		}
		clip.Transcript = strings.TrimSpace(clip.Transcript)
		validUse := slices.Contains([]string{"pending", "train", "holdout", "skip"}, clip.Use)
		if !hexID(clip.ID, 32) || seen[clip.ID] || !validUse ||
			!slices.Contains([]string{"yue", "zh", "en", "ja", "ko"}, clip.Language) ||
			strings.ContainsAny(clip.Transcript, "|\r\n") || len([]rune(clip.Transcript)) > 10000 {
			fail(w, 422, fmt.Sprintf("片段 %d 的語言、用途或逐字稿無效。逐字稿請維持單行。", i+1))
			return
		}
		if (clip.Use == "train" || clip.Use == "holdout") && clip.Transcript == "" {
			fail(w, 422, fmt.Sprintf("請填寫片段 %d 的逐字稿。", i+1))
			return
		}
		source, _, _, _, err := s.openTrainingClip(*clip)
		if err != nil {
			fail(w, 422, fmt.Sprintf("片段 %d：%s", i+1, err))
			return
		}
		source.Close()
		seen[clip.ID] = true
	}
	s.files.Lock()
	defer s.files.Unlock()
	key := r.PathValue("id")
	if key == "" {
		if project.ID != "" || project.Revision != 0 {
			fail(w, 400, "新的專案不能沿用現有編號。")
			return
		}
		project.ID = id()
		if err := os.Mkdir(s.path("training", project.ID), 0700); err != nil {
			fail(w, 500, err.Error())
			return
		}
	} else {
		current, err := s.loadTraining(key)
		if err != nil {
			fail(w, 404, "找不到訓練專案。")
			return
		}
		if project.ID != key || current.Revision != project.Revision {
			fail(w, 409, "專案已有更新，請重新載入後再儲存。")
			return
		}
	}
	project.Revision++
	if err := writeJSON(s.path("training", project.ID, "project.json"), project); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if key == "" {
		send(w, 201, project)
	} else {
		send(w, 200, project)
	}
}

func (s *studio) openTrainingClip(clip trainingClip) (*os.File, waveData, int64, int64, error) {
	bad := errors.New("請選擇 PCM16 WAV 及錄音範圍內的起訖毫秒。")
	if !hexID(clip.Reference, 64) || clip.StartMS < 0 || clip.EndMS <= clip.StartMS {
		return nil, waveData{}, 0, 0, bad
	}
	f, err := os.Open(s.path("voices", clip.Reference+".wav"))
	if err != nil {
		return nil, waveData{}, 0, 0, errors.New("找不到原錄音。")
	}
	wav, err := parseWAV(f)
	if err != nil || wav.format != 1 || wav.bits != 16 {
		f.Close()
		return nil, waveData{}, 0, 0, bad
	}
	rate := int64(wav.rate)
	frames := int64(wav.size / wav.block)
	if clip.EndMS > frames*1000/rate {
		f.Close()
		return nil, waveData{}, 0, 0, bad
	}
	start := clip.StartMS * rate / 1000
	end := clip.EndMS * rate / 1000
	if end <= start {
		f.Close()
		return nil, waveData{}, 0, 0, bad
	}
	return f, wav, start, end, nil
}

func (s *studio) splitTrainingClip(w http.ResponseWriter, r *http.Request) {
	var clip trainingClip
	if !decode(w, r, &clip) {
		return
	}
	src, wav, start, end, err := s.openTrainingClip(clip)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	defer src.Close()
	rate, block := int64(wav.rate), int64(wav.block)
	input := checkingReader{r.Context(), io.NewSectionReader(src, wav.offset+start*block, (end-start)*block)}
	clips, err := detectTrainingSegments(input, clip, rate, block, start, end)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	send(w, 200, clips)
}

// Detect speech by comparing the PCM energy in each 20 ms window. Keep a
// short padding on both sides and split on pauses of at least 400 ms.
func detectTrainingSegments(input io.Reader, clip trainingClip, rate, block, start, end int64) ([]trainingClip, error) {
	const rmsThreshold = 328 // Approximately -40 dBFS for PCM16.
	window := rate / 50
	buffer := make([]byte, window*block)
	var clips []trainingClip
	first, last := int64(-1), int64(-1)
	appendSegment := func() {
		if first < 0 || last-first < rate/5 {
			return
		}
		segment := clip
		segment.ID, segment.Transcript, segment.Use = "", "", "pending"
		segment.StartMS = max(clip.StartMS, (first-rate/10)*1000/rate)
		segment.EndMS = min(clip.EndMS, (last+rate/10)*1000/rate)
		clips = append(clips, segment)
	}
	for pos := start; pos < end; pos += window {
		frames := min(window, end-pos)
		data := buffer[:frames*block]
		if _, err := io.ReadFull(input, data); err != nil {
			return nil, err
		}
		var energy int64
		for i := 0; i < len(data); i += 2 {
			sample := int64(int16(binary.LittleEndian.Uint16(data[i:])))
			energy += sample * sample
		}
		if energy < int64(len(data)/2)*rmsThreshold*rmsThreshold {
			continue
		}
		if first < 0 || pos-last >= rate*4/10 {
			appendSegment()
			first = pos
		}
		last = pos + frames
		if len(clips) >= 300 {
			return nil, errors.New("片段過多，請縮短錄音範圍。")
		}
	}
	appendSegment()
	if len(clips) == 0 {
		return nil, errors.New("未偵測到足夠音量的片段，可手動調整起訖。")
	}
	return clips, nil
}

func (s *studio) writeTrainingWAV(ctx context.Context, dst io.Writer, clip trainingClip) error {
	src, wav, start, end, err := s.openTrainingClip(clip)
	if err != nil {
		return err
	}
	defer src.Close()
	offset := start * int64(wav.block)
	length := (end - start) * int64(wav.block)
	var header [44]byte
	copy(header[:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+length))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], wav.format)
	binary.LittleEndian.PutUint16(header[22:], wav.channels)
	binary.LittleEndian.PutUint32(header[24:], wav.rate)
	binary.LittleEndian.PutUint32(header[28:], wav.rate*wav.block)
	binary.LittleEndian.PutUint16(header[32:], uint16(wav.block))
	binary.LittleEndian.PutUint16(header[34:], wav.bits)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(length))
	if _, err := dst.Write(header[:]); err != nil {
		return err
	}
	_, err = io.CopyN(dst, checkingReader{ctx, io.NewSectionReader(src, wav.offset+offset, length)}, length)
	return err
}

func (s *studio) trainingAudio(w http.ResponseWriter, r *http.Request) {
	project, err := s.loadTraining(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, clip := range project.Clips {
		if clip.ID == r.PathValue("clip") {
			f, err := os.CreateTemp(s.path("cache"), ".clip-*.wav")
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			defer os.Remove(f.Name())
			defer f.Close()
			if err := s.writeTrainingWAV(r.Context(), f, clip); err != nil {
				fail(w, 422, err.Error())
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			http.ServeContent(w, r, "clip.wav", time.Time{}, f)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *studio) trainingDataset(w http.ResponseWriter, r *http.Request) {
	project, err := s.loadTraining(r.PathValue("id"))
	if err != nil {
		fail(w, 404, "找不到訓練專案。")
		return
	}
	if !slices.ContainsFunc(project.Clips, func(clip trainingClip) bool { return clip.Use == "train" }) {
		fail(w, 422, "請先選擇至少一個已校對的訓練片段。")
		return
	}
	f, err := os.CreateTemp(s.path("cache"), ".dataset-*.zip")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := s.writeTrainingDataset(r.Context(), f, project); err != nil {
		fail(w, 500, err.Error())
		return
	}
	name := "TimbreDeck-" + project.ID + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeContent(w, r, name, time.Time{}, f)
}

func (s *studio) writeTrainingDataset(ctx context.Context, dst io.Writer, project trainingProject) error {
	archive := zip.NewWriter(dst)
	lists := map[string]*strings.Builder{"train": {}, "holdout": {}}
	for _, clip := range project.Clips {
		list := lists[clip.Use]
		if list == nil {
			continue
		}
		folder := "wavs"
		if clip.Use == "holdout" {
			folder = "holdout"
		}
		path := folder + "/" + clip.ID + ".wav"
		file, err := archive.Create(path)
		if err != nil {
			return err
		}
		if err := s.writeTrainingWAV(ctx, file, clip); err != nil {
			return err
		}
		fmt.Fprintf(list, "%s|%s|%s|%s\n", path, project.ID, strings.ToUpper(clip.Language), clip.Transcript)
	}
	for _, list := range []struct{ use, name string }{{"train", "dataset.list"}, {"holdout", "holdout.list"}} {
		file, err := archive.Create(list.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(file, lists[list.use].String()); err != nil {
			return err
		}
	}
	return archive.Close()
}

func (s *studio) trainingTranscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelPath string
		Revision  int
	}
	if !decode(w, r, &in) {
		return
	}
	project, err := s.loadTraining(r.PathValue("id"))
	if err != nil || project.Revision != in.Revision {
		fail(w, 409, "請先儲存或重新載入訓練專案。")
		return
	}
	if !slices.ContainsFunc(project.Clips, needsTranscription) {
		fail(w, 422, "未有待校對的空白片段。")
		return
	}
	paths, err := s.paths()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	engine := s.inspect("gpt-sovits", paths).Path
	if !installedFile(filepath.Join(engine, "runtime", "python.exe")) {
		fail(w, 422, "請先連接有 faster-whisper 的 GPT-SoVITS 執行環境。")
		return
	}
	model := strings.TrimSpace(in.ModelPath)
	if model == "" {
		model = filepath.Join(engine, "GPT_SoVITS", "pretrained_models", "faster-whisper-large-v3")
	}
	if !filepath.IsAbs(model) || len(missingFiles(model, []string{"model.bin", "config.json", "tokenizer.json"})) > 0 {
		fail(w, 422, "請輸入完整的本機 faster-whisper 模型資料夾路徑。")
		return
	}
	ctx, j, ok := s.reserve("gpt-sovits", "transcription")
	if !ok {
		fail(w, 409, "已有作業進行中。")
		return
	}
	go func() { s.finish(s.transcribe(ctx, j.ID, engine, model, project)) }()
	send(w, 202, j)
}

func needsTranscription(clip trainingClip) bool {
	return clip.Use == "pending" && clip.Transcript == ""
}

func (s *studio) transcribe(ctx context.Context, jobID, engine, model string, project trainingProject) error {
	temp, err := os.MkdirTemp(s.path("cache"), ".transcribe-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	type input struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		Language string `json:"language"`
	}
	var clips []input
	for _, clip := range project.Clips {
		if !needsTranscription(clip) {
			continue
		}
		path := filepath.Join(temp, clip.ID+".wav")
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		err = s.writeTrainingWAV(ctx, f, clip)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		clips = append(clips, input{ID: clip.ID, Path: path, Language: clip.Language})
	}
	output := filepath.Join(temp, "transcripts.json")
	payload, err := json.Marshal(map[string]any{"clips": clips, "model": model, "output": output})
	if err != nil {
		return err
	}
	script, err := assets.ReadFile("transcribe.py")
	if err != nil {
		return err
	}
	logFile, err := os.Create(s.path("logs", jobID+".log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	python := filepath.Join(engine, "runtime", "python.exe")
	cmd := exec.CommandContext(ctx, python, "-c", string(script))
	cmd.Dir = engine
	cmd.Stdin = strings.NewReader(string(payload))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(cmd.Environ(), "PYTHONUTF8=1", "PYTHONNOUSERSITE=1", "HF_HUB_OFFLINE=1",
		"PATH="+filepath.Dir(python)+string(os.PathListSeparator)+engine+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("轉錄失敗。請查看 logs/%s.log（%v）。", jobID, err)
	}
	transcripts := map[string]string{}
	if err := readJSON(output, &transcripts); err != nil {
		return err
	}
	s.files.Lock()
	defer s.files.Unlock()
	current, err := s.loadTraining(project.ID)
	if err != nil || current.Revision != project.Revision {
		return errors.New("轉錄期間專案已有更新，請儲存後再試。")
	}
	for i, clip := range current.Clips {
		if needsTranscription(clip) {
			text, found := transcripts[clip.ID]
			if !found || strings.ContainsAny(text, "|\r\n") || len([]rune(text)) > 10000 {
				return errors.New("轉錄輸出缺少片段或含有無效文字。")
			}
			current.Clips[i].Transcript = text
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current.Revision++
	return writeJSON(s.path("training", current.ID, "project.json"), current)
}
