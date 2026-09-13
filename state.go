package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stateSchemaVersion = 1
	stateRetentionDays = 7
	stateFileName      = "state.json"
	stateDirName       = "quota-warmup"
)

// slotRecord is the persisted outcome of one (auth, calendar day, HH:MM)
// warmup attempt.
type slotRecord struct {
	Auth        string    `json:"auth"`
	Date        string    `json:"date"`
	Time        string    `json:"time"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	TriggeredAt time.Time `json:"triggered_at"`
	Covered     bool      `json:"covered"`
	Rounds      int       `json:"rounds"`
	StatusCode  int       `json:"status_code,omitempty"`
	Warning     string    `json:"warning,omitempty"`
}

func slotKey(auth, date, hhmm string) string {
	return auth + "|" + date + "|" + hhmm
}

type stateFile struct {
	Version int                   `json:"version"`
	Slots   map[string]slotRecord `json:"slots"`
}

// stateStore is the process-wide handle on the persisted schedule state. All
// access goes through its mutex: the background scheduler goroutine writes
// after every attempt, and the management "status" route reads a snapshot
// concurrently.
type stateStore struct {
	mu   sync.Mutex
	path string
	data stateFile
}

// newStateStore loads path if it exists, or starts empty (a missing or
// corrupt file is never fatal -- it just means we may re-run today's slots
// once, which is harmless).
func newStateStore(path string) *stateStore {
	s := &stateStore{path: path, data: stateFile{Version: stateSchemaVersion, Slots: map[string]slotRecord{}}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded stateFile
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return s
	}
	if loaded.Slots == nil {
		loaded.Slots = map[string]slotRecord{}
	}
	loaded.Version = stateSchemaVersion
	s.data = loaded
	return s
}

// isRecorded reports whether a slot already has a persisted outcome, meaning
// the scheduler must not trigger it again.
func (s *stateStore) isRecorded(auth, date, hhmm string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Slots[slotKey(auth, date, hhmm)]
	return ok
}

// record stores rec and persists the store to disk, then prunes anything
// older than the retention window. Save errors are returned so the caller can
// log them, but the in-memory record is kept either way -- losing the file on
// a write failure must not cause the same slot to fire repeatedly.
func (s *stateStore) record(rec slotRecord, now time.Time, loc *time.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Slots[slotKey(rec.Auth, rec.Date, rec.Time)] = rec
	pruneSlots(&s.data, now, loc, stateRetentionDays)
	return saveStateFile(s.path, s.data)
}

// snapshot returns a stable-ordered copy of every persisted slot, newest
// first, for the management status route.
func (s *stateStore) snapshot() []slotRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]slotRecord, 0, len(s.data.Slots))
	for _, rec := range s.data.Slots {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		if out[i].Time != out[j].Time {
			return out[i].Time > out[j].Time
		}
		return out[i].Auth < out[j].Auth
	})
	return out
}

// pruneSlots drops any slot record whose Date is older than retainDays
// relative to now (in loc). Mutates data in place.
func pruneSlots(data *stateFile, now time.Time, loc *time.Location, retainDays int) {
	cutoff := now.In(loc).AddDate(0, 0, -retainDays).Format(slotDateFormat)
	for key, rec := range data.Slots {
		if rec.Date < cutoff {
			delete(data.Slots, key)
		}
	}
}

// saveStateFile writes data to path atomically (temp file + rename). This is
// our own file, never watched by CPA's config hot-reload inotify watcher, so
// unlike config.yaml a rename-based replace here is safe and preferred.
func saveStateFile(path string, data stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp state file: %w", err)
	}
	return nil
}

// stateFilePath resolves the state file location relative to the process
// working directory (CPA's cwd), mirroring how cpa-usage-panel resolves its
// own data-dir.
func stateFilePath() (string, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Join(workDir, stateDirName, stateFileName), nil
}

// stateKeyProviderGlob is a tiny helper used only by management.go's "run"
// route to accept an optional auth-name glob for a manual trigger; kept here
// next to slotKey since both are string-shape helpers for the same map.
func stateKeyProviderGlob(pattern, name string) bool {
	if strings.TrimSpace(pattern) == "" {
		return true
	}
	return globMatch(pattern, name)
}
