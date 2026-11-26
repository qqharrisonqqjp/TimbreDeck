package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
)

func (s *studio) saveAvatar(w http.ResponseWriter, r *http.Request) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if e != nil {
		fail(w, 413, "頭像最多 5 MiB。")
		return
	}
	cfg, t, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || (t != "png" && t != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		fail(w, 422, "請選擇 4096 × 4096 以內的 JPEG 或 PNG。")
		return
	}
	src, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		fail(w, 422, "圖片讀取失敗。")
		return
	}
	bounds := src.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	x := bounds.Min.X + (bounds.Dx()-side)/2
	y := bounds.Min.Y + (bounds.Dy()-side)/2
	resized := image.NewNRGBA(image.Rect(0, 0, 160, 160))
	for yy := 0; yy < 160; yy++ {
		for xx := 0; xx < 160; xx++ {
			resized.Set(xx, yy, src.At(x+xx*side/160, y+yy*side/160))
		}
	}
	// Re-encode pixels only; discard embedded location/camera metadata.
	img := image.NewNRGBA(resized.Bounds())
	draw.Draw(img, img.Bounds(), resized, image.Point{}, draw.Src)
	var out bytes.Buffer
	if e = png.Encode(&out, img); e != nil {
		fail(w, 500, "頭像轉換失敗。")
		return
	}
	h := sha256.Sum256(out.Bytes())
	id := hex.EncodeToString(h[:])
	folder := s.path("voices", "avatars")
	if e = s.ensureManagedDirectory(folder); e != nil {
		fail(w, 500, "頭像資料夾不可用。")
		return
	}
	f, e := os.CreateTemp(folder, ".avatar-")
	if e != nil {
		fail(w, 500, "無法保存頭像。")
		return
	}
	defer os.Remove(f.Name())
	_, e = f.Write(out.Bytes())
	ce := f.Close()
	target := s.path("voices", "avatars", id+".png")
	if e != nil || ce != nil {
		fail(w, 500, "無法保存頭像。")
		return
	}
	if !installedFile(target) {
		if e = os.Rename(f.Name(), target); e != nil && !installedFile(target) {
			fail(w, 500, "無法保存頭像。")
			return
		}
	}
	send(w, 201, map[string]string{"id": id})
}
func (s *studio) avatar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !hexID(id, 64) {
		http.NotFound(w, r)
		return
	}
	p := s.path("voices", "avatars", id+".png")
	if !s.managedPath(p) || !installedFile(p) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, p)
}
