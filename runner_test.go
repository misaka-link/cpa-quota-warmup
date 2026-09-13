package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func testConfig(t *testing.T) pluginConfig {
	t.Helper()
	cfg := defaultPluginConfig()
	cfg.Providers = map[string]providerDefault{
		"antigravity": {Model: "gemini-3.1-flash-lite"},
		"codex":       {Model: "gpt-5.6-luna", ReasoningEffort: "low"},
	}
	return cfg
}

func testStateStore(t *testing.T) *stateStore {
	t.Helper()
	return newStateStore(filepath.Join(t.TempDir(), "quota-warmup", "state.json"))
}

func TestGroupDueTargetsSkipsDisabledAndUnavailable(t *testing.T) {
	cfg := testConfig(t)
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 5, 30, 0, 0, cfg.location)
	entries := []pluginapi.HostAuthFileEntry{
		{Name: "a.json", Provider: "antigravity", Disabled: true},
		{Name: "b.json", Provider: "antigravity", Unavailable: true},
		{Name: "c.json", Provider: "antigravity"},
	}
	groups, _ := groupDueTargets(entries, cfg, now, time.Hour, state)
	targets := groups["antigravity"]
	if len(targets) != 1 || targets[0].Name != "c.json" {
		t.Fatalf("expected only c.json to be due, got %+v", targets)
	}
}

func TestGroupDueTargetsSkipsUnmappedProviderWithWarning(t *testing.T) {
	cfg := testConfig(t)
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 5, 30, 0, 0, cfg.location)
	entries := []pluginapi.HostAuthFileEntry{
		{Name: "weird.json", Provider: "some-unmapped-provider"},
	}
	groups, warnings := groupDueTargets(entries, cfg, now, time.Hour, state)
	if len(groups) != 0 {
		t.Fatalf("expected no groups, got %+v", groups)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one no-model warning, got %v", warnings)
	}
}

func TestGroupDueTargetsRespectsTimeOfDay(t *testing.T) {
	cfg := testConfig(t)
	state := testStateStore(t)
	entries := []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "antigravity"}}

	before := time.Date(2026, 9, 13, 5, 0, 0, 0, cfg.location)
	groups, _ := groupDueTargets(entries, cfg, before, time.Hour, state)
	if len(groups) != 0 {
		t.Fatalf("expected nothing due before the slot, got %+v", groups)
	}

	due := time.Date(2026, 9, 13, 5, 30, 0, 0, cfg.location)
	groups, _ = groupDueTargets(entries, cfg, due, time.Hour, state)
	if len(groups["antigravity"]) != 1 {
		t.Fatalf("expected a.json to be due at the slot, got %+v", groups)
	}
}

func TestGroupDueTargetsSkipsAlreadyRecordedSlot(t *testing.T) {
	cfg := testConfig(t)
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 5, 30, 0, 0, cfg.location)
	entries := []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "antigravity"}}

	if err := state.record(slotRecord{Auth: "a.json", Date: "2026-09-13", Time: "05:30", Covered: true}, now, cfg.location); err != nil {
		t.Fatalf("record: %v", err)
	}
	groups, _ := groupDueTargets(entries, cfg, now, time.Hour, state)
	if len(groups) != 0 {
		t.Fatalf("expected the already-recorded slot to be skipped, got %+v", groups)
	}
}

func TestFilterUnavailableModelsSplitsByAvailability(t *testing.T) {
	groups := map[string][]dueTarget{
		"antigravity": {
			{Name: "a.json", Model: "gemini-3.7-flash-high"},
			{Name: "b.json", Model: "gemini-3.1-flash-lite"}, // stale/404 model
		},
		"kimi": {
			{Name: "c.json", Model: "kimi-k2.8"},
		},
	}
	available := map[string]bool{
		"gemini-3.7-flash-high": true,
		"kimi-k2.8":             true,
	}

	sendable, unavailable := filterUnavailableModels(groups, available)

	if len(sendable["antigravity"]) != 1 || sendable["antigravity"][0].Name != "a.json" {
		t.Fatalf("sendable[antigravity] = %+v, want only a.json", sendable["antigravity"])
	}
	if len(sendable["kimi"]) != 1 || sendable["kimi"][0].Name != "c.json" {
		t.Fatalf("sendable[kimi] = %+v, want c.json", sendable["kimi"])
	}
	if len(unavailable) != 1 || unavailable[0].Name != "b.json" {
		t.Fatalf("unavailable = %+v, want just b.json", unavailable)
	}
}

