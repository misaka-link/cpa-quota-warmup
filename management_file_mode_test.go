package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// registerFileMode registers a v0.4.0 file-mode config pointed at an
// absolute, test-scoped config-file path (so it never touches the real
// process cwd), and swaps in temp-dir-backed state/overrides stores for the
// same isolation reason registerNewFormat does.
func registerFileMode(t *testing.T, extraYAML string) (*engine, string) {
	t.Helper()
	t.Cleanup(shutdownEngine)
	shutdownEngine()
	warmupPath := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	yamlDoc := "enabled: true\nconfig-file: \"" + filepath.ToSlash(warmupPath) + "\"\n" + extraYAML
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
	e.state = newStateStore(filepath.Join(t.TempDir(), "state.json"))
	return e, warmupPath
}

func TestFileModeStatusShowsModeAndConfigFile(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	status, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d body=%s", status, body)
	}
	var payload statusPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	if payload.Config.Mode != "file" {
		t.Fatalf("Config.Mode = %q, want file", payload.Config.Mode)
	}
	if payload.Config.ConfigFile != warmupPath {
		t.Fatalf("Config.ConfigFile = %q, want %q", payload.Config.ConfigFile, warmupPath)
	}
	if payload.Config.ConfigFileError != "" {
		t.Fatalf("unexpected ConfigFileError: %s", payload.Config.ConfigFileError)
	}
	if _, err := os.Stat(warmupPath); err != nil {
		t.Fatalf("expected the status call's underlying engine to have generated %s: %v", warmupPath, err)
	}

	var aStatus *authStatus
	for i := range payload.Auths {
		if payload.Auths[i].Name == "a.json" {
			aStatus = &payload.Auths[i]
		}
	}
	if aStatus == nil {
		t.Fatalf("expected a.json in the auths list: %+v", payload.Auths)
	}
	if aStatus.Enabled {
		t.Fatalf("expected a freshly generated account to be disabled by default: %+v", aStatus)
	}
	if aStatus.Skipped == "" {
		t.Fatalf("expected a Skipped reason for a disabled file-mode account: %+v", aStatus)
	}
}

func TestFileModeSetRouteEnablesAndSchedulesAccount(t *testing.T) {
	e, _ := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	// Ensure the file exists (mirrors what a real tick/status call would do).
	if _, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil); false {
		t.Log(string(body))
	}

	setPath := "/v0/management/plugins/cpa-quota-warmup/set"
	status, body := callManagement(t, setPath, url.Values{"auth": {"a.json"}, "enabled": {"true"}, "time": {"10:30"}})
	if status != http.StatusOK {
		t.Fatalf("set: status=%d body=%s", status, body)
	}

	status, body = callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
	if status != http.StatusOK {
		t.Fatalf("status: %d body=%s", status, body)
	}
	var payload statusPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var aStatus *authStatus
	for i := range payload.Auths {
		if payload.Auths[i].Name == "a.json" {
			aStatus = &payload.Auths[i]
		}
	}
	if aStatus == nil || !aStatus.Enabled {
		t.Fatalf("expected a.json to now be enabled: %+v", aStatus)
	}
	if len(aStatus.Times) != 1 || aStatus.Times[0] != "10:30" {
		t.Fatalf("expected the new time 10:30 to be reflected: %+v", aStatus)
	}

	// Bad enabled value is rejected.
	status, _ = callManagement(t, setPath, url.Values{"auth": {"a.json"}, "enabled": {"not-a-bool"}})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid enabled value, got %d", status)
	}
	// Missing auth is rejected.
	status, _ = callManagement(t, setPath, url.Values{"model": {"x"}})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing auth, got %d", status)
	}
	// No fields at all is rejected.
	status, _ = callManagement(t, setPath, url.Values{"auth": {"a.json"}})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 when nothing is being updated, got %d", status)
	}
}

func TestFileModeSetRouteValidatesModelAgainstAvailableList(t *testing.T) {
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-luna","owned_by":"codex"}]}`))
	}))
	defer modelsSrv.Close()

	e, _ := registerFileMode(t, "advanced:\n  base-url: \""+modelsSrv.URL+"\"\n  api-key: \"sk-test\"\n")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	setPath := "/v0/management/plugins/cpa-quota-warmup/set"
	status, body := callManagement(t, setPath, url.Values{"auth": {"a.json"}, "model": {"no-such-model"}})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unavailable model, status=%d body=%s", status, body)
	}
	status, body = callManagement(t, setPath, url.Values{"auth": {"a.json"}, "model": {"gpt-5.6-luna"}})
	if status != http.StatusOK {
		t.Fatalf("expected 200 for an available model, status=%d body=%s", status, body)
	}
}

func TestInlineModesReportModeInlineAndSetIsGated(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		e := registerNewFormat(t, "timezone: UTC\nproviders:\n  antigravity: { model: m }\n")
		e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "antigravity"}}}
		status, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
		if status != http.StatusOK {
			t.Fatalf("status: %d body=%s", status, body)
		}
		var payload statusPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Config.Mode != "inline" || !payload.Config.LegacyMode {
			t.Fatalf("Config = %+v, want mode=inline legacy_mode=true", payload.Config)
		}

		setStatus, setBody := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/set", url.Values{"scope": {"global"}, "model": {"x"}})
		if setStatus != http.StatusNotImplemented {
			t.Fatalf("expected 501 for /set under legacy mode, got %d body=%s", setStatus, setBody)
		}
	})

	t.Run("v3inline", func(t *testing.T) {
		e := registerNewFormat(t, "time: \"05:30\"\naccounts: [\"*\"]\n")
		e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}
		status, body := callManagement(t, "/v0/management/plugins/cpa-quota-warmup/status", nil)
		if status != http.StatusOK {
			t.Fatalf("status: %d body=%s", status, body)
		}
		var payload statusPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Config.Mode != "inline" || payload.Config.LegacyMode {
			t.Fatalf("Config = %+v, want mode=inline legacy_mode=false", payload.Config)
		}
	})
}
