package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultTimezone       = "Asia/Shanghai"
	defaultBaseURL        = "http://127.0.0.1:8317"
	defaultMessage        = "hi"
	defaultMaxTokens      = 16
	defaultMaxRounds      = 3
	defaultCatchUpMinutes = 60
	defaultTime           = "05:30"
	hostConfigFileName    = "config.yaml"
)

// providerDefault is one entry of the top-level `providers:` map: the
// cheapest model to use for every auth on that provider, unless a per-auth
// override in `auths:` replaces it.
type providerDefault struct {
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoning-effort"`
}

// defaultSchedule is the `default:` block applied to every auth file before
// any `auths:` entry narrows or overrides it.
type defaultSchedule struct {
	Enabled *bool    `yaml:"enabled"`
	Times   []string `yaml:"times"`
}

// authOverride is one entry of the `auths:` list. Match is a glob against
// HostAuthFileEntry.Name; later entries in the list override earlier ones
// for any auth file matched by both.
type authOverride struct {
	Match           string   `yaml:"match"`
	Enabled         *bool    `yaml:"enabled"`
	Times           []string `yaml:"times"`
	Model           string   `yaml:"model"`
	ReasoningEffort string   `yaml:"reasoning-effort"`
}

// pluginConfig is the decoded `plugins.configs.cpa-quota-warmup` block.
type pluginConfig struct {
	Enabled        bool                       `yaml:"enabled"`
	Priority       int                        `yaml:"priority"`
	Log            bool                       `yaml:"log"`
	Timezone       string                     `yaml:"timezone"`
	BaseURL        string                     `yaml:"base-url"`
	APIKey         string                     `yaml:"api-key"`
	Message        string                     `yaml:"message"`
	MaxTokens      int                        `yaml:"max-tokens"`
	MaxRounds      int                        `yaml:"max-rounds"`
	CatchUpMinutes int                        `yaml:"catch-up-minutes"`
	Default        defaultSchedule            `yaml:"default"`
	Providers      map[string]providerDefault `yaml:"providers"`
	Auths          []authOverride             `yaml:"auths"`

	// location is resolved from Timezone at decode time; nil is never
	// returned to callers (decodeConfig fails first).
	location *time.Location
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:        true,
		Priority:       1,
		Log:            true,
		Timezone:       defaultTimezone,
		BaseURL:        defaultBaseURL,
		Message:        defaultMessage,
		MaxTokens:      defaultMaxTokens,
		MaxRounds:      defaultMaxRounds,
		CatchUpMinutes: defaultCatchUpMinutes,
		Default: defaultSchedule{
			Times: []string{defaultTime},
		},
		location: time.UTC,
	}
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// decodeConfig parses raw plugin.register/plugin.reconfigure request bytes
// (the {"config_yaml": <bytes>} envelope) into a fully defaulted pluginConfig.
func decodeConfig(raw []byte) (pluginConfig, error) {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return pluginConfig{}, fmt.Errorf("invalid lifecycle request: %w", err)
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return pluginConfig{}, fmt.Errorf("parse plugin config: %w", err)
		}
	}

	if strings.TrimSpace(cfg.Message) == "" {
		cfg.Message = defaultMessage
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = defaultMaxRounds
	}
	if cfg.CatchUpMinutes <= 0 {
		cfg.CatchUpMinutes = defaultCatchUpMinutes
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if len(cfg.Default.Times) == 0 {
		cfg.Default.Times = []string{defaultTime}
	}

	loc, err := resolveLocation(cfg.Timezone)
	if err != nil {
		return pluginConfig{}, err
	}
	cfg.location = loc
	cfg.Timezone = loc.String()

	return cfg, nil
}

func resolveLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultTimezone
	}
	if strings.EqualFold(name, "utc") {
		return time.UTC, nil
	}
	if strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	return loc, nil
}

// effectiveAuthConfig is the fully-resolved warmup schedule for one auth
// file, after layering `default:`, the provider's entry in `providers:`, and
// every matching `auths:` override (later entries win) on top of each other.
type effectiveAuthConfig struct {
	Enabled         bool
	Times           []string
	Model           string
	ReasoningEffort string
}

// resolveAuthConfig computes the effective schedule for one auth file. The
// bool return is false when the auth is disabled or has no usable model
// (unmapped provider with no per-auth override), in which case the caller
// should skip it (and, for the model case, log a warning).
func resolveAuthConfig(cfg pluginConfig, name, provider string) (effectiveAuthConfig, bool) {
	enabled := true
	if cfg.Default.Enabled != nil {
		enabled = *cfg.Default.Enabled
	}
	times := append([]string(nil), cfg.Default.Times...)

	var model, reasoningEffort string
	if provider != "" {
		if def, ok := cfg.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			model = def.Model
			reasoningEffort = def.ReasoningEffort
		}
	}

	for _, override := range cfg.Auths {
		if !globMatch(override.Match, name) {
			continue
		}
		if override.Enabled != nil {
			enabled = *override.Enabled
		}
		if len(override.Times) > 0 {
			times = append([]string(nil), override.Times...)
		}
		if strings.TrimSpace(override.Model) != "" {
			model = override.Model
		}
		if strings.TrimSpace(override.ReasoningEffort) != "" {
			reasoningEffort = override.ReasoningEffort
		}
	}

	if !enabled {
		return effectiveAuthConfig{}, false
	}
	if strings.TrimSpace(model) == "" {
		return effectiveAuthConfig{}, false
	}
	validTimes := make([]string, 0, len(times))
	seen := make(map[string]bool, len(times))
	for _, t := range times {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if _, err := time.Parse("15:04", t); err != nil {
			continue
		}
		seen[t] = true
		validTimes = append(validTimes, t)
	}
	if len(validTimes) == 0 {
		return effectiveAuthConfig{}, false
	}

	return effectiveAuthConfig{
		Enabled:         true,
		Times:           validTimes,
		Model:           model,
		ReasoningEffort: reasoningEffort,
	}, true
}

// minimalHostConfig is the one key this plugin ever reads from CPA's own
// config.yaml: the inbound API keys it accepts. We must authenticate our own
// warmup requests as a normal client, and the plugin has no other source for
// a valid key.
type minimalHostConfig struct {
	APIKeys []string `yaml:"api-keys"`
}

// resolveAPIKeyFromFile reads api-keys[0] out of a CPA config.yaml at path.
func resolveAPIKeyFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read host config %s: %w", path, err)
	}
	var hostCfg minimalHostConfig
	if err := yaml.Unmarshal(data, &hostCfg); err != nil {
		return "", fmt.Errorf("parse host config %s: %w", path, err)
	}
	if len(hostCfg.APIKeys) == 0 || strings.TrimSpace(hostCfg.APIKeys[0]) == "" {
		return "", fmt.Errorf("host config %s has no api-keys", path)
	}
	return hostCfg.APIKeys[0], nil
}

// resolveAPIKey returns the configured api-key, or falls back to the host's
// own config.yaml (relative to the process working directory, which is
// CPA's cwd) when the plugin config leaves it blank.
func resolveAPIKey(cfg pluginConfig) (string, error) {
	if strings.TrimSpace(cfg.APIKey) != "" {
		return cfg.APIKey, nil
	}
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return resolveAPIKeyFromFile(filepath.Join(workDir, hostConfigFileName))
}

// atoiOrZero parses s as an integer, returning 0 for anything unparsable.
// Used by management.go query-string handling.
func atoiOrZero(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