func TestFilterUnavailableModelsEmptyAvailableExcludesEverything(t *testing.T) {
	groups := map[string][]dueTarget{"antigravity": {{Name: "a.json", Model: "m"}}}
	sendable, unavailable := filterUnavailableModels(groups, map[string]bool{})
	if len(sendable) != 0 {
		t.Fatalf("expected nothing sendable against an empty available set, got %+v", sendable)
	}
	if len(unavailable) != 1 || unavailable[0].Name != "a.json" {
		t.Fatalf("unavailable = %+v, want a.json", unavailable)
	}
}

// fakeSender simulates the host's account selection: assign decides, for the
// call at position idx (0-based across every round), which AuthID (if any)
// the request lands on and what usage.handle would report for it.
type fakeSender struct {
	ring   *usageRing
	assign func(idx int, req warmupSendRequest) (authID string, statusCode int, ok bool)
	calls  int
}

func (f *fakeSender) sendWarmup(_ context.Context, req warmupSendRequest) warmupSendResult {
	idx := f.calls
	f.calls++
	authID, status, ok := f.assign(idx, req)
	if !ok {
		return warmupSendResult{StatusCode: 200}
	}
	f.ring.record(pluginapi.UsageRecord{
		SessionID:   sessionHeaderRecordPrefix + req.SessionTag,
		AuthID:      authID,
		Model:       req.Model,
		RequestedAt: time.Now(),
		Failure:     pluginapi.UsageFailure{StatusCode: status},
	})
	return warmupSendResult{StatusCode: status}
}

func targetsFor(names ...string) []dueTarget {
	out := make([]dueTarget, len(names))
	for i, n := range names {
		out[i] = dueTarget{Name: n, AuthID: "auth-" + n, Provider: "antigravity", Model: "m", HHMM: "05:30"}
	}
	return out
}

func TestRunProviderGroupCoversEveryoneInOneRound(t *testing.T) {
	ring := newUsageRing()
	targets := targetsFor("a", "b", "c")
	sender := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
		// Perfect round-robin: the i-th request lands on the i-th account.
		return targets[idx].AuthID, 200, true
	}}

	outcomes := runProviderGroup(context.Background(), sender, ring, "hi", 16, 3, targets)
	for _, name := range []string{"a", "b", "c"} {
		o, ok := outcomes[name]
		if !ok || !o.Covered || o.Rounds != 1 || o.StatusCode != 200 {
			t.Fatalf("outcome[%s] = %+v (present=%v), want covered in round 1", name, o, ok)
		}
	}
	if sender.calls != 3 {
		t.Fatalf("expected exactly 3 requests for perfect round-robin coverage, got %d", sender.calls)
	}
}

func TestRunProviderGroupRetriesUncoveredAccountsNextRound(t *testing.T) {
	ring := newUsageRing()
	targets := targetsFor("a", "b", "c")
	// Round 1 (3 calls, idx 0,1,2): lands on a, b, a -- c never covered.
	// Round 2 (1 call, idx 3): lands on c.
	sender := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
		switch idx {
		case 0:
			return "auth-a", 200, true
		case 1:
			return "auth-b", 200, true
		case 2:
			return "auth-a", 200, true
		default:
			return "auth-c", 200, true
		}
	}}

	outcomes := runProviderGroup(context.Background(), sender, ring, "hi", 16, 3, targets)
	if o := outcomes["a"]; !o.Covered || o.Rounds != 1 {
		t.Fatalf("outcome[a] = %+v, want covered round 1", o)
	}
	if o := outcomes["b"]; !o.Covered || o.Rounds != 1 {
		t.Fatalf("outcome[b] = %+v, want covered round 1", o)
	}
	if o := outcomes["c"]; !o.Covered || o.Rounds != 2 {
		t.Fatalf("outcome[c] = %+v, want covered round 2", o)
	}
	if sender.calls != 4 {
		t.Fatalf("expected 3 (round 1) + 1 (round 2) = 4 requests, got %d", sender.calls)
	}
}

