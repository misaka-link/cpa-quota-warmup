package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestHandleStatusRequestPerAccountModelsFilteredByProvider covers v0.6.2's
// authStatus.Models field: each account's dropdown list must be restricted
// to its own provider (via modelsForProvider's owned_by mapping and
// candidate-list union), not the full available_models list every account
// used to be offered regardless of provider.
func TestHandleStatusRequestPerAccountModelsFilteredByProvider(t *testing.T) {
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-5.3-codex-spark","owned_by":"openai"},
			{"id":"gemini-3.7-flash-high","owned_by":"antigravity"},
			{"id":"claude-sonnet-4-6","owned_by":"antigravity"},
			{"id":"kimi-k2.8","owned_by":"moonshot"}
		]}`))
	}))
	defer modelsSrv.Close()

	e, _ := registerFileMode(t, "advanced:\n  base-url: \""+modelsSrv.URL+"\"\n  api-key: \"sk-test\"\n")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{
		{Name: "a.json", Provider: "codex"},
		{Name: "b.json", Provider: "antigravity"},
	}}

	status, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d body=%s", status, body)
	}
	var payload statusPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var aStatus, bStatus *authStatus
	for i := range payload.Auths {
		switch payload.Auths[i].Name {
		case "a.json":
			aStatus = &payload.Auths[i]
		case "b.json":
			bStatus = &payload.Auths[i]
		}
	}
	if aStatus == nil || bStatus == nil {
		t.Fatalf("expected both a.json and b.json in Auths: %+v", payload.Auths)
	}

	// a.json is codex: only gpt-5.3-codex-spark maps to it (owned_by
	// "openai"); kimi-k2.8 (owned_by "moonshot") must not leak in.
	if len(aStatus.Models) != 1 || aStatus.Models[0] != "gpt-5.3-codex-spark" {
		t.Fatalf("a.json (codex) Models = %v, want [gpt-5.3-codex-spark]", aStatus.Models)
	}

	// b.json is antigravity: both gemini-3.7-flash-high and
	// claude-sonnet-4-6 are in antigravity's own candidate list and present
	// in available, so both are included, in candidates.go's cheapest-first
	// order (gemini-3.7-flash-high precedes claude-sonnet-4-6 there).
	if len(bStatus.Models) != 2 || bStatus.Models[0] != "gemini-3.7-flash-high" || bStatus.Models[1] != "claude-sonnet-4-6" {
		t.Fatalf("b.json (antigravity) Models = %v, want [gemini-3.7-flash-high claude-sonnet-4-6]", bStatus.Models)
	}

	// available_models at the top level must still be the full,
	// un-filtered list (the inline mode's global model editor keeps using
	// it).
	if len(payload.AvailableModels) != 4 {
		t.Fatalf("top-level available_models = %v, want all 4 entries", payload.AvailableModels)
	}
}

// TestHandleStatusRequestModelHintWhenModelNotInProviderList covers the
// non-blocking gray hint: a model explicitly pinned to an account (still
// validated and saved normally via /set, which only checks the global
// available_models list) that does not appear on that account's own
// provider-filtered Models list gets a ModelHint, without being rejected or
// altered.
func TestHandleStatusRequestModelHintWhenModelNotInProviderList(t *testing.T) {
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-5.3-codex-spark","owned_by":"openai"},
			{"id":"grok-3-mini","owned_by":"xai"}
		]}`))
	}))
	defer modelsSrv.Close()

	e, _ := registerFileMode(t, "advanced:\n  base-url: \""+modelsSrv.URL+"\"\n  api-key: \"sk-test\"\n")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "c.json", Provider: "codex"}}}

	setStatus, setBody := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/set", url.Values{
		"auth": {"c.json"}, "enabled": {"true"}, "model": {"grok-3-mini"},
	})
	if setStatus != http.StatusOK {
		t.Fatalf("set: status=%d body=%s", setStatus, setBody)
	}

	status, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d body=%s", status, body)
	}
	var payload statusPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var cStatus *authStatus
	for i := range payload.Auths {
		if payload.Auths[i].Name == "c.json" {
			cStatus = &payload.Auths[i]
		}
	}
	if cStatus == nil {
		t.Fatalf("expected c.json in Auths: %+v", payload.Auths)
	}
	if cStatus.Model != "grok-3-mini" {
		t.Fatalf("c.json Model = %q, want grok-3-mini (saving must not be blocked)", cStatus.Model)
	}
	if cStatus.ModelHint == "" {
		t.Fatalf("expected a ModelHint since grok-3-mini is not in codex's own Models list: %+v", cStatus)
	}
	for _, m := range cStatus.Models {
		if m == "grok-3-mini" {
			t.Fatalf("grok-3-mini should not be in codex's own Models list: %v", cStatus.Models)
		}
	}
}
