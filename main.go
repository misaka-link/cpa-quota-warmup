package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "cpa-quota-warmup"
	pluginVersion = "0.1.1"
	logPrefix     = "[cpa-quota-warmup] "
)

func main() { fmt.Println(pluginName, pluginVersion) }

// engineMu guards the single process-wide *engine instance across
// plugin.register / plugin.reconfigure / plugin.shutdown calls.
var engineMu sync.Mutex
var currentEngine *engine

func ensureEngineRunning(cfg pluginConfig) error {
	engineMu.Lock()
	defer engineMu.Unlock()
	if currentEngine != nil {
		currentEngine.settings.Store(&cfg)
		return nil
	}
	path, err := stateFilePath()
	if err != nil {
		return err
	}
	e := newEngine(cfg, newStateStore(path), hostAuthLister{})
	e.start()
	currentEngine = e
	return nil
}

func reconfigureEngine(cfg pluginConfig) error {
	engineMu.Lock()
	e := currentEngine
	engineMu.Unlock()
	if e == nil {
		// No prior register somehow reached us; behave like a first
		// register instead of losing the config.
		return ensureEngineRunning(cfg)
	}
	e.settings.Store(&cfg)
	return nil
}

func shutdownEngine() {
	engineMu.Lock()
	e := currentEngine
	currentEngine = nil
	engineMu.Unlock()
	if e != nil {
		e.stop()
	}
}

func activeEngine() *engine {
	engineMu.Lock()
	defer engineMu.Unlock()
	return currentEngine
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		cfg, err := decodeConfig(raw)
		if err != nil {
			return nil, err
		}
		if err := ensureEngineRunning(cfg); err != nil {
			return nil, err
		}
		return okEnvelope(registrationPayload())
	case pluginabi.MethodPluginReconfigure:
		cfg, err := decodeConfig(raw)
		if err != nil {
			return nil, err
		}
		if err := reconfigureEngine(cfg); err != nil {
			return nil, err
		}
		return okEnvelope(registrationPayload())
	case pluginabi.MethodPluginQuiesce:
		return okEnvelope(struct{}{})
	case pluginabi.MethodPluginShutdown:
		shutdownEngine()
		return okEnvelope(struct{}{})
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &record)
		}
		if e := activeEngine(); e != nil {
			e.ring.record(record)
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagementRequest(raw)
	default:
		return errorEnvelope("unknown_method", "unsupported method"), nil
	}
}

func registrationPayload() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]bool    `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:    pluginName,
			Version: pluginVersion,
			Author:  "Scottio",
			// Required by CPA. This points to the host SDK, not a published plugin repository.
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "When false, the scheduler tick is a no-op every 30s instead of warming anything up."},
				{Name: "priority", Type: pluginapi.ConfigFieldTypeInteger, Description: "Plugin load priority. Only affects load order relative to other plugins."},
				{Name: "log", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Send one host.log line per warmup attempt and per skip/warn condition."},
				{Name: "timezone", Type: pluginapi.ConfigFieldTypeString, Description: "IANA timezone the default: and auths[].times HH:MM schedules are interpreted in. Defaults to Asia/Shanghai."},
				{Name: "base-url", Type: pluginapi.ConfigFieldTypeString, Description: "This CPA instance's own base URL, used to send warmup chat completions to itself."},
				{Name: "api-key", Type: pluginapi.ConfigFieldTypeString, Description: "Bearer key used to authenticate the plugin's own warmup requests. Empty reads api-keys[0] from the host's own config.yaml in the current working directory."},
				{Name: "message", Type: pluginapi.ConfigFieldTypeString, Description: "The user message body sent in every warmup request. Keep it minimal."},
				{Name: "max-tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "max_tokens on every warmup request. Default 16."},
				{Name: "max-rounds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum number of send-then-check-usage rounds per due provider group before giving up on any account not yet covered. Default 3."},
				{Name: "catch-up-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "How late a missed slot (e.g. after a service restart) may still fire before being skipped for the day. Default 60."},
				{Name: "default", Type: pluginapi.ConfigFieldTypeObject, Description: "Baseline schedule applied to every auth file before providers[] and auths[] narrow it."},
				{Name: "default.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Whether an auth file is warmed up at all by default. Default true."},
				{Name: "default.times", Type: pluginapi.ConfigFieldTypeArray, Description: "Default list of HH:MM times of day to warm up each auth file at. Default [\"05:30\"]."},
				{Name: "providers", Type: pluginapi.ConfigFieldTypeObject, Description: "Map of provider key (as reported by host.auth.list, e.g. antigravity, codex, kimi, xai, claude, gemini-cli) to its cheapest warmup model."},
				{Name: "providers.<provider>.model", Type: pluginapi.ConfigFieldTypeString, Description: "Model name sent in the warmup request for every auth on this provider, unless an auths[] override replaces it."},
				{Name: "providers.<provider>.reasoning-effort", Type: pluginapi.ConfigFieldTypeString, Description: "Optional reasoning_effort field added to warmup requests for this provider (e.g. \"low\" for gpt-* / codex models)."},
				{Name: "auths", Type: pluginapi.ConfigFieldTypeArray, Description: "Ordered per-auth-file overrides. `match` is a glob against the auth file name (host.auth.list's `name` field); later entries override earlier ones for the same file."},
				{Name: "auths[].match", Type: pluginapi.ConfigFieldTypeString, Description: "Glob pattern (*, ?, []) matched against the auth file name."},
				{Name: "auths[].enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Overrides default.enabled for matching auth files."},
				{Name: "auths[].times", Type: pluginapi.ConfigFieldTypeArray, Description: "Overrides default.times for matching auth files."},
				{Name: "auths[].model", Type: pluginapi.ConfigFieldTypeString, Description: "Overrides the provider's default model for matching auth files."},
				{Name: "auths[].reasoning-effort", Type: pluginapi.ConfigFieldTypeString, Description: "Overrides the provider's default reasoning-effort for matching auth files."},
			},
		},
		Capabilities: map[string]bool{"usage_plugin": true, "management_api": true},
	}
}

func okEnvelope(result any) ([]byte, error) {
	return json.Marshal(struct {
		OK     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, result})
}

func errorEnvelope(code, message string) []byte {
	out, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": message}})
	return out
}