// TestRunProviderGroupCoversBothAccountsFromASingleRetriedRequest reproduces
// a production incident: one codex warmup request hit the szxypy team
// account first (usage_limit_reached, 429), and the host retried the exact
// same client request against the shao account (200) -- both usage.handle
// records carried the same SessionID. The old bySessionTag/waitFor logic
// returned only the most recent match per tag and retired the tag on its
// first hit, so it saw only the 200 on shao and never noticed szxypy had
// been touched at all, then wasted two more rounds re-sending to an account
// that was already in a 429 cooldown before giving up and reporting szxypy
// as "not covered" even though it plainly had been.
func TestRunProviderGroupCoversBothAccountsFromASingleRetriedRequest(t *testing.T) {
	ring := newUsageRing()
	targets := targetsFor("szxypy", "shao")
	sender := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
		if idx != 0 {
			// The second outbound request in this round is not needed for
			// coverage, but the round loop always fires len(pending) of
			// them up front, before it knows the first will cover everyone.
			return "", 0, false
		}
		// The single client request tagged idx 0 produced two usage.handle
		// callbacks under the same SessionID: a failed attempt on szxypy,
		// then a successful retry on shao.
		ring.record(pluginapi.UsageRecord{
			SessionID:   sessionHeaderRecordPrefix + req.SessionTag,
			AuthID:      "auth-szxypy",
			Model:       req.Model,
			RequestedAt: time.Now(),
			Failed:      true,
			Failure:     pluginapi.UsageFailure{StatusCode: 429},
		})
		return "auth-shao", 200, true
	}}

	outcomes := runProviderGroup(context.Background(), sender, ring, "hi", 16, 3, targets)
	if o := outcomes["szxypy"]; !o.Covered || o.StatusCode != 429 || o.Rounds != 1 {
		t.Fatalf("outcome[szxypy] = %+v, want covered in round 1 with the 429 status recorded", o)
	}
	if o := outcomes["shao"]; !o.Covered || o.StatusCode != 200 || o.Rounds != 1 {
		t.Fatalf("outcome[shao] = %+v, want covered in round 1 with the 200 status", o)
	}
	if sender.calls != 2 {
		t.Fatalf("expected exactly 2 requests (both accounts already covered after round 1), got %d", sender.calls)
	}
}

func TestRunProviderGroupWarnsAfterMaxRounds(t *testing.T) {
	ring := newUsageRing()
	targets := targetsFor("a", "b")
	// Every request, no matter which target sent it, lands on auth-a.
	sender := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
		return "auth-a", 200, true
	}}

	outcomes := runProviderGroup(context.Background(), sender, ring, "hi", 16, 3, targets)
	if o := outcomes["a"]; !o.Covered || o.Rounds != 1 {
		t.Fatalf("outcome[a] = %+v, want covered round 1", o)
	}
	o := outcomes["b"]
	if o.Covered {
		t.Fatalf("outcome[b] = %+v, want uncovered after max-rounds", o)
	}
	if o.Rounds != 3 {
		t.Fatalf("outcome[b].Rounds = %d, want 3 (max-rounds exhausted)", o.Rounds)
	}
	if o.Warning == "" {
		t.Fatalf("expected a warning message for the uncovered account")
	}
}

func TestRunProviderGroupFallsBackToWindowMatchWithoutSessionTag(t *testing.T) {
	ring := newUsageRing()
	targets := targetsFor("a", "b")
	// Simulate a host that does not preserve the session tag: usage records
	// show up with an unrelated SessionID, so allBySessionTag can never
	// match, forcing the byWindow(model, time) fallback.
	sender := &fakeSender{ring: ring, assign: func(idx int, req warmupSendRequest) (string, int, bool) {
		ring.record(pluginapi.UsageRecord{
			SessionID:   "header:unrelated-session",
			AuthID:      targets[idx].AuthID,
			Model:       req.Model,
			RequestedAt: time.Now(),
		})
		return "", 0, false // do not also record via the normal tagged path
	}}

	outcomes := runProviderGroup(context.Background(), sender, ring, "hi", 16, 3, targets)
	for _, name := range []string{"a", "b"} {
		o, ok := outcomes[name]
		if !ok || !o.Covered {
			t.Fatalf("outcome[%s] = %+v (present=%v), want covered via the window fallback", name, o, ok)
		}
	}
}
