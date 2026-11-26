package main

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"
)

func packSpeechParts(ctx context.Context, dst string, paths []string) (seconds float64, rate uint32, err error) {
	f, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return 0, 0, e
	}
	defer f.Close()
	z := zip.NewWriter(f)
	var size int64
	for i, p := range paths {
		if e = ctx.Err(); e != nil {
			return 0, 0, e
		}
		sec, hz, e := wavInfo(p)
		if e != nil || sec <= 0 {
			return 0, 0, errors.New("片段 WAV 無效。")
		}
		if i == 0 {
			rate = hz
		}
		seconds += sec
		src, e := os.Open(p)
		if e != nil {
			return 0, 0, e
		}
		info, e := src.Stat()
		if e != nil {
			src.Close()
			return 0, 0, e
		}
		size += info.Size()
		if size > 512<<20 {
			src.Close()
			return 0, 0, errors.New("輸出超過 512 MiB。")
		}
		file, e := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("part-%03d.wav", i+1), Method: zip.Store})
		if e == nil {
			_, e = io.Copy(file, asrContextReader{Context: ctx, Reader: src})
		}
		src.Close()
		if e != nil {
			return 0, 0, e
		}
	}
	if e := z.Close(); e != nil {
		return 0, 0, e
	}
	return seconds, rate, f.Close()
}
func (s *studio) audioParts(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !hexID(key, 32) {
		http.NotFound(w, r)
		return
	}
	name := s.path("outputs", key, "parts.zip")
	if !s.managedPath(name) || !installedFile(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=TimbreDeck-Parts-"+key+".zip")
	http.ServeFile(w, r, name)
}

func splitSynthesis(text string, limit int) []string {
	var parts []string
	r := []rune(strings.TrimSpace(text))
	for len(r) > limit {
		strong, weak, space := 0, 0, 0
		for i, c := range r[:limit] {
			switch {
			case strings.ContainsRune("。！？!?；;\n", c) || c == '.' && (i+1 == len(r) || unicode.IsSpace(r[i+1])):
				strong = i + 1
			case strings.ContainsRune("，、,：:", c):
				weak = i + 1
			case unicode.IsSpace(c):
				space = i + 1
			}
		}
		cut := strong
		if cut == 0 {
			cut = weak
		}
		if cut == 0 {
			cut = space
		}
		if cut == 0 {
			cut = limit
		}
		if p := strings.TrimSpace(string(r[:cut])); p != "" {
			parts = append(parts, p)
		}
		r = r[cut:]
		for len(r) > 0 && unicode.IsSpace(r[0]) {
			r = r[1:]
		}
	}
	if p := strings.TrimSpace(string(r)); p != "" {
		parts = append(parts, p)
	}
	return parts
}
func (s *studio) updateSynthesisProgress(id string, done, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.ID == id {
		s.active.Completed = done
		s.active.Total = total
	}
}
func joinSpeech(ctx context.Context, dst string, paths []string, gap int) error {
	f, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	header := make([]byte, 44)
	if _, e = f.Write(header); e != nil {
		return e
	}
	var rate uint32
	var size uint64
	for i, p := range paths {
		if e = ctx.Err(); e != nil {
			return e
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		w, e := parseWAV(in)
		if e == nil && (w.format != 1 || w.channels != 1 || w.bits != 16 || w.block != 2 || (i > 0 && w.rate != rate)) {
			e = errors.New("切分輸出必須為相同取樣率的單聲道 PCM16。")
		}
		if e != nil {
			in.Close()
			return e
		}
		rate = w.rate
		if i > 0 && gap > 0 {
			pad := (uint64(gap)*uint64(rate) + 500) / 1000 * 2
			if _, e = f.Write(make([]byte, pad)); e != nil {
				in.Close()
				return e
			}
			size += pad
		}
		size += uint64(w.size)
		if size > 512<<20 {
			in.Close()
			return errors.New("合併 WAV 超過 512 MiB。")
		}
		_, e = io.CopyN(f, io.NewSectionReader(in, w.offset, int64(w.size)), int64(w.size))
		in.Close()
		if e != nil {
			return e
		}
	}
	if rate == 0 || size == 0 {
		return errors.New("沒有輸出音訊。")
	}
	copy(header, "RIFF    WAVEfmt ")
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[4:], uint32(size)+36)
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	binary.LittleEndian.PutUint32(header[40:], uint32(size))
	if _, e = f.WriteAt(header, 0); e != nil {
		return e
	}
	return f.Close()
}
