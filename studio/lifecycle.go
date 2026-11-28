package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type modelAsset struct {
	ID       string `json:"id"`
	Variant  string `json:"variant"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Provider string `json:"provider,omitempty"`
}

func (s *studio) managedPath(path string) bool {
	expected, err := filepath.Rel(s.root, path)
	if err != nil || expected == "." || !filepath.IsLocal(expected) {
		return false
	}
	base, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return false
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	actualRelative, err := filepath.Rel(base, actual)
	return err == nil && actualRelative == expected
}

func (s *studio) ensureManagedDirectory(path string) error {
	if path == s.root {
		return nil
	}
	relative, err := filepath.Rel(s.root, path)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("儲存位置位於工作區以外。")
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() || !s.managedPath(path) {
			return errors.New("工作區的儲存資料夾位置無效。")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := s.ensureManagedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return s.ensureManagedDirectory(path)
}

func (s *studio) ownedPack(root, engine, id string) (manifest, bool) {
	var p manifest
	if !installedPackName.MatchString(id) || !s.managedPath(root) ||
		readJSON(filepath.Join(root, packMetadata), &p) != nil || p.check(engine) != nil {
		return p, false
	}
	return p, strings.HasPrefix(id, "pack-"+p.Hash[:16])
}

func (s *studio) installedModels(w http.ResponseWriter, r *http.Request) {
	engine := r.PathValue("engine")
	if !supported(engine) {
		fail(w, 404, "找不到此引擎。")
		return
	}
	s.files.Lock()
	defer s.files.Unlock()
	items := []modelAsset{}
	for _, variant := range engineCatalog[engine].Variants {
		roots := []struct{ path, source string }{
			{s.path("models", engine, variant), "model"},
			{s.path("runtimes", engine), "bundle"},
		}
		for _, entry := range roots {
			for _, id := range packFolders(entry.path) {
				root := filepath.Join(entry.path, id)
				version, provider, _, owned := s.modelOwnership(root, engine, variant, id, entry.source)
				path := filepath.Join(root, modelFolder(engine, variant))
				if info, err := os.Lstat(path); owned && err == nil && info.IsDir() && s.managedPath(path) {
					items = append(items, modelAsset{ID: id, Variant: variant, Version: version, Source: entry.source, Path: root, Provider: provider})
				}
			}
		}
	}
	send(w, 200, items)
}

func (s *studio) modelOwnership(root, engine, variant, key, source string) (string, string, any, bool) {
	if source == "model" {
		if model, owned := s.ownedUpstream(root, engine, variant, key); owned {
			return model.Version, "Hugging Face", model, true
		}
	}
	p, owned := s.ownedPack(root, engine, key)
	return p.Version, "", p, owned
}

func (s *studio) beginRemoval(w http.ResponseWriter) bool {
	s.mu.Lock()
	if s.busy() {
		s.mu.Unlock()
		fail(w, 409, "請等候目前作業完成或取消後再移除。")
		return false
	}
	s.files.Lock()
	return true
}

func (s *studio) endRemoval() {
	s.files.Unlock()
	s.mu.Unlock()
}

// Rename the selected files out of the active layout first. Roll back any
// moves if the state update fails. RemoveAll does not follow symlinks.
func removeResources(paths []string, commit func() error) error {
	type movement struct{ from, to string }
	var moved []movement
	rollback := func(cause error) error {
		for i := len(moved) - 1; i >= 0; i-- {
			cause = errors.Join(cause, os.Rename(moved[i].to, moved[i].from))
		}
		return cause
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return rollback(err)
		}
		staged := filepath.Join(filepath.Dir(path), ".removed-"+id())
		if err := os.Rename(path, staged); err != nil {
			return rollback(err)
		}
		moved = append(moved, movement{path, staged})
	}
	if err := commit(); err != nil {
		return rollback(err)
	}
	var err error
	for _, item := range moved {
		err = errors.Join(err, os.RemoveAll(item.to))
	}
	return err
}

func (s *studio) removalRecord(kind, engine, variant, version, asset string, details any) error {
	dir := s.path("config", "removed-assets")
	if err := s.ensureManagedDirectory(dir); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, id()+".json"), map[string]any{
		"kind": kind, "engine": engine, "variant": variant, "version": version,
		"asset": asset, "removed": time.Now().UTC(), "details": details,
	})
}

// A verified legacy bundle may contain both engine code and model data. Move
// each model directory intact to its own managed root before removing the code.
func (s *studio) preserveBundleModels(p manifest, runtime string) error {
	for _, variant := range engineCatalog[p.Engine].Variants {
		folder := modelFolder(p.Engine, variant)
		from := filepath.Join(runtime, folder)
		info, err := os.Lstat(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || !s.managedPath(from) {
			return fmt.Errorf("無法安全保留 %s 模型資料夾。", variant)
		}
		parent := s.path("models", p.Engine, variant)
		if err := s.ensureManagedDirectory(parent); err != nil {
			return err
		}
		dst := filepath.Join(parent, "pack-"+p.Hash[:16]+"-"+id()[:8])
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, folder)), 0700); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(dst, packMetadata), p); err != nil {
			return err
		}
		if err := os.Rename(from, filepath.Join(dst, folder)); err != nil {
			return err
		}
	}
	return nil
}

func (s *studio) preserveBundleData(runtime, engine, key string) error {
	entries, err := os.ReadDir(runtime)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		keep := name == "pretrained_models" || name == "models" || name == "checkpoints" ||
			name == "voices" || name == "outputs" || name == "training" || name == "recordings" ||
			name == "config" || name == "logs" || strings.Contains(name, "weights") ||
			strings.HasSuffix(name, ".wav") || strings.HasSuffix(name, ".ckpt") ||
			strings.HasSuffix(name, ".safetensors") || strings.HasSuffix(name, ".pth")
		if !keep {
			continue
		}
		from := filepath.Join(runtime, entry.Name())
		if !s.managedPath(from) {
			return errors.New("整合包內有無法安全搬移的資料。")
		}
		dst := s.path("preserved", engine, key)
		if err := s.ensureManagedDirectory(dst); err != nil {
			return err
		}
		to := filepath.Join(dst, entry.Name())
		if _, err := os.Lstat(to); !os.IsNotExist(err) {
			to += "-" + id()[:8]
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

func (s *studio) removeRuntime(w http.ResponseWriter, r *http.Request) {
	engine, key := r.PathValue("engine"), r.PathValue("id")
	if !supported(engine) || !installedPackName.MatchString(key) {
		fail(w, 400, "請選擇由聲台安裝的引擎。")
		return
	}
	if !s.beginRemoval(w) {
		return
	}
	defer s.endRemoval()
	root := s.path("runtimes", engine, key)
	p, managed := s.ownedPack(root, engine, key)
	if !managed {
		fail(w, 404, "找不到由聲台安裝的引擎。")
		return
	}
	paths, err := s.paths()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.preserveBundleModels(p, root); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.preserveBundleData(root, engine, key); err != nil {
		fail(w, 500, err.Error())
		return
	}
	active := s.runtimePath(engine, paths[engine]) == root
	err = removeResources([]string{root}, func() error {
		if err := s.removalRecord("runtime", engine, "", p.Version, key, p); err != nil {
			return err
		}
		if active {
			delete(paths, engine)
			return writeJSON(s.path("config", "engines.json"), paths)
		}
		return nil
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, 200, s.inspectSelected(engine, paths))
}

func (s *studio) removeOfficialModel(w http.ResponseWriter, r *http.Request) {
	engine, variant, key := r.PathValue("engine"), r.PathValue("variant"), r.PathValue("id")
	source := r.URL.Query().Get("source")
	if !validPackTarget(engine, "model", variant) || !installedPackName.MatchString(key) ||
		(source != "bundle" && source != "model") {
		fail(w, 400, "請選擇由聲台安裝的模型。")
		return
	}
	if !s.beginRemoval(w) {
		return
	}
	defer s.endRemoval()
	root := s.path("models", engine, variant, key)
	selected := root
	if source == "bundle" {
		root = s.path("runtimes", engine, key)
		selected = filepath.Join(root, modelFolder(engine, variant))
	}
	version, _, metadata, managed := s.modelOwnership(root, engine, variant, key, source)
	if !managed || !s.managedPath(selected) {
		fail(w, 404, "找不到由聲台安裝的模型。")
		return
	}
	if err := removeResources([]string{selected}, func() error {
		return s.removalRecord("model", engine, variant, version, key, metadata)
	}); err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, 200, map[string]string{"id": key})
}

func (s *studio) removeTrainedModel(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("id")
	if !s.beginRemoval(w) {
		return
	}
	defer s.endRemoval()
	if hexID(key, 32) {
		folder := s.path("finetunes", key)
		if installedFile(filepath.Join(folder, "model.json")) && s.managedPath(folder) {
			metadata, err := s.loadTrainedModel(key)
			if err != nil {
				fail(w, 404, "找不到可移除的微調模型。")
				return
			}
			if err := removeResources([]string{folder}, func() error {
				return s.removalRecord("trained", "gpt-sovits", "v2Pro", "", key, map[string]any{"name": metadata.Name})
			}); err != nil {
				fail(w, 500, err.Error())
				return
			}
			send(w, 200, map[string]string{"id": key})
			return
		}
	}
	project, err := s.loadTraining(key)
	folder := s.path("training", project.ID)
	if err != nil || !s.managedPath(folder) {
		fail(w, 404, "找不到訓練專案。")
		return
	}
	lock, err := lockWorkspace(filepath.Join(folder, ".run.lock"))
	if err != nil {
		fail(w, 409, "請先停止此訓練專案的訓練程序。")
		return
	}
	defer lock.Close()
	run := filepath.Join(folder, "run")
	metadata, err := s.loadTrainedModel(key)
	if err != nil || !s.managedPath(run) {
		fail(w, 404, "找不到可移除的微調模型。")
		return
	}
	items := []string{
		filepath.Join(run, "model.json"), filepath.Join(run, "s1-complete.json"), filepath.Join(run, "s2-complete.json"),
		filepath.Join(run, "s1-weights"), filepath.Join(run, "s2-weights"),
		filepath.Join(run, "logs_s1_v2Pro", "ckpt"), filepath.Join(run, "data", "logs_s2_v2Pro"),
	}
	for _, path := range items {
		if _, err := os.Lstat(path); !os.IsNotExist(err) && (err != nil || !s.managedPath(path)) {
			fail(w, 422, "微調權重資料夾位置無效。")
			return
		}
	}
	if err := removeResources(items, func() error {
		return s.removalRecord("trained", "gpt-sovits", "v2Pro", "", key, map[string]any{"name": project.Name, "model": metadata})
	}); err != nil {
		fail(w, 500, err.Error())
		return
	}
	send(w, 200, map[string]string{"id": key})
}
