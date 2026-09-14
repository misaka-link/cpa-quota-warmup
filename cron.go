package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a parsed standard 5-field cron expression (minute hour
// dom month dow). It supports the forms the coordinator asked for: "*",
// "*/n", "a", "a,b,c", "a-b", and "a-b/n", combined across fields with plain
// AND logic (all five fields must match). This is a deliberate, documented
// simplification of full POSIX vixie-cron semantics, which OR the
// day-of-month and day-of-week fields together when both are restricted;
// that quirk is not needed for this plugin's use case ("every day at these
// times" / "every N hours") and AND logic is simpler to reason about and
// test.
type cronExpr struct {
	raw    string
	minute [60]bool
	hour   [24]bool
	dom    [32]bool // index 1..31 used
	month  [13]bool // index 1..12 used
	dow    [7]bool  // index 0..6 used, 0 = Sunday
}

// hhmmPattern matches the plugin's own "HH:MM" shorthand (1-2 digit hour, no
// leading-zero requirement, so both "5:30" and "05:30" are accepted).
var hhmmPattern = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})$`)

// parseTimeExpr parses one `time:`/account-override entry, accepting either
// the "HH:MM" shorthand (converted to an equivalent once-a-day cron
// expression) or a standard 5-field cron string.
func parseTimeExpr(raw string) (*cronExpr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty time expression")
	}
	if m := hhmmPattern.FindStringSubmatch(raw); m != nil {
		hour, err1 := strconv.Atoi(m[1])
		minute, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || hour > 23 || minute > 59 {
			return nil, fmt.Errorf("invalid HH:MM time %q", raw)
		}
		return &cronExpr{
			raw:    raw,
			minute: boolArray60(minute),
			hour:   boolArray24(hour),
			dom:    wildcard32(),
			month:  wildcard13(),
			dow:    wildcard7(),
		}, nil
	}
	return parseCronExpr(raw)
}

// parseCronExpr parses a standard 5-field cron string ("min hour dom month
// dow").
func parseCronExpr(raw string) (*cronExpr, error) {
	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression %q must have exactly 5 fields (min hour dom month dow), got %d", raw, len(fields))
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	dom, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	dow, err := parseCronField(fields[4], 0, 7) // 0 and 7 both mean Sunday
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}

	c := &cronExpr{raw: raw}
	copy(c.minute[:], minute)
	copy(c.hour[:], hour)
	copy(c.dom[:], dom)
	copy(c.month[:], month)
	for i, v := range dow {
		if v {
			c.dow[i%7] = true // fold 7 -> 0 (Sunday)
		}
	}
	return c, nil
}

// parseCronField parses one comma-separated cron field (each segment being
// "*", "*/n", "a", "a-b", or "a-b/n") into an allowed-value boolean slice
// indexed 0..max (index 0 unused when min>0, but kept for simple indexing).
func parseCronField(field string, min, max int) ([]bool, error) {
	allowed := make([]bool, max+1)
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty segment in %q", field)
		}
		step := 1
		rangePart := part
		if idx := strings.Index(part, "/"); idx >= 0 {
			rangePart = part[:idx]
			s, err := strconv.Atoi(part[idx+1:])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			step = s
		}
		lo, hi := min, max
		switch {
		case rangePart == "*":
			// full range, already set above
		case strings.Contains(rangePart, "-"):
			dash := strings.Index(rangePart, "-")
			a, err1 := strconv.Atoi(rangePart[:dash])
			b, err2 := strconv.Atoi(rangePart[dash+1:])
			if err1 != nil || err2 != nil || a > b {
				return nil, fmt.Errorf("invalid range %q", rangePart)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(rangePart)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", rangePart)
			}
			lo, hi = v, v
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("value out of range [%d,%d] in %q", min, max, part)
		}
		for v := lo; v <= hi; v += step {
			allowed[v] = true
		}
	}
	return allowed, nil
}

func boolArray60(i int) (out [60]bool) { out[i] = true; return }
func boolArray24(i int) (out [24]bool) { out[i] = true; return }
func wildcard32() (out [32]bool) {
	for i := 1; i <= 31; i++ {
		out[i] = true
	}
	return
}
func wildcard13() (out [13]bool) {
	for i := 1; i <= 12; i++ {
		out[i] = true
	}
	return
}
func wildcard7() (out [7]bool) {
	for i := 0; i <= 6; i++ {
		out[i] = true
	}
	return
}

// matches reports whether t (evaluated at minute granularity) satisfies
// every field of c.
func (c *cronExpr) matches(t time.Time) bool {
	return c.minute[t.Minute()] && c.hour[t.Hour()] && c.dom[t.Day()] && c.month[int(t.Month())] && c.dow[int(t.Weekday())]
}

// maxCronLookaround bounds the brute-force minute-by-minute search in
// nextTrigger/lastTriggerAtOrBefore below so a pathological expression (e.g.
// one that can only ever match Feb 29) cannot spin forever. 366 days at
// minute granularity is enough to guarantee finding a match if one exists
// within a year, and is fast enough for on-demand status calls (a simple
// daily/hourly cron typically resolves within at most a day of iterations).
const maxCronLookaround = 366 * 24 * time.Hour

// nextTrigger returns the earliest minute strictly after 'after' (in loc)
// that matches c, searching up to maxCronLookaround ahead. ok=false if none
// is found in that window (a pathological or contradictory expression).
func (c *cronExpr) nextTrigger(after time.Time, loc *time.Location) (time.Time, bool) {
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := after.Add(maxCronLookaround)
	for !t.After(deadline) {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// parseTimeExprs parses every entry of raw, splitting successes from
// failures. Failures are reported back (as their original raw string) so
// the caller can hostLog-warn and surface a per-account status warning
// instead of failing the whole plugin.register/reconfigure call over one
// typo'd cron expression.
func parseTimeExprs(raw []string) (valid []*cronExpr, invalid []string) {
	for _, r := range raw {
		expr, err := parseTimeExpr(r)
		if err != nil {
			invalid = append(invalid, r)
			continue
		}
		valid = append(valid, expr)
	}
	return valid, invalid
}

// lastTriggerAtOrBefore returns the latest minute <= now (in loc) that
// matches c, searching back up to maxLookback. ok=false if none is found in
// that window.
func (c *cronExpr) lastTriggerAtOrBefore(now time.Time, loc *time.Location, maxLookback time.Duration) (time.Time, bool) {
	t := now.In(loc).Truncate(time.Minute)
	earliest := now.Add(-maxLookback)
	for !t.Before(earliest) {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}, false
}
