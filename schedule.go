package main

import "time"

const slotDateFormat = "2006-01-02"

// slotTime anchors hhmm ("HH:MM") to now's calendar date in loc, returning
// today's instant for that time-of-day. ok is false when hhmm cannot be
// parsed, which resolveAuthConfig already filters out, but callers loading
// state from disk need to re-validate free-form strings too.
func slotTime(now time.Time, hhmm string, loc *time.Location) (t time.Time, ok bool) {
	parsed, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, false
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc), true
}

// isDue reports whether the slot at hhmm (today, in loc) should fire at now:
// now must have reached the slot, and not by more than catchUp -- past that
// window the slot is considered missed and is left for tomorrow. dueAt is
// returned regardless of due so callers can key state off it either way.
func isDue(now time.Time, hhmm string, loc *time.Location, catchUp time.Duration) (dueAt time.Time, due bool) {
	slot, ok := slotTime(now, hhmm, loc)
	if !ok {
		return time.Time{}, false
	}
	elapsed := now.Sub(slot)
	if elapsed < 0 || elapsed > catchUp {
		return slot, false
	}
	return slot, true
}

// slotDateKey is the calendar-day key (in loc) a slot instant belongs to, used
// together with the auth name and HH:MM to identify one state record.
func slotDateKey(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(slotDateFormat)
}
