package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDecodeConfigDefaults documents the v0.3.0 default: with no config at
// all (no legacy-only key present), decodeConfig produces the new minimal
// format's defaults, not the legacy ones -- timezone auto-follows the host
// process (time.Local), accounts defaults to empty (warm up nothing), and
// model defaults to "auto". See TestDecodeConfigLegacyDefaults below for the
// old-format equivalent of this test.
func TestDecodeConfigDefaults(t *testing.T) {
	cfg, err := decodeConfig(nil)
	if err != nil {
		t.Fatalf("decodeConfig(nil): %v", err)
	}
	if cfg.legacyMode {
		t.Fatalf("expected an empty config to be treated as the new format")
	}
	if !cfg.Enabled || !cfg.Log {
		t.Fatalf("expected enabled+log defaults true, got %+v", cfg)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, defaultBaseURL)
	}
	if cfg.Message != defaultMessage {
		t.Fatalf("Message = %q, want %q", cfg.Message, defaultMessage)
	}
	if cfg.MaxTokens != defaultMaxTokens || cfg.MaxRounds != defaultMaxRounds || cfg.CatchUpMinutes != defaultCatchUpMinutes {
		t.Fatalf("unexpected numeric defaults: %+v", cfg)
	}
	if len(cfg.TimeRaw) != 1 || cfg.TimeRaw[0] != defaultTime {
		t.Fatalf("TimeRaw = %v, want [%s]", cfg.TimeRaw, defaultTime)
	}
	if len(cfg.Accounts) != 0 {
		t.Fatalf("Accounts = %v, want empty (warm up nothing by default)", cfg.Accounts)
	}
	if cfg.Model.Scalar != "" || len(cfg.Model.Map) != 0 {
		t.Fatalf("Model = %+v, want the zero value (auto)", cfg.Model)
	}
	if cfg.location == nil || cfg.location != timeLocalForTest(t) {
		t.Fatalf("location = %v, want time.Local", cfg.location)
	}
}

// TestDecodeConfigLegacyDefaults is the pre-v0.3.0 behavior, preserved
// exactly: any config containing at least one legacy-only top-level key
// (here just `timezone:`) is resolved the old way.
func TestDecodeConfigLegacyDefaults(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte("timezone: \"Asia/Shanghai\"\n"))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if !cfg.legacyMode {
		t.Fatalf("expected a bare `timezone:` key to be detected as legacy")
	}
	if len(cfg.Default.Times) != 1 || cfg.Default.Times[0] != defaultTime {
		t.Fatalf("Default.Times = %v, want [%s]", cfg.Default.Times, defaultTime)
	}
	if cfg.location == nil || cfg.location.String() != "Asia/Shanghai" {
		t.Fatalf("location = %v, want Asia/Shanghai", cfg.location)
	}
}

// timeLocalForTest resolves what decodeConfig's "" timezone sentinel
// resolves to (time.Local), for comparison in TestDecodeConfigDefaults.
func timeLocalForTest(t *testing.T) *time.Location {
	t.Helper()
	return time.Local
}

func TestDecodeConfigFromSpecExample(t *testing.T) {
	yamlDoc := []byte(`
enabled: true
priority: 1
log: true
timezone: "Asia/Shanghai"
base-url: "http://127.0.0.1:8317"
api-key: ""
message: "hi"
max-tokens: 16
max-rounds: 3
catch-up-minutes: 60
default:
  enabled: true
  times: ["05:30"]
providers:
  antigravity: { model: "gemini-3.1-flash-lite" }
  codex:       { model: "gpt-5.6-luna", reasoning-effort: "low" }
  kimi:        { model: "kimi-k2" }
  xai:         { model: "grok-3-mini" }
  claude:      { model: "claude-haiku-4-5-20251001" }
  gemini-cli:  { model: "gemini-2.5-flash-lite" }
  aistudio:    { model: "gemini-2.5-flash-lite" }
  vertex:      { model: "gemini-2.5-flash-lite" }
auths:
  - match: "codex-*-prolite.json"
    enabled: false
  - match: "antigravity-alice@example.com.json"
    times: ["05:30", "10:35"]
    model: "gemini-3.1-flash-lite"
`)
	raw := lifecycleRequestJSON(t, yamlDoc)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if got := cfg.Providers["antigravity"].Model; got != "gemini-3.1-flash-lite" {
		t.Fatalf("antigravity model = %q", got)
	}
	if got := cfg.Providers["codex"].ReasoningEffort; got != "low" {
		t.Fatalf("codex reasoning-effort = %q", got)
	}

	// Plain antigravity auth: provider default model, default schedule.
	eff, ok := resolveAuthConfig(cfg, "antigravity-bob@example.com.json", "antigravity")
	if !ok {
		t.Fatalf("expected antigravity-bob to resolve")
	}
	if eff.Model != "gemini-3.1-flash-lite" || len(eff.Times) != 1 || eff.Times[0] != "05:30" {
		t.Fatalf("unexpected effective config: %+v", eff)
	}

	// Overridden antigravity auth: extra time slot, same model (explicit).
	eff, ok = resolveAuthConfig(cfg, "antigravity-alice@example.com.json", "antigravity")
	if !ok {
		t.Fatalf("expected the overridden antigravity auth to resolve")
	}
	if len(eff.Times) != 2 || eff.Times[0] != "05:30" || eff.Times[1] != "10:35" {
		t.Fatalf("unexpected override times: %v", eff.Times)
	}

	// codex-*-prolite.json is disabled by the override.
	if _, ok := resolveAuthConfig(cfg, "codex-acct-prolite.json", "codex"); ok {
		t.Fatalf("expected codex-acct-prolite.json to be disabled")
	}

	// A regular (non-prolite) codex auth still resolves via the provider default.
	eff, ok = resolveAuthConfig(cfg, "codex-acct-team.json", "codex")
	if !ok {
		t.Fatalf("expected codex-acct-team.json to resolve")
	}
	if eff.Model != "gpt-5.6-luna" || eff.ReasoningEffort != "low" {
		t.Fatalf("unexpected codex effective config: %+v", eff)
	}

	// An unmapped provider with no per-auth override has no model to warm up with.
	if _, ok := resolveAuthConfig(cfg, "weird-provider.json", "some-unmapped-provider"); ok {
		t.Fatalf("expected unmapped provider to be skipped")
	}
}

