package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSTSModelCatalogue(t *testing.T) {
	assets := t.TempDir()
	t.Setenv("TIMBREDECK_STS_ASSETS", assets)
	s, err := openStudio(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.listSTSModels(w, httptest.NewRequest("GET", "/api/sts/models", nil))
	var rows []stsModel
	if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 || len(rows) != 5 {
		t.Fatalf("bad STS catalogue: %d %v %s", w.Code, err, w.Body.String())
	}
	if rows[0].ID != defaultSTS || !rows[0].Mic {
		t.Fatal("CosyVoice defaults changed")
	}
	for _, row := range rows[1:] {
		if row.Ready || row.Mic {
			t.Fatalf("uninstalled model advertised as usable: %+v", row)
		}
	}
	if x, _, _, e := s.resolveSTS("../seed-vc-tiny"); x.ID != "" || e == nil {
		t.Fatal("accepted a non-catalogue model id")
	}
	py := filepath.Join(assets, "python.exe")
	if err := os.WriteFile(py, []byte("runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(assets, "runtime.json"), map[string]string{"python": py}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(assets, "verified"), 0700); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(assets, "verified", "seed-vc-tiny.json")
	for _, name := range []string{"../missing", "missing"} {
		if err := writeJSON(stamp, map[string]any{"model": "seed-vc-tiny", "files": []string{name}}); err != nil {
			t.Fatal(err)
		}
		if x, _, _, e := s.resolveSTS("seed-vc-tiny"); x.Ready || e == nil {
			t.Fatalf("accepted invalid local asset %q", name)
		}
	}
	if err := writeJSON(stamp, map[string]any{"model": "seed-vc-tiny", "files": []string{"python.exe"}}); err != nil {
		t.Fatal(err)
	}
	if x, root, model, e := s.resolveSTS("seed-vc-tiny"); !x.Ready || root != assets || model != x.ID || e != nil {
		t.Fatalf("verified runtime not resolved: %+v %q %q %v", x, root, model, e)
	}
	w = httptest.NewRecorder()
	s.liveSTS(w, httptest.NewRequest("POST", "/api/sts/live?model=seed-vc-tiny", strings.NewReader("")))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "咪高峰尚未驗證") {
		t.Fatalf("expected unsupported mic response, got %d %s", w.Code, w.Body.String())
	}
	upstream := filepath.Join(assets, "upstream", "seed-vc", "real-time-gui.py")
	if e := os.MkdirAll(filepath.Dir(upstream), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(upstream, []byte("# installed"), 0600); e != nil {
		t.Fatal(e)
	}
	if x, _, _, e := s.resolveSTS(seedSTS); e != nil || !x.Mic {
		t.Fatalf("did not recognize installed Seed-VC realtime kernel: %+v %v", x, e)
	}
	w = httptest.NewRecorder()
	s.liveSTS(w, httptest.NewRequest("POST", "/api/sts/live?model=seed-vc-tiny&seq=0", strings.NewReader("")))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "作業編號") {
		t.Fatalf("expected missing session rejection, got %d %s", w.Code, w.Body.String())
	}
}
