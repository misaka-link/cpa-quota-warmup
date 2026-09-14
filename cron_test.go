package main

import (
	"testing"
	"time"
)

func TestParseTimeExprHHMM(t *testing.T) {
	c, err := parseTimeExpr("05:30")
	if err != nil {
		t.Fatalf("parseTimeExpr(05:30): %v", err)
	}
	loc := time.UTC
	got := c.matches(time.Date(2026, 9, 13, 5, 30, 0, 0, loc))
	if !got {
		t.Fatalf("expected 05:30 cron to match 05:30")
	}
	if c.matches(time.Date(2026, 9, 13, 5, 31, 0, 0, loc)) {
		t.Fatalf("expected 05:30 cron to not match 05:31")
	}
}

func TestParseTimeExprInvalidHHMM(t *testing.T) {
	if _, err := parseTimeExpr("25:00"); err == nil {
		t.Fatalf("expected an error for an out-of-range hour")
	}
	if _, err := parseTimeExpr("05:60"); err == nil {
		t.Fatalf("expected an error for an out-of-range minute")
	}
}

func TestParseCronExprWildcard(t *testing.T) {
	c, err := parseTimeExpr("0 */5 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc := time.UTC
	for _, hour := range []int{0, 5, 10, 15, 20} {
		if !c.matches(time.Date(2026, 9, 13, hour, 0, 0, 0, loc)) {
			t.Errorf("expected match at hour %d", hour)
		}
	}
	if c.matches(time.Date(2026, 9, 13, 1, 0, 0, 0, loc)) {
		t.Errorf("expected no match at hour 1")
	}
	if c.matches(time.Date(2026, 9, 13, 5, 1, 0, 0, loc)) {
		t.Errorf("expected no match at minute 1 (only minute 0 is due)")
	}
}

func TestParseCronExprCommaList(t *testing.T) {
	c, err := parseTimeExpr("30 5,10,15,20 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc := time.UTC
	for _, hour := range []int{5, 10, 15, 20} {
		if !c.matches(time.Date(2026, 9, 13, hour, 30, 0, 0, loc)) {
			t.Errorf("expected match at %02d:30", hour)
		}
	}
	if c.matches(time.Date(2026, 9, 13, 6, 30, 0, 0, loc)) {
		t.Errorf("expected no match at 06:30")
	}
}

func TestParseCronExprRange(t *testing.T) {
	c, err := parseTimeExpr("0 9-17 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc := time.UTC
	if !c.matches(time.Date(2026, 9, 13, 9, 0, 0, 0, loc)) {
		t.Errorf("expected match at the range start (9)")
	}
	if !c.matches(time.Date(2026, 9, 13, 17, 0, 0, 0, loc)) {
		t.Errorf("expected match at the range end (17)")
	}
	if c.matches(time.Date(2026, 9, 13, 18, 0, 0, 0, loc)) {
		t.Errorf("expected no match past the range end")
	}
	if c.matches(time.Date(2026, 9, 13, 8, 0, 0, 0, loc)) {
		t.Errorf("expected no match before the range start")
	}
}

func TestParseCronExprWeekday(t *testing.T) {
	// 2026-09-13 is a Sunday.
	c, err := parseTimeExpr("0 8 * * 1-5")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	loc := time.UTC
	sunday := time.Date(2026, 9, 13, 8, 0, 0, 0, loc)
	monday := time.Date(2026, 9, 14, 8, 0, 0, 0, loc)
	if c.matches(sunday) {
		t.Errorf("expected weekday-only cron to not match Sunday")
	}
	if !c.matches(monday) {
		t.Errorf("expected weekday-only cron to match Monday")
	}
}

func TestParseCronExprSundayAliasSevenAndZero(t *testing.T) {
	loc := time.UTC
	sunday := time.Date(2026, 9, 13, 8, 0, 0, 0, loc)
	for _, expr := range []string{"0 8 * * 0", "0 8 * * 7"} {
		c, err := parseTimeExpr(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		if !c.matches(sunday) {
			t.Errorf("expr %q: expected Sunday (dow=0 and dow=7 alias) to match", expr)
		}
	}
}

func TestParseCronExprInvalidFieldCount(t *testing.T) {
	if _, err := parseTimeExpr("* * *"); err == nil {
		t.Fatalf("expected an error for a cron string with the wrong number of fields")
	}
}

func TestParseCronExprInvalidValue(t *testing.T) {
	if _, err := parseTimeExpr("99 * * * *"); err == nil {
		t.Fatalf("expected an error for an out-of-range minute value")
	}
}

func TestLastTriggerAtOrBeforeCatchUpBoundary(t *testing.T) {
	loc := time.UTC
	c, err := parseTimeExpr("05:30")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
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
	for _, tc := range cases {
		trigger, ok := c.lastTriggerAtOrBefore(tc.now, loc, catchUp)
		due := ok && !trigger.After(tc.now) && tc.now.Sub(trigger) <= catchUp
		if due != tc.want {
			t.Errorf("%s: due = %v, want %v (trigger=%v ok=%v)", tc.name, due, tc.want, trigger, ok)
		}
	}
}

func TestNextTriggerPicksSoonestMatch(t *testing.T) {
	loc := time.UTC
	c, err := parseTimeExpr("30 5,10 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)
	next, ok := c.nextTrigger(now, loc)
	if !ok {
		t.Fatalf("expected a next trigger to be found")
	}
	want := time.Date(2026, 9, 13, 10, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

func TestNextTriggerRollsToNextDay(t *testing.T) {
	loc := time.UTC
	c, err := parseTimeExpr("05:30")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc)
	next, ok := c.nextTrigger(now, loc)
	if !ok {
		t.Fatalf("expected a next trigger to be found")
	}
	want := time.Date(2026, 9, 14, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want tomorrow's slot %v", next, want)
	}
}
