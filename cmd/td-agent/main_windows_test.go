package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKeepsExistingDataAndFindsPortableLayout(t *testing.T) {
	dir := t.TempDir()
	write := func(relative string) {
		file := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("Start.exe")
	a, err := resolve(dir, "")
	if err != nil || a.Studio != filepath.Join(dir, "Start.exe") || a.Data != filepath.Join(dir, "StudioData") {
		t.Fatalf("portable launch mismatch: %+v %v", a, err)
	}
	write("source/local-active.txt")
	write("source/sts-active.txt")
	write("source/Start-STS.exe")
	b, err := resolve(dir, "")
	if err != nil || b.Studio != filepath.Join(dir, "source", "Start-STS.exe") ||
		b.Data != filepath.Join(dir, "acceptance-80ebf58", "more", "release", "StudioData") {
		t.Fatalf("migration changed existing workspace: %+v %v", b, err)
	}
	special := filepath.Join(dir, "工作區")
	c, err := resolve(dir, special)
	if err != nil || c.Data != special {
		t.Fatal("override ignored")
	}
	if _, err = resolve(dir, "relative"); err == nil {
		t.Fatal("relative override accepted")
	}
}
