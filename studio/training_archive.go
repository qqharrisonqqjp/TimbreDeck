package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxTrainingDataset = 512 << 20

type datasetSource struct {
	Group   string `json:"group"`
	Name    string `json:"name"`
	StartMS int64  `json:"startMs"`
}

func trainingTranscript(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '|' {
			return '｜'
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}

func clipSource(clip trainingClip) datasetSource {
	group := clip.SourceGroup
	if group == "" && hexID(clip.Reference, 64) {
		group = clip.Reference[:32]
	}
	return datasetSource{Group: group, Name: clip.SourceName, StartMS: clip.SourceOffsetMS + clip.StartMS}
}

func zipMembers(a *zip.Reader, maxFiles int, maxBytes uint64) (map[string]*zip.File, error) {
	if len(a.File) > maxFiles {
		return nil, errors.New("ZIP 內的檔案數量超出上限。")
	}
	files := make(map[string]*zip.File, len(a.File))
	var total uint64
	for _, entry := range a.File {
		if !entry.Mode().IsRegular() || !filepath.IsLocal(entry.Name) || strings.ContainsAny(entry.Name, "\\:") ||
			strings.Join(strings.FieldsFunc(entry.Name, func(r rune) bool { return r == '/' }), "/") != entry.Name ||
			strings.Contains("/"+entry.Name+"/", "/../") || strings.Contains("/"+entry.Name+"/", "/./") ||
			files[entry.Name] != nil || entry.UncompressedSize64 > maxBytes-total {
			return nil, errors.New("ZIP 檔案結構或大小不符。")
		}
		total += entry.UncompressedSize64
		files[entry.Name] = entry
	}
	return files, nil
}

func readZipJSON(entry *zip.File, maximum uint64, out any) error {
	if entry == nil || entry.UncompressedSize64 > maximum {
		return errors.New("ZIP 內的設定檔缺失或過大。")
	}
	f, err := entry.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, int64(maximum)+1))
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("ZIP 內的設定檔無效。")
	}
	return nil
}

func extractZipFile(ctx context.Context, entry *zip.File, dest string, hash io.Writer) error {
	src, err := entry.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	var dst io.Writer = out
	if hash != nil {
		dst = io.MultiWriter(out, hash)
	}
	n, err := io.Copy(dst, io.LimitReader(checkingReader{ctx, src}, int64(entry.UncompressedSize64)+1))
	if err == nil && uint64(n) != entry.UncompressedSize64 {
		err = io.ErrUnexpectedEOF
	}
	return errors.Join(err, out.Close())
}

func (s *studio) stageTrainingUpload(w http.ResponseWriter, r *http.Request, maximum int64) (string, func(), error) {
	file, err := os.CreateTemp(s.path("cache"), ".upload-*.zip")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.Remove(file.Name()) }
	body := http.MaxBytesReader(w, r.Body, maximum)
	defer body.Close()
	n, err := io.Copy(file, checkingReader{r.Context(), body})
	err = errors.Join(err, file.Close())
	if err != nil || n == 0 {
		remove()
		return "", nil, errors.New("ZIP 為空、過大，或傳送途中中斷。")
	}
	return file.Name(), remove, nil
}

func validTrainingName(name string, maximum int) bool {
	n := utf8.RuneCountInString(name)
	return n > 0 && n <= maximum && strings.IndexFunc(name, unicode.IsControl) < 0
}

func (s *studio) importTrainingDataset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "匯入的訓練專案"
	}
	if !validTrainingName(name, 80) {
		fail(w, 422, "請填寫有效的專案名稱（最多 80 字）。")
		return
	}
	archive, cleanup, err := s.stageTrainingUpload(w, r, maxTrainingDataset)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	defer cleanup()
	project, err := s.importDataset(r.Context(), archive, name)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	send(w, 201, project)
}

func (s *studio) importAutoProject(w http.ResponseWriter, r *http.Request) {
	record, ok := s.readAutoDataset(r.PathValue("id"))
	if !ok {
		fail(w, 404, "找不到已建立的資料集。")
		return
	}
	path := s.path(autoDatasetDir, record.ID, "dataset.zip")
	if !s.managedPath(path) {
		fail(w, 404, "找不到資料集 ZIP。")
		return
	}
	project, err := s.importDataset(r.Context(), path, record.Speaker)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	send(w, 201, project)
}