func TestResolveAuthConfigLaterOverrideWins(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Providers = map[string]providerDefault{"antigravity": {Model: "cheap-1"}}
	cfg.Auths = []authOverride{
		{Match: "acct-*.json", Model: "cheap-2"},
		{Match: "acct-special.json", Model: "cheap-3"},
	}
	eff, ok := resolveAuthConfig(cfg, "acct-special.json", "antigravity")
	if !ok || eff.Model != "cheap-3" {
		t.Fatalf("expected the later, more specific override to win, got %+v ok=%v", eff, ok)
	}
	eff, ok = resolveAuthConfig(cfg, "acct-other.json", "antigravity")
	if !ok || eff.Model != "cheap-2" {
		t.Fatalf("expected the first glob to still apply to a non-special account, got %+v ok=%v", eff, ok)
	}
}

func TestResolveAuthConfigInvalidTimesAreDropped(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Providers = map[string]providerDefault{"antigravity": {Model: "m"}}
	cfg.Default.Times = []string{"05:30", "not-a-time", "05:30", "9:5"}
	eff, ok := resolveAuthConfig(cfg, "acct.json", "antigravity")
	if !ok {
		t.Fatalf("expected resolution to succeed")
	}
	if len(eff.Times) != 1 || eff.Times[0] != "05:30" {
		t.Fatalf("expected invalid/duplicate times to be dropped, got %v", eff.Times)
	}
}

func TestResolveAuthConfigDisabledByDefault(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.Providers = map[string]providerDefault{"antigravity": {Model: "m"}}
	off := false
	cfg.Default.Enabled = &off
	if _, ok := resolveAuthConfig(cfg, "acct.json", "antigravity"); ok {
		t.Fatalf("expected default.enabled=false to skip every auth without an override")
	}
	on := true
	cfg.Auths = []authOverride{{Match: "acct.json", Enabled: &on}}
	if _, ok := resolveAuthConfig(cfg, "acct.json", "antigravity"); !ok {
		t.Fatalf("expected the per-auth override to re-enable the account")
	}
}

func TestResolveAPIKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("api-keys:\n  - sk-first\n  - sk-second\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	key, err := resolveAPIKeyFromFile(path)
	if err != nil {
		t.Fatalf("resolveAPIKeyFromFile: %v", err)
	}
	if key != "sk-first" {
		t.Fatalf("key = %q, want sk-first", key)
	}

	if _, err := resolveAPIKeyFromFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatalf("expected an error reading a missing file")
	}

	empty := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(empty, []byte("enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	if _, err := resolveAPIKeyFromFile(empty); err == nil {
		t.Fatalf("expected an error when api-keys is absent")
	}
}

func TestResolveAPIKeyPrefersConfiguredValue(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.APIKey = "sk-configured"
	key, err := resolveAPIKey(cfg)
	if err != nil || key != "sk-configured" {
		t.Fatalf("resolveAPIKey = (%q, %v), want sk-configured", key, err)
	}
}

// lifecycleRequestJSON builds the {"config_yaml": <bytes>} envelope
// plugin.register/reconfigure requests arrive as.
func lifecycleRequestJSON(t *testing.T, yamlDoc []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		ConfigYAML []byte `json:"config_yaml"`
	}{ConfigYAML: yamlDoc})
	if err != nil {
		t.Fatalf("marshal lifecycle request: %v", err)
	}
	return raw
}
