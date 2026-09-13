package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestUsageRingAllBySessionTagAppliesHeaderPrefix(t *testing.T) {
	r := newUsageRing()
	r.record(pluginapi.UsageRecord{
		SessionID:   "header:cpa-quota-warmup-abc123",
		AuthID:      "auth-1",
		Model:       "gemini-3.1-flash-lite",
		Provider:    "antigravity",
		RequestedAt: time.Now(),
	})

	if entries := r.allBySessionTag("cpa-quota-warmup-abc123"); len(entries) != 1 {
		t.Fatalf("expected the tag to match once the header: prefix is accounted for, got %v", entries)
	}
	// A raw, unprefixed SessionID (as the task spec originally assumed) must
	// NOT be treated as a match -- this pins down the discrepancy against the
	// verified host behavior (sdk/cliproxy/session/info.go: `"header:" + sid`).
	r2 := newUsageRing()
	r2.record(pluginapi.UsageRecord{SessionID: "cpa-quota-warmup-rawvalue", AuthID: "auth-2"})
	if entries := r2.allBySessionTag("cpa-quota-warmup-rawvalue"); len(entries) != 0 {
		t.Fatalf("an unprefixed SessionID must not match: the host always prepends header:, got %v", entries)
	}
}

// TestUsageRingAllBySessionTagReturnsEveryRecord pins down the production bug
// this was fixed for: the host can retry one outbound request (one
// SessionID/tag) against a second account after the first attempt fails, and
// usage.handle fires once per attempt. Both records must come back, not just
// the most recent one.
func TestUsageRingAllBySessionTagReturnsEveryRecord(t *testing.T) {
	r := newUsageRing()
	base := time.Now()
	r.record(pluginapi.UsageRecord{
		SessionID: "header:tag", AuthID: "auth-old", RequestedAt: base,
		Failed: true, Failure: pluginapi.UsageFailure{StatusCode: 429},
	})
	r.record(pluginapi.UsageRecord{SessionID: "header:tag", AuthID: "auth-new", RequestedAt: base.Add(time.Second)})

	entries := r.allBySessionTag("tag")
	if len(entries) != 2 {
		t.Fatalf("expected both records under the shared tag, got %+v", entries)
	}
	if entries[0].AuthID != "auth-old" || entries[0].StatusCode != 429 {
		t.Fatalf("entries[0] = %+v, want the first (failed) attempt", entries[0])
	}
	if entries[1].AuthID != "auth-new" {
		t.Fatalf("entries[1] = %+v, want the second (retried) attempt", entries[1])
	}
}

func TestUsageRingPrunesOldEntries(t *testing.T) {
	r := newUsageRing()
	r.entries = append(r.entries, usageEntry{
		SessionID:  "header:stale",
		AuthID:     "auth-stale",
		ReceivedAt: time.Now().Add(-usageRingRetention - time.Minute),
	})
	r.record(pluginapi.UsageRecord{SessionID: "header:fresh", AuthID: "auth-fresh", RequestedAt: time.Now()})

	if entries := r.allBySessionTag("stale"); len(entries) != 0 {
		t.Fatalf("expected the stale entry to have been pruned, got %v", entries)
	}
	if entries := r.allBySessionTag("fresh"); len(entries) != 1 {
		t.Fatalf("expected the fresh entry to still be present, got %v", entries)
	}
	if r.len() != 1 {
		t.Fatalf("ring len = %d, want 1 after pruning", r.len())
	}
}

func TestUsageRingByWindowFallback(t *testing.T) {
	r := newUsageRing()
	now := time.Now()
	r.record(pluginapi.UsageRecord{SessionID: "header:unrelated", AuthID: "auth-1", Model: "gemini-x", RequestedAt: now})
	r.record(pluginapi.UsageRecord{SessionID: "header:unrelated2", AuthID: "auth-2", Model: "gemini-x", RequestedAt: now.Add(time.Second)})

	claimed := map[string]bool{}
	entry, ok := r.byWindow("gemini-x", now.Add(-time.Second), now.Add(2*time.Second), 500*time.Millisecond, claimed)
	if !ok {
		t.Fatalf("expected a window match")
	}
	claimed[entry.AuthID] = true
	entry2, ok := r.byWindow("gemini-x", now.Add(-time.Second), now.Add(2*time.Second), 500*time.Millisecond, claimed)
	if !ok || entry2.AuthID == entry.AuthID {
		t.Fatalf("expected the second call to return the other, unclaimed auth; got %+v (first was %+v)", entry2, entry)
	}

	if _, ok := r.byWindow("no-such-model", now, now, 0, map[string]bool{}); ok {
		t.Fatalf("expected no match for an unrelated model")
	}
}