func (s *studio) importDataset(ctx context.Context, path, name string) (trainingProject, error) {
	project := trainingProject{ID: id(), Name: name, Revision: 1}
	a, err := zip.OpenReader(path)
	if err != nil {
		return project, errors.New("請選擇有效的 TimbreDeck 訓練集 ZIP。")
	}
	defer a.Close()
	files, err := zipMembers(&a.Reader, 312, maxTrainingDataset)
	if err != nil {
		return project, err
	}
	review := map[string]datasetSource{}
	if files["review.json"] != nil {
		if err := readZipJSON(files["review.json"], 512<<10, &review); err != nil {
			return project, err
		}
	}
	staged, err := os.MkdirTemp(s.path("cache"), ".review-*")
	if err != nil {
		return project, err
	}
	defer os.RemoveAll(staged)
	type audio struct{ from, to string }
	var copies []audio
	used := map[string]bool{}
	for _, list := range []struct{ name, folder, use string }{
		{"dataset.list", "wavs", "train"}, {"holdout.list", "holdout", "holdout"}, {"pending.list", "review", "pending"},
	} {
		entry := files[list.name]
		if entry == nil && list.use != "train" {
			continue
		}
		if entry == nil || entry.UncompressedSize64 > 8<<20 {
			return project, fmt.Errorf("缺少有效的 %s。", list.name)
		}
		rc, err := entry.Open()
		if err != nil {
			return project, err
		}
		scan := bufio.NewScanner(rc)
		scan.Buffer(make([]byte, 1024), 64<<10)
		for scan.Scan() {
			if strings.TrimSpace(scan.Text()) == "" {
				continue
			}
			p := strings.Split(scan.Text(), "|")
			if len(p) != 4 || !strings.HasPrefix(p[0], list.folder+"/") ||
				!strings.HasSuffix(p[0], ".wav") || !hexID(strings.TrimSuffix(strings.TrimPrefix(p[0], list.folder+"/"), ".wav"), 32) ||
				files[p[0]] == nil || used[p[0]] || !hexID(p[1], 32) || !slices.Contains([]string{"yue", "zh", "en", "ja", "ko"}, strings.ToLower(p[2])) ||
				!utf8.ValidString(p[3]) || len([]rune(strings.TrimSpace(p[3]))) > 10000 || strings.ContainsAny(p[3], "\r\n") ||
				(list.use != "pending" && strings.TrimSpace(p[3]) == "") || len(project.Clips) >= 300 {
				rc.Close()
				return project, errors.New("資料集清單、語言或對應 WAV 無效（最多 300 個片段）。")
			}
			used[p[0]] = true
			key := id()
			local := filepath.Join(staged, key+".wav")
			sha := sha256.New()
			if err := extractZipFile(ctx, files[p[0]], local, sha); err != nil {
				rc.Close()
				return project, err
			}
			wav, _, err := cleanWAV(local, cleanClipBytes)
			if err != nil {
				rc.Close()
				return project, errors.New("資料集包含無效或太大的 PCM16 WAV。")
			}
			duration := int64(wav.size/wav.block) * 1000 / int64(wav.rate)
			if duration <= 0 {
				rc.Close()
				return project, errors.New("資料集包含空白 WAV。")
			}
			ref := hex.EncodeToString(sha.Sum(nil))
			source := review[p[0]]
			if !hexID(source.Group, 32) {
				source.Group = p[1]
			}
			if !validTrainingName(source.Name, 255) {
				source.Name = name
			}
			if source.StartMS < 0 || source.StartMS > 24*60*60*1000 {
				source.StartMS = 0
			}
			copies = append(copies, audio{local, s.path("voices", ref+".wav")})
			project.Clips = append(project.Clips, trainingClip{ID: key, Reference: ref,
				SourceName: source.Name, SourceGroup: source.Group, SourceOffsetMS: source.StartMS,
				EndMS: duration, Language: strings.ToLower(p[2]), Transcript: strings.TrimSpace(p[3]), Use: list.use})
		}
		err = errors.Join(scan.Err(), rc.Close())
		if err != nil {
			return project, err
		}
	}
	if len(project.Clips) == 0 {
		return project, errors.New("ZIP 未有任何可匯入的片段。")
	}
	for key := range files {
		if !used[key] && !slices.Contains([]string{"dataset.list", "holdout.list", "pending.list", "review.json", "full.wav", "manifest.json"}, key) {
			return project, errors.New("資料集 ZIP 內有不受支援的檔案。")
		}
	}
	s.files.Lock()
	defer s.files.Unlock()
	if err := ctx.Err(); err != nil {
		return project, err
	}
	if !s.managedPath(s.path("voices")) || !s.managedPath(s.path("training")) {
		return project, errors.New("訓練專案或錄音資料夾無效。")
	}
	for _, pair := range copies {
		if _, err := os.Lstat(pair.to); os.IsNotExist(err) {
			if err := os.Rename(pair.from, pair.to); err != nil && (!installedFile(pair.to) || !s.managedPath(pair.to)) {
				return project, err
			}
		} else if err != nil || !installedFile(pair.to) || !s.managedPath(pair.to) {
			return project, errors.New("錄音儲存位置無效。")
		}
	}
	result, err := os.MkdirTemp(s.path("training"), ".project-*")
	if err != nil {
		return project, err
	}
	defer os.RemoveAll(result)
	if err := writeJSON(filepath.Join(result, "project.json"), project); err != nil {
		return project, err
	}
	return project, os.Rename(result, s.path("training", project.ID))
}
