package main

import (
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const usageRingRetention = 5 * time.Minute

// sessionHeaderRecordPrefix is what the host's session extraction
// (sdk/cliproxy/session/info.go, "5. OpenCode / Pi Slot / Task / Generic
// Headers" branch) prepends to any X-Session-ID header value before it ends
// up as UsageRecord.SessionID. Verified against CLIProxyAPI v7.2.158 source:
// `info.SessionID = "header:" + sid`. The plugin design notes this plugin was
// built from assumed SessionID would equal the raw header value; it does
// not -- every match against a session tag we sent must account for this
// prefix.
const sessionHeaderRecordPrefix = "header:"

// usageEntry is the subset of pluginapi.UsageRecord that warmup round
// coverage-checking needs.
type usageEntry struct {
	SessionID   string
	AuthID      string
	Model       string
	Provider    string
	RequestedAt time.Time
	ReceivedAt  time.Time
	Failed      bool
	StatusCode  int
}

// usageRing is a lock-protected, time-bounded buffer of recent usage.handle
// callbacks. The host delivers usage.handle for every request it proxies, not
// just this plugin's own warmup traffic, so entries are cheap and pruned
// aggressively on every write.
type usageRing struct {
	mu      sync.Mutex
	entries []usageEntry
}

func newUsageRing() *usageRing { return &usageRing{} }

// record appends one observed usage record. Safe for concurrent use: the host
// calls usage.handle from whatever goroutine served the underlying request,
// so multiple calls can race here.
func (r *usageRing) record(rec pluginapi.UsageRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, usageEntry{
		SessionID:   rec.SessionID,
		AuthID:      rec.AuthID,
		Model:       rec.Model,
		Provider:    rec.Provider,
		RequestedAt: rec.RequestedAt,
		ReceivedAt:  time.Now(),
		Failed:      rec.Failed,
		StatusCode:  rec.Failure.StatusCode,
	})
	r.pruneLocked(time.Now())
}

func (r *usageRing) pruneLocked(now time.Time) {
	cutoff := now.Add(-usageRingRetention)
	kept := r.entries[:0]
	for _, e := range r.entries {
		if e.ReceivedAt.After(cutoff) {
			kept = append(kept, e)
		}
	}
	r.entries = kept
}

// len reports how many entries the ring currently holds; used only by tests.
func (r *usageRing) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// allBySessionTag returns every usage entry recorded under tag (the host's
// "header:"-prefixed form, see sessionHeaderRecordPrefix above), oldest
// first.
//
// A single outbound warmup request (one tag) is not guaranteed to produce
// exactly one usage record: the host can retry the same request against a
// second account after the first attempt fails, and both attempts share the
// same SessionID. Returning only the most recent match would silently drop
// the first account's record.
func (r *usageRing) allBySessionTag(tag string) []usageEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := sessionHeaderRecordPrefix + tag
	var out []usageEntry
	for _, e := range r.entries {
		if e.SessionID == want {
			out = append(out, e)
		}
	}
	return out
}

// byWindow is the degraded fallback used when no usage record carries a
// recognizable session tag (for example, a future host build that no longer
// threads arbitrary X-Session-ID values through to UsageRecord.SessionID): the
// most recent entry for model within [start-slop, end+slop] whose AuthID is
// not already in claimed.
func (r *usageRing) byWindow(model string, start, end time.Time, slop time.Duration, claimed map[string]bool) (usageEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lo := start.Add(-slop)
	hi := end.Add(slop)
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := r.entries[i]
		if !strings.EqualFold(e.Model, model) {
			continue
		}
		if e.RequestedAt.Before(lo) || e.RequestedAt.After(hi) {
			continue
		}
		if claimed[e.AuthID] {
			continue
		}
		return e, true
	}
	return usageEntry{}, false
}
