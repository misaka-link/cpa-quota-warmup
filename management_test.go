package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestNextTriggerForUpcomingSlot(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, loc)
	next := nextTriggerFor([]string{"05:30"}, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

func TestNextTriggerForDueNowWithinCatchUp(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 6, 0, 0, 0, loc) // 30min after the 05:30 slot
	next := nextTriggerFor([]string{"05:30"}, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want today's slot (due now), got %v", want, next)
	}
}

func TestNextTriggerForMissedRollsToTomorrow(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 7, 0, 0, 0, loc) // 90min after the slot, catch-up is 60min
	next := nextTriggerFor([]string{"05:30"}, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 14, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want tomorrow's slot %v", next, want)
	}
}

func TestNextTriggerForAlreadyRecordedRollsToTomorrow(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 5, 45, 0, 0, loc)
	if err := state.record(slotRecord{Auth: "acct.json", Date: "2026-09-13", Time: "05:30", Covered: true}, now, loc); err != nil {
		t.Fatalf("record: %v", err)
	}
	next := nextTriggerFor([]string{"05:30"}, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 14, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want tomorrow's slot %v (today already recorded)", next, want)
	}
}

func TestNextTriggerForPicksEarliestOfMultipleTimes(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, loc)
	next := nextTriggerFor([]string{"10:35", "05:30"}, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want the earlier 05:30 slot", next)
	}
}

// fakeAuthLister is a fixed host.auth.list stand-in for handleMethod-level
// integration tests.
type fakeAuthLister struct {
	entries []pluginapi.HostAuthFileEntry
	err     error
}

func (f fakeAuthLister) ListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	return f.entries, f.err
}

// TestHandleMethodLifecycle exercises register -> usage.handle ->
// management.handle(status) -> reconfigure -> shutdown at the Go call level
// (the real C ABI surface is covered separately by integration_abi_test.py).
func TestHandleMethodLifecycle(t *testing.T) {
	t.Cleanup(shutdownEngine)
	shutdownEngine() // in case a previous test left an engine running

	yamlDoc := []byte("timezone: UTC\nproviders:\n  antigravity: { model: m }\n")
	registerRaw, _ := json.Marshal(struct {
		ConfigYAML []byte `json:"config_yaml"`
	}{ConfigYAML: yamlDoc})

	resp, err := handleMethod(pluginabi.MethodPluginRegister, registerRaw)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if !envelope.OK || !envelope.Result.Capabilities["usage_plugin"] || !envelope.Result.Capabilities["management_api"] {
		t.Fatalf("unexpected register response: %+v", envelope)
	}

	// Swap in a fake auth lister so status/management calls do not depend on
	// a real cgo host being present.
	e := activeEngine()
	if e == nil {
		t.Fatalf("expected an engine to be running after register")
	}
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "antigravity"}}}

	usageRaw, _ := json.Marshal(pluginapi.UsageRecord{AuthID: "auth-a", SessionID: "header:tag", Model: "m"})
	if _, err := handleMethod(pluginabi.MethodUsageHandle, usageRaw); err != nil {
		t.Fatalf("usage.handle: %v", err)
	}
	if e.ring.len() != 1 {
		t.Fatalf("expected usage.handle to record one entry, got %d", e.ring.len())
	}

	statusReq, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/cpa-quota-warmup/status"})
	statusResp, err := handleMethod(pluginabi.MethodManagementHandle, statusReq)
	if err != nil {
		t.Fatalf("management.handle(status): %v", err)
	}
	var statusEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(statusResp, &statusEnvelope); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !statusEnvelope.OK || statusEnvelope.Result.StatusCode != 200 {
		t.Fatalf("unexpected status response: %+v", statusEnvelope)
	}
	var payload statusPayload
	if err := json.Unmarshal(statusEnvelope.Result.Body, &payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	if len(payload.Auths) != 1 || payload.Auths[0].Name != "a.json" || !payload.Auths[0].Enabled {
		t.Fatalf("unexpected status payload: %+v", payload)
	}

	if _, err := handleMethod(pluginabi.MethodPluginReconfigure, registerRaw); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	if activeEngine() != e {
		t.Fatalf("reconfigure must not replace the running engine")
	}

	if _, err := handleMethod(pluginabi.MethodPluginShutdown, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if activeEngine() != nil {
		t.Fatalf("expected shutdown to clear the active engine")
	}
}
