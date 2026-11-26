package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// The extension is a hint in file pickers only; the server requires an actual
// audio container. No auto-detected playlists, network loaders, subtitles or video.
func mediaDemuxer(head []byte) string {
	if len(head) >= 12 && (string(head[4:8]) == "ftyp" || string(head[4:8]) == "moov" || string(head[4:8]) == "mdat" || string(head[4:8]) == "wide") {
		return "mov"
	}
	if len(head) >= 3 && string(head[:3]) == "ID3" {
		return "mp3"
	}
	if len(head) >= 2 && head[0] == 255 {
		if (head[1] & 0xf6) == 0xf0 {
			return "aac"
		}
		if head[1]&0xe0 == 0xe0 && head[1]&6 != 0 {
			return "mp3"
		}
	}
	return ""
}

func transcodeReference(ctx context.Context, source, destination string) (float64, string, error) {
	f, err := os.Open(source)
	if err != nil {
		return 0, "", err
	}
	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	f.Close()
	kind := mediaDemuxer(head[:n])
	if kind == "" {
		return 0, "", errors.New("請匯入 WAV、MP3、MP4、M4A、AAC 或 MOV 錄音。")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return 0, "", errors.New("未找到 FFmpeg；請安裝並加入 PATH，或先匯入 WAV。")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	args := []string{"-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-threads", "1", "-f", kind}
	if kind == "mov" {
		args = append(args, "-enable_drefs", "0")
	}
	args = append(args, "-i", source, "-map", "0:a:0", "-vn", "-sn", "-dn", "-ar", "32000", "-ac", "1", "-c:a", "pcm_s16le", "-t", "181", "-f", "wav", "-y", destination)
	if err = exec.CommandContext(ctx, ffmpeg, args...).Run(); err != nil {
		return 0, "", errors.New("無法讀取此檔嘅音軌，請確認檔案內有可用錄音。")
	}
	sec, err := wavDuration(destination)
	if err != nil || sec <= 0 || sec > 180 {
		return 0, "", errors.New("匯入音軌必須有聲音資料，長度不可超過 180 秒。")
	}
	f, err = os.Open(destination)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return 0, "", err
	}
	return sec, hex.EncodeToString(h.Sum(nil)), nil
}
