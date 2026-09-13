package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateStoreRecordAndIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "state.json")
	s := newStateStore(path)
	loc := time.UTC
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)

	if s.isRecorded("acct.json", "2026-09-13", "05:30") {
		t.Fatalf("expected a fresh store to have no records")
	}
	rec := slotRecord{Auth: "acct.json", Date: "2026-09-13", Time: "05:30", Covered: true, Rounds: 1, StatusCode: 200}
	if err := s.record(rec, now, loc); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !s.isRecorded("acct.json", "2026-09-13", "05:30") {
		t.Fatalf("expected the slot to be recorded")
	}
	if s.isRecorded("acct.json", "2026-09-13", "10:35") {
		t.Fatalf("a different HH:MM must be a different slot")
	}
	if s.isRecorded("other.json", "2026-09-13", "05:30") {
		t.Fatalf("a different auth must be a different slot")
	}
}

func TestStateStorePersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "state.json")
	loc := time.UTC
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)

	s1 := newStateStore(path)
	rec := slotRecord{Auth: "acct.json", Date: "2026-09-13", Time: "05:30", Covered: true}
	if err := s1.record(rec, now, loc); err != nil {
		t.Fatalf("record: %v", err)
	}

	s2 := newStateStore(path)
	if !s2.isRecorded("acct.json", "2026-09-13", "05:30") {
		t.Fatalf("expected the reloaded store to see the previously persisted slot")
	}
}

func TestStateStoreMissingFileStartsEmpty(t *testing.T) {
	s := newStateStore(filepath.Join(t.TempDir(), "does-not-exist", "state.json"))
	if s.isRecorded("acct.json", "2026-09-13", "05:30") {
		t.Fatalf("expected a missing file to start with no records")
	}
}

func TestStateStorePruneRetainsSevenDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "state.json")
	s := newStateStore(path)
	loc := time.UTC
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)

	old := now.AddDate(0, 0, -8)
	within := now.AddDate(0, 0, -6)
	if err := s.record(slotRecord{Auth: "acct.json", Date: slotDateKey(old, loc), Time: "05:30"}, old, loc); err != nil {
		t.Fatalf("record old: %v", err)
	}
	if err := s.record(slotRecord{Auth: "acct.json", Date: slotDateKey(within, loc), Time: "05:30"}, within, loc); err != nil {
		t.Fatalf("record within: %v", err)
	}
	// Recording "now" triggers the prune relative to "now", which should drop
	// the 8-day-old entry but keep the 6-day-old one and the fresh one.
	if err := s.record(slotRecord{Auth: "acct.json", Date: slotDateKey(now, loc), Time: "05:30"}, now, loc); err != nil {
		t.Fatalf("record now: %v", err)
	}

	if s.isRecorded("acct.json", slotDateKey(old, loc), "05:30") {
		t.Fatalf("expected the 8-day-old slot to be pruned")
	}
	if !s.isRecorded("acct.json", slotDateKey(within, loc), "05:30") {
		t.Fatalf("expected the 6-day-old slot to survive pruning")
	}
	if !s.isRecorded("acct.json", slotDateKey(now, loc), "05:30") {
		t.Fatalf("expected today's slot to survive pruning")
	}
}

func TestStateStoreSnapshotOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup", "state.json")
	s := newStateStore(path)
	loc := time.UTC
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)

	_ = s.record(slotRecord{Auth: "b.json", Date: "2026-09-12", Time: "05:30"}, now, loc)
	_ = s.record(slotRecord{Auth: "a.json", Date: "2026-09-13", Time: "05:30"}, now, loc)
	_ = s.record(slotRecord{Auth: "a.json", Date: "2026-09-13", Time: "10:35"}, now, loc)

	snap := s.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	// Newest date first, then newest time first within a date.
	if snap[0].Date != "2026-09-13" || snap[0].Time != "10:35" {
		t.Fatalf("snap[0] = %+v, want 2026-09-13 10:35", snap[0])
	}
	if snap[1].Date != "2026-09-13" || snap[1].Time != "05:30" {
		t.Fatalf("snap[1] = %+v, want 2026-09-13 05:30", snap[1])
	}
	if snap[2].Date != "2026-09-12" {
		t.Fatalf("snap[2] = %+v, want 2026-09-12", snap[2])
	}
}
