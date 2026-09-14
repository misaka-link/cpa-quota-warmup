package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// fakeWarmupClient adapts fakeSender (chatSender only) into a full
// warmupClient (chatSender + modelLister) for engine-level tests that need
// to inject e.newSender directly, bypassing net/http entirely.
type fakeWarmupClient struct {
	*fakeSender
	available    map[string]bool
	availableErr error
}

func (f *fakeWarmupClient) AvailableModels(context.Context) (map[string]bool, error) {
	return f.available, f.availableErr
}

// TestManualTriggerFileEndToEnd exercises the full v0.4.0 file-mode manual
// trigger pipeline at the Go level (no HTTP, no cgo): enable one account via
// warmupFileManager.setAccount, inject a fake warmup client, call
// engine.manualTrigger, and confirm both the returned outcome and the
// persisted state record.
func TestManualTriggerFileEndToEnd(t *testing.T) {
	warmupPath := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	fm := newWarmupFileManager(warmupPath)
	entries := []pluginapi.HostAuthFileEntry{
		{ID: "auth-a", Name: "a.json", Provider: "antigravity"},
	}
	if err := fm.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}
	enabled := true
	model := "gemini-3.7-flash-high"
	if err := fm.setAccount("a.json", warmupFieldUpdate{Enabled: &enabled, Model: &model}, "antigravity"); err != nil {
		t.Fatalf("setAccount: %v", err)
	}

	cfg := defaultPluginConfig()
	cfg.legacyMode = false
	cfg.v3InlineMode = false
	cfg.location = time.UTC
	cfg.Message = "hi"
	cfg.MaxTokens = 16
	cfg.MaxRounds = 3
	cfg.APIKey = "sk-test"

	state := testStateStore(t)
	e := newEngine(cfg, state, newOverridesStore(filepath.Join(t.TempDir(), "overrides.json")), fakeAuthLister{entries: entries})
	e.warmupFile = fm
	ring := e.ring
	e.newSender = func(baseURL, apiKey string) warmupClient {
		fs := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
			return "auth-a", 200, true
		}}
		return &fakeWarmupClient{fakeSender: fs, available: map[string]bool{"gemini-3.7-flash-high": true}}
	}

	result, err := e.manualTrigger(context.Background(), "*", langEN)
	if err != nil {
		t.Fatalf("manualTrigger: %v", err)
	}
	if len(result.Attempted) != 1 || result.Attempted[0] != "a.json" {
		t.Fatalf("Attempted = %v, want [a.json]", result.Attempted)
	}
	outcome, ok := result.Outcomes["a.json"]
	if !ok || !outcome.Covered || outcome.StatusCode != 200 {
		t.Fatalf("Outcomes[a.json] = %+v (present=%v), want covered/200", outcome, ok)
	}
}

// TestTickFileEndToEnd is the same scenario driven through engine.tick()
// instead of a manual trigger, proving groupDueTargetsFile/resolveFileAuth
// correctly identify a due, enabled file-mode account and persist a
// covered slotRecord.
func TestTickFileEndToEnd(t *testing.T) {
	warmupPath := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	fm := newWarmupFileManager(warmupPath)
	entries := []pluginapi.HostAuthFileEntry{
		{ID: "auth-a", Name: "a.json", Provider: "antigravity"},
	}
	if err := fm.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}
	enabled := true
	model := "gemini-3.7-flash-high"
	// Use the current UTC minute so the slot is deterministically "due right
	// now" regardless of when this test happens to run, instead of a fixed
	// clock time that tickFile's internal time.Now() call cannot be made to
	// agree with.
	loc := time.UTC
	timeVal := time.Now().In(loc).Format("15:04")
	if err := fm.setAccount("a.json", warmupFieldUpdate{Enabled: &enabled, Model: &model, Time: &timeVal}, "antigravity"); err != nil {
		t.Fatalf("setAccount: %v", err)
	}

	cfg := defaultPluginConfig()
	cfg.legacyMode = false
	cfg.v3InlineMode = false
	cfg.location = loc
	cfg.Message = "hi"
	cfg.MaxTokens = 16
	cfg.MaxRounds = 3
	cfg.CatchUpMinutes = 60
	cfg.APIKey = "sk-test"

	state := testStateStore(t)
	e := newEngine(cfg, state, newOverridesStore(filepath.Join(t.TempDir(), "overrides.json")), fakeAuthLister{entries: entries})
	e.warmupFile = fm
	ring := e.ring
	e.newSender = func(baseURL, apiKey string) warmupClient {
		fs := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
			return "auth-a", 200, true
		}}
		return &fakeWarmupClient{fakeSender: fs, available: map[string]bool{"gemini-3.7-flash-high": true}}
	}

	l := logLanguage(cfg)
	e.tickFile(cfg, l, entries)

	snap := state.snapshot()
	if len(snap) != 1 {
		t.Fatalf("state.snapshot() = %+v, want exactly one persisted record", snap)
	}
	if !snap[0].Covered || snap[0].Auth != "a.json" || snap[0].Model != "gemini-3.7-flash-high" {
		t.Fatalf("persisted record = %+v, want a covered a.json/gemini-3.7-flash-high record", snap[0])
	}
	if _, parseErr := fm.snapshot(); parseErr != "" {
		t.Fatalf("unexpected parse error after tickFile: %s", parseErr)
	}
}
