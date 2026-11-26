package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPunctuationSplit(t *testing.T) {
	const text = "第一句很好。第二句更長更長！最後一句。"
	if got := splitSynthesis(text, 13); !reflect.DeepEqual(got, []string{"第一句很好。", "第二句更長更長！最後一句。"}) {
		t.Fatal(got)
	}
	if got := splitSynthesis("alpha beta gamma delta", 11); !reflect.DeepEqual(got, []string{"alpha beta", "gamma delta"}) {
		t.Fatal(got)
	}
	if got := splitSynthesis("abcdefg", 3); !reflect.DeepEqual(got, []string{"abc", "def", "g"}) {
		t.Fatal(got)
	}
}
func tinyPCM(rate, n int, v uint16) []byte {
	b := make([]byte, 44+2*n)
	copy(b, "RIFF    WAVEfmt ")
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate)*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	binary.LittleEndian.PutUint32(b[40:], uint32(2*n))
	for i := 44; i < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], v)
	}
	return b
}
func TestJoinSpeechExactGap(t *testing.T) {
	p := t.TempDir()
	a, b, d := filepath.Join(p, "a.wav"), filepath.Join(p, "b.wav"), filepath.Join(p, "d.wav")
	for path, raw := range map[string][]byte{a: tinyPCM(24000, 100, 1000), b: tinyPCM(24000, 75, 2000)} {
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := joinSpeech(context.Background(), d, []string{a, b}, 125); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(d)
	if e != nil {
		t.Fatal(e)
	}
	want := append(append(tinyPCM(24000, 100, 1000)[44:], make([]byte, 6000)...), tinyPCM(24000, 75, 2000)[44:]...)
	if !bytes.Equal(data[44:], want) {
		t.Fatal("unexpected samples or silence", len(data))
	}
	if sec, _, e := wavInfo(d); e != nil || sec != float64(3175)/24000 {
		t.Fatalf("%v %v", sec, e)
	}
}
func TestSeparateSpeechArchive(t *testing.T) {
	p := t.TempDir()
	a, b := filepath.Join(p, "a.wav"), filepath.Join(p, "b.wav")
	dst := filepath.Join(p, "part.zip")
	x, y := tinyPCM(24000, 100, 500), tinyPCM(24000, 200, 550)
	if e := os.WriteFile(a, x, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(b, y, 0600); e != nil {
		t.Fatal(e)
	}
	n, r, e := packSpeechParts(context.Background(), dst, []string{a, b})
	if e != nil || n != .0125 || r != 24000 {
		t.Fatal(n, r, e)
	}
	z, e := zip.OpenReader(dst)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	for i, name := range []string{"part-001.wav", "part-002.wav"} {
		if z.File[i].Name != name {
			t.Fatal(z.File[i].Name)
		}
		f, e := z.File[i].Open()
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(f)
		f.Close()
		if e != nil || !bytes.Equal(data, [][]byte{x, y}[i]) {
			t.Fatal(e)
		}
	}
}
func TestUploadMediaContainers(t *testing.T) {
	bin, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("optional ffmpeg missing")
	}
	stage := t.TempDir()
	s, e := openStudio(filepath.Join(stage, "client"))
	if e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(stage, "fixture.wav")
	if e = os.WriteFile(source, tinyPCM(16000, 16000, 1024), 0600); e != nil {
		t.Fatal(e)
	}
	for _, x := range []string{"wav", "mp3", "mp4", "m4a", "aac", "mov"} {
		t.Run(x, func(t *testing.T) {
			file := source
			if x != "wav" {
				file = filepath.Join(stage, "file."+x)
				if out, e := exec.Command(bin, "-y", "-nostdin", "-v", "error", "-i", source, file).CombinedOutput(); e != nil {
					t.Fatalf("%s %v", out, e)
				}
			}
			f, e := os.Open(file)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			res := httptest.NewRecorder()
			s.reference(res, httptest.NewRequest("POST", "/api/reference", f))
			if res.Code != 201 {
				t.Fatal(res.Code, res.Body.String())
			}
			var v struct {
				ID, SourceSha256 string
				Seconds          float64
			}
			if e = json.Unmarshal(res.Body.Bytes(), &v); e != nil || len(v.ID) != 64 || len(v.SourceSha256) != 64 || v.Seconds < .9 || v.Seconds > 1.2 {
				t.Fatal(v, e)
			}
			if x == "wav" && v.ID != v.SourceSha256 {
				t.Fatal("WAV ID changed")
			}
			ready, _ := os.Open(s.path("voices", v.ID+".wav"))
			defer ready.Close()
			if hdr, _ := io.ReadAll(ready); !strings.HasPrefix(string(hdr), "RIFF") {
				t.Fatal("no decoded WAV")
			}
		})
	}
	bad := httptest.NewRecorder()
	s.reference(bad, httptest.NewRequest("POST", "/api/reference", strings.NewReader("#EXTM3U\nhttps://example.invalid/x\n")))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatal(bad.Code)
	}
}
