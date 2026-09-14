package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestMigrateOverridesToWarmupFileMovesModelsAndRenames(t *testing.T) {
	dir := t.TempDir()
	overridesPath := filepath.Join(dir, "overrides.json")
	ov := modelOverrides{
		Global: "kimi-k2.8",
		Auths:  map[string]string{"codex-alice-team.json": "gpt-5.6-luna"},
	}
	raw, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal overrides: %v", err)
	}
	if err := os.WriteFile(overridesPath, raw, 0o644); err != nil {
		t.Fatalf("write overrides.json: %v", err)
	}

	fm := newWarmupFileManager(filepath.Join(dir, "quota-warmup.yaml"))
	entries := []pluginapi.HostAuthFileEntry{
		{Name: "codex-alice-team.json", Provider: "codex"},
		{Name: "kimi-bob.json", Provider: "kimi"},
	}
	migrated, err := migrateOverridesToWarmupFile(overridesPath, fm, entries)
	if err != nil {
		t.Fatalf("migrateOverridesToWarmupFile: %v", err)
	}
	if !migrated {
		t.Fatalf("expected migrated=true")
	}

	if _, err := os.Stat(overridesPath); !os.IsNotExist(err) {
		t.Fatalf("expected overrides.json to be renamed away, stat err=%v", err)
	}
	if _, err := os.Stat(overridesPath + ".migrated"); err != nil {
		t.Fatalf("expected overrides.json.migrated to exist: %v", err)
	}

	data, parseErr := fm.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error: %s", parseErr)
	}
	if data.Defaults.Model != "kimi-k2.8" {
		t.Fatalf("Defaults.Model = %q, want the migrated global override kimi-k2.8", data.Defaults.Model)
	}
	alice := data.Accounts["codex-alice-team.json"]
	if alice.Model != "gpt-5.6-luna" {
		t.Fatalf("alice.Model = %q, want the migrated per-auth override gpt-5.6-luna", alice.Model)
	}
}

func TestMigrateOverridesToWarmupFileNoOpWhenMissing(t *testing.T) {
	dir := t.TempDir()
	fm := newWarmupFileManager(filepath.Join(dir, "quota-warmup.yaml"))
	migrated, err := migrateOverridesToWarmupFile(filepath.Join(dir, "overrides.json"), fm, nil)
	if err != nil {
		t.Fatalf("migrateOverridesToWarmupFile: %v", err)
	}
	if migrated {
		t.Fatalf("expected migrated=false when overrides.json does not exist")
	}
}

func TestMigrateOverridesToWarmupFileLeavesCorruptFileAlone(t *testing.T) {
	dir := t.TempDir()
	overridesPath := filepath.Join(dir, "overrides.json")
	if err := os.WriteFile(overridesPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write corrupt overrides.json: %v", err)
	}
	fm := newWarmupFileManager(filepath.Join(dir, "quota-warmup.yaml"))
	migrated, err := migrateOverridesToWarmupFile(overridesPath, fm, nil)
	if err == nil {
		t.Fatalf("expected an error for a corrupt overrides.json")
	}
	if migrated {
		t.Fatalf("expected migrated=false on error")
	}
	if _, statErr := os.Stat(overridesPath); statErr != nil {
		t.Fatalf("expected the corrupt overrides.json to be left in place, stat err=%v", statErr)
	}
}
