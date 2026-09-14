package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// registerNewFormat registers yamlDoc (must not contain any legacy-only key)
// through the real handleMethod dispatch and returns the resulting engine,
// with cleanup wired to leave no engine running for the next test.
func registerNewFormat(t *testing.T, yamlDoc string) *engine {
	t.Helper()
	t.Cleanup(shutdownEngine)
	shutdownEngine()
	raw, err := json.Marshal(struct {
		ConfigYAML []byte `json:"config_yaml"`
	}{ConfigYAML: []byte(yamlDoc)})
	if err != nil {
		t.Fatalf("marshal lifecycle request: %v", err)
	}
	if _, err := handleMethod(pluginabi.MethodPluginRegister, raw); err != nil {
		t.Fatalf("register: %v", err)
	}
	e := activeEngine()
	if e == nil {
		t.Fatalf("expected an engine to be running after register")
	}
	// ensureEngineRunning resolves overrides.json/state.json relative to the
	// real process cwd (shared across every test in this binary, since it is
	// not test-scoped), so swap in temp-dir-backed stores here for isolation
	// -- otherwise a panel override saved by one test would leak into the
	// next one's assertions.
	e.overrides = newOverridesStore(filepath.Join(t.TempDir(), "overrides.json"))
	e.state = newStateStore(filepath.Join(t.TempDir(), "state.json"))
	return e
}

// callManagement drives handleMethod's management.handle path exactly as
// the real C ABI does, decoding the ManagementResponse envelope.
func callManagement(t *testing.T, path string, query url.Values) (int, []byte) {
	t.Helper()
	req := pluginapi.ManagementRequest{Method: "GET", Path: path, Query: query}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := handleMethod(pluginabi.MethodManagementHandle, raw)
	if err != nil {
		t.Fatalf("management.handle: %v", err)
	}
	var envelope struct {
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return envelope.Result.StatusCode, envelope.Result.Body
}

func TestHandleSetRequestGlobalAndAuthOverrides(t *testing.T) {
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-luna","owned_by":"codex"},{"id":"kimi-k2.8","owned_by":"kimi"}]}`))
	}))
	defer modelsSrv.Close()

	yamlDoc := "time: \"05:30\"\naccounts: [\"*\"]\nadvanced:\n  base-url: \"" + modelsSrv.URL + "\"\n  api-key: \"sk-test\"\n"
	e := registerNewFormat(t, yamlDoc)
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	const setPath = "/v0/resource/plugins/cpa-quota-warmup/set"

	if status, body := callManagement(t, setPath, url.Values{"scope": {"bogus"}, "model": {"x"}}); status != http.StatusBadRequest {
		t.Fatalf("invalid scope: status=%d body=%s", status, body)
	}
	if status, body := callManagement(t, setPath, url.Values{"scope": {"auth"}, "model": {"x"}}); status != http.StatusBadRequest {
		t.Fatalf("missing auth: status=%d body=%s", status, body)
	}
	if status, body := callManagement(t, setPath, url.Values{"scope": {"global"}, "model": {"no-such-model"}}); status != http.StatusBadRequest {
		t.Fatalf("unknown model: status=%d body=%s", status, body)
	}

	status, body := callManagement(t, setPath, url.Values{"scope": {"global"}, "model": {"kimi-k2.8"}})
	if status != http.StatusOK {
		t.Fatalf("valid global set: status=%d body=%s", status, body)
	}
	if got := e.overridesSnapshot().Global; got != "kimi-k2.8" {
		t.Fatalf("Global override = %q, want kimi-k2.8", got)
	}

	status, body = callManagement(t, setPath, url.Values{"scope": {"auth"}, "auth": {"a.json"}, "model": {"gpt-5.6-luna"}})
	if status != http.StatusOK {
		t.Fatalf("valid auth set: status=%d body=%s", status, body)
	}
	if got := e.overridesSnapshot().Auths["a.json"]; got != "gpt-5.6-luna" {
		t.Fatalf("Auths[a.json] = %q, want gpt-5.6-luna", got)
	}

	status, body = callManagement(t, setPath, url.Values{"scope": {"auth"}, "auth": {"a.json"}, "model": {"auto"}})
	if status != http.StatusOK {
		t.Fatalf("clear: status=%d body=%s", status, body)
	}
	if _, ok := e.overridesSnapshot().Auths["a.json"]; ok {
		t.Fatalf("expected the auth override to be cleared")
	}
}

func TestHandleStatusRequestNewFormatShowsTimeAccountsAndModelSource(t *testing.T) {
	e := registerNewFormat(t, "time: \"05:30\"\naccounts: [\"a.json\"]\nmodel: \"gpt-5.6-luna\"\n")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{
		{Name: "a.json", Provider: "codex"},
		{Name: "b.json", Provider: "codex"},
	}}

	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d body=%s", status, body)
	}
	var payload statusPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}

	if len(payload.Config.Time) != 1 || payload.Config.Time[0] != "05:30" {
		t.Fatalf("Config.Time = %v", payload.Config.Time)
	}
	if len(payload.Config.Accounts) != 1 || payload.Config.Accounts[0] != "a.json" {
		t.Fatalf("Config.Accounts = %v", payload.Config.Accounts)
	}
	if payload.Config.Model != "gpt-5.6-luna" {
		t.Fatalf("Config.Model = %q, want gpt-5.6-luna", payload.Config.Model)
	}
	if !payload.Config.TimezoneAuto {
		t.Fatalf("expected TimezoneAuto=true when advanced.timezone is unset")
	}
	if payload.AvailableModels == nil {
		t.Fatalf("expected available_models to be a non-nil (possibly empty) array")
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
	if aStatus == nil || !aStatus.Enabled || aStatus.Model != "gpt-5.6-luna" || aStatus.ModelSource != modelSourceGlobal {
		t.Fatalf("a.json status = %+v", aStatus)
	}
	if bStatus == nil || bStatus.Enabled || bStatus.Skipped == "" {
		t.Fatalf("b.json (not in accounts[]) status = %+v, want Skipped set", bStatus)
	}
}

func TestNextTriggerForCronPicksEarliestAcrossExpressions(t *testing.T) {
	loc := time.UTC
	state := testStateStore(t)
	now := time.Date(2026, 9, 13, 4, 0, 0, 0, loc)
	exprs, invalid := parseTimeExprs([]string{"10:35", "05:30"})
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid exprs: %v", invalid)
	}
	next := nextTriggerForCron(exprs, now, loc, time.Hour, state, "acct.json")
	want := time.Date(2026, 9, 13, 5, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}
