package main

import (
	"path/filepath"
	"testing"
)

func TestOverridesStoreSetAndClearAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "overrides.json")
	s := newOverridesStore(path)

	snap := s.snapshot()
	if snap.Global != "" || len(snap.Auths) != 0 {
		t.Fatalf("expected an empty fresh store, got %+v", snap)
	}

	if err := s.setAuth("acct.json", "gpt-5.6-luna"); err != nil {
		t.Fatalf("setAuth: %v", err)
	}
	snap = s.snapshot()
	if snap.Auths["acct.json"] != "gpt-5.6-luna" {
		t.Fatalf("expected acct.json override to be set, got %+v", snap)
	}

	// "auto" clears the override by deleting the entry, not by storing the
	// literal string "auto" (see modelOverrides doc comment).
	if err := s.setAuth("acct.json", ""); err != nil {
		t.Fatalf("clear setAuth: %v", err)
	}
	snap = s.snapshot()
	if _, ok := snap.Auths["acct.json"]; ok {
		t.Fatalf("expected acct.json override to be cleared, got %+v", snap)
	}
}

func TestOverridesStoreSetAndClearGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "overrides.json")
	s := newOverridesStore(path)

	if err := s.setGlobal("kimi-k2.8"); err != nil {
		t.Fatalf("setGlobal: %v", err)
	}
	if got := s.snapshot().Global; got != "kimi-k2.8" {
		t.Fatalf("Global = %q, want kimi-k2.8", got)
	}
	if err := s.setGlobal(""); err != nil {
		t.Fatalf("clear setGlobal: %v", err)
	}
	if got := s.snapshot().Global; got != "" {
		t.Fatalf("Global = %q, want cleared", got)
	}
}

func TestOverridesStorePersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "overrides.json")
	s1 := newOverridesStore(path)
	if err := s1.setAuth("acct.json", "gpt-5.6-luna"); err != nil {
		t.Fatalf("setAuth: %v", err)
	}
	if err := s1.setGlobal("kimi-k2.8"); err != nil {
		t.Fatalf("setGlobal: %v", err)
	}

	s2 := newOverridesStore(path)
	snap := s2.snapshot()
	if snap.Global != "kimi-k2.8" || snap.Auths["acct.json"] != "gpt-5.6-luna" {
		t.Fatalf("expected the reloaded store to see persisted overrides, got %+v", snap)
	}
}

func TestOverridesStoreMissingFileStartsEmpty(t *testing.T) {
	s := newOverridesStore(filepath.Join(t.TempDir(), "does-not-exist", "overrides.json"))
	snap := s.snapshot()
	if snap.Global != "" || len(snap.Auths) != 0 {
		t.Fatalf("expected a missing file to start empty, got %+v", snap)
	}
}
