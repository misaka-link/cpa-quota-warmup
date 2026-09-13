package main

import (
	"testing"
	"time"
)

func TestSlotTime(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, loc)
	slot, ok := slotTime(now, "05:30", loc)
	if !ok {
		t.Fatalf("expected slotTime to parse 05:30")
	}
	want := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)
	if !slot.Equal(want) {
		t.Fatalf("slot = %v, want %v", slot, want)
	}
	if _, ok := slotTime(now, "not-a-time", loc); ok {
		t.Fatalf("expected an invalid HH:MM to fail")
	}
}

func TestIsDue(t *testing.T) {
	loc := time.UTC
	catchUp := 60 * time.Minute
	base := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before slot", base.Add(-1 * time.Minute), false},
		{"exactly at slot", base, true},
		{"within catch-up", base.Add(59 * time.Minute), true},
		{"exactly at catch-up boundary", base.Add(60 * time.Minute), true},
		{"past catch-up", base.Add(61 * time.Minute), false},
	}
	for _, c := range cases {
		_, due := isDue(c.now, "05:30", loc, catchUp)
		if due != c.want {
			t.Errorf("%s: isDue = %v, want %v", c.name, due, c.want)
		}
	}
}

func TestSlotDateKey(t *testing.T) {
	loc := time.UTC
	got := slotDateKey(time.Date(2026, 9, 13, 5, 30, 0, 0, loc), loc)
	if got != "2026-09-13" {
		t.Fatalf("slotDateKey = %q, want 2026-09-13", got)
	}
}
