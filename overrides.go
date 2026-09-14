package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const overridesFileName = "overrides.json"

// modelOverrides is what the panel's "set" route persists: manual model
// pins that outrank every other tier of the resolution chain (see
// resolveModelSpec in config.go). An empty/absent entry means "no panel
// override" -- the "自动"/"Auto" button clears an entry rather than storing
// the literal string "auto", so presence alone always means "the operator
// pinned this from the panel".
type modelOverrides struct {
	Global string            `json:"global,omitempty"`
	Auths  map[string]string `json:"auths,omitempty"`
}

// overridesStore is the process-wide, disk-backed handle on modelOverrides.
// Like stateStore, it is never watched by CPA's own config hot-reload
// inotify watcher, so a rename-based atomic replace is safe here.
type overridesStore struct {
	mu   sync.Mutex
	path string
	data modelOverrides
}

// newOverridesStore loads path if it exists; a missing or corrupt file just
// starts empty (no panel overrides yet), never fatal.
func newOverridesStore(path string) *overridesStore {
	s := &overridesStore{path: path, data: modelOverrides{Auths: map[string]string{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded modelOverrides
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return s
	}
	if loaded.Auths == nil {
		loaded.Auths = map[string]string{}
	}
	s.data = loaded
	return s
}

// snapshot returns a copy of the current overrides, safe to read without
// holding the store's lock any longer.
func (s *overridesStore) snapshot() modelOverrides {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := modelOverrides{Global: s.data.Global, Auths: make(map[string]string, len(s.data.Auths))}
	for k, v := range s.data.Auths {
		out.Auths[k] = v
	}
	return out
}

// setGlobal pins (model != "") or clears (model == "") the global override
// and persists the store.
func (s *overridesStore) setGlobal(model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Global = model
	return saveOverridesFile(s.path, s.data)
}

// setAuth pins (model != "") or clears (model == "") the per-account override
// for name and persists the store.
func (s *overridesStore) setAuth(name, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Auths == nil {
		s.data.Auths = map[string]string{}
	}
	if model == "" {
		delete(s.data.Auths, name)
	} else {
		s.data.Auths[name] = model
	}
	return saveOverridesFile(s.path, s.data)
}

// saveOverridesFile writes data to path atomically (temp file + rename),
// mirroring state.go's saveStateFile.
func saveOverridesFile(path string, data modelOverrides) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create overrides dir: %w", err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode overrides: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".overrides-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp overrides file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp overrides file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp overrides file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp overrides file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp overrides file: %w", err)
	}
	return nil
}

// overridesFilePath resolves the overrides file location relative to the
// process working directory (CPA's cwd), next to state.json.
func overridesFilePath() (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Join(workDir, stateDirName, overridesFileName), nil
}
