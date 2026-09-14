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
	defaultLanguage       = "auto"
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
	Enabled        bool   `yaml:"enabled"`
	Priority       int    `yaml:"priority"`
	Log            bool   `yaml:"log"`
	Timezone       string `yaml:"timezone"`
	BaseURL        string `yaml:"base-url"`
	APIKey         string `yaml:"api-key"`
	Message        string `yaml:"message"`
	MaxTokens      int    `yaml:"max-tokens"`
	MaxRounds      int    `yaml:"max-rounds"`
	CatchUpMinutes int    `yaml:"catch-up-minutes"`
	// Language selects the language for host.log lines, the status/run JSON
	// (a translated Skipped/Warning value plus a "lang" field saying which
	// language was used), and the default the panel page's own ?lang= query
	// falls back to. "auto" (the default) negotiates per call; see
	// requestLanguage/logLanguage in i18n.go.
	Language  string                     `yaml:"language"`
	Default   defaultSchedule            `yaml:"default"`
	Providers map[string]providerDefault `yaml:"providers"`
	Auths     []authOverride             `yaml:"auths"`

	// New-format (v0.3.0) fields. Populated when the config uses the
	// simplified time/accounts/model/advanced shape instead of the legacy
	// default/providers/auths block above. See decodeConfig for the
	// legacy-vs-new detection and how Advanced.* gets flattened into the
	// shared fields above (BaseURL, Message, MaxTokens, ...) so the rest of
	// the codebase (runner.go, management.go) never has to branch on format
	// for those generic settings -- only schedule/model resolution differs.
	TimeRaw  flexStringList      `yaml:"time"`
	Model    modelField          `yaml:"model"`
	Accounts accountOverrideList `yaml:"accounts"`
	Advanced advancedConfig      `yaml:"advanced"`

	// legacyMode is true when decodeConfig detected at least one legacy-only
	// top-level key (default/providers/auths/timezone/base-url/api-key/
	// message/max-tokens/max-rounds/catch-up-minutes). It selects which of
	// the two schedule/model resolution code paths (resolveAuthConfig vs
	// resolveNewAuth) runner.go and management.go use.
	legacyMode bool

	// location is resolved from Timezone (legacy mode) or Advanced.Timezone
	// (new mode, defaulting to time.Local) at decode time; nil is never
	// returned to callers (decodeConfig fails first).
	location *time.Location
}

// advancedConfig is the new-format `advanced:` block: every setting a
// beginner never needs to touch, all optional. See decodeConfig for how each
// field is defaulted and flattened onto pluginConfig's shared fields.
type advancedConfig struct {
	Timezone       string            `yaml:"timezone"`
	BaseURL        string            `yaml:"base-url"`
	APIKey         string            `yaml:"api-key"`
	Message        string            `yaml:"message"`
	MaxTokens      int               `yaml:"max-tokens"`
	MaxRounds      int               `yaml:"max-rounds"`
	CatchUpMinutes int               `yaml:"catch-up-minutes"`
	Language       string            `yaml:"language"`
	Log            *bool             `yaml:"log"`
	Models         map[string]string `yaml:"models"`
}

// flexStringList decodes a YAML value that may be a single string (optionally
// comma-separated, e.g. "05:30, 10:30") or a list of strings, into a
// normalized []string. Used for the new-format `time:` key and the
// per-account `time:` override.
type flexStringList []string

func (f *flexStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*f = splitCommaList(s)
		return nil
	case yaml.SequenceNode:
		var out []string
		for _, item := range node.Content {
			var s string
			if err := item.Decode(&s); err != nil {
				return fmt.Errorf("expected a string list item: %w", err)
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		*f = out
		return nil
	default:
		return fmt.Errorf("expected a string or a list of strings, got yaml kind %d", node.Kind)
	}
}

// splitCommaList splits a comma-separated string into trimmed, non-empty
// parts, e.g. "05:30, 10:30" -> ["05:30", "10:30"].
func splitCommaList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	// Only a list of plain HH:MM entries is comma-separated. Anything else
	// (a cron expression such as "30 5,10,15,20 * * *") uses commas inside
	// its own fields and must stay one expression.
	for _, part := range out {
		if !hhmmPattern.MatchString(part) {
			return []string{s}
		}
	}
	return out
}

// modelField decodes the new-format top-level `model:` key, which accepts
// either a scalar ("auto" or a literal model name) or a mapping of
// provider -> model name (equivalent to advanced.models, for a beginner who
// wants to write everything in one place).
type modelField struct {
	Scalar string
	Map    map[string]string
}

func (m *modelField) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&m.Scalar)
	case yaml.MappingNode:
		return node.Decode(&m.Map)
	default:
		return fmt.Errorf("model: expected a string or a mapping, got yaml kind %d", node.Kind)
	}
}

// accountOverrideNew is one entry of the new-format `accounts:` list, after
// normalizing both the plain-glob-string shorthand and the
// {match, time, model} object form onto a single shape.
type accountOverrideNew struct {
	Match string
	Time  []string
	Model string
}

// accountOverrideList decodes the new-format `accounts:` key: a bare string
// (e.g. "*"), or a list whose items are each either a plain glob string or
// an object with match/time/model.
type accountOverrideList []accountOverrideNew

func (a *accountOverrideList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		if strings.TrimSpace(s) != "" {
			*a = append(*a, accountOverrideNew{Match: s})
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				var s string
				if err := item.Decode(&s); err != nil {
					return fmt.Errorf("accounts[] item: %w", err)
				}
				if strings.TrimSpace(s) != "" {
					*a = append(*a, accountOverrideNew{Match: s})
				}
			case yaml.MappingNode:
				var obj struct {
					Match string         `yaml:"match"`
					Time  flexStringList `yaml:"time"`
					Model string         `yaml:"model"`
				}
				if err := item.Decode(&obj); err != nil {
					return fmt.Errorf("accounts[] object: %w", err)
				}
				*a = append(*a, accountOverrideNew{Match: obj.Match, Time: []string(obj.Time), Model: obj.Model})
			default:
				return fmt.Errorf("accounts[] item must be a string or an object, got yaml kind %d", item.Kind)
			}
		}
		return nil
	default:
		return fmt.Errorf("accounts: expected a string or a list, got yaml kind %d", node.Kind)
	}
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
		Language:       defaultLanguage,
		Default: defaultSchedule{
			Times: []string{defaultTime},
		},
		location: time.UTC,
	}
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// legacyOnlyTopLevelKeys are the top-level YAML keys that only ever existed
// in the pre-v0.3.0 config shape. Any one of them present at the top level
// means "this is a legacy config", regardless of whether new-format keys
// (time/accounts/model/advanced) also happen to be present.
var legacyOnlyTopLevelKeys = []string{
	"default", "providers", "auths", "timezone", "base-url", "api-key",
	"message", "max-tokens", "max-rounds", "catch-up-minutes",
}

// detectLegacyFormat reports whether configYAML uses the legacy top-level
// key shape. A parse failure here is not fatal on its own -- the real
// yaml.Unmarshal into pluginConfig right after this call will surface the
// error properly -- so this just conservatively reports "not legacy".
func detectLegacyFormat(configYAML []byte) bool {
	if len(configYAML) == 0 {
		return false
	}
	var raw map[string]any
	if err := yaml.Unmarshal(configYAML, &raw); err != nil {
		return false
	}
	for _, key := range legacyOnlyTopLevelKeys {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	return false
}

// decodeConfig parses raw plugin.register/plugin.reconfigure request bytes
// (the {"config_yaml": <bytes>} envelope) into a fully defaulted pluginConfig.
//
// It supports two config shapes: the legacy default/providers/auths block
// (detected by detectLegacyFormat, resolved exactly as it always has been
// -- see the legacyMode branch below) and the v0.3.0 minimal
// time/accounts/model/advanced shape (the else branch). Callers (main.go's
// plugin.register handler) can check the returned cfg.legacyMode to decide
// whether to emit the "consider migrating" hostLog notice.
func decodeConfig(raw []byte) (pluginConfig, error) {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return pluginConfig{}, fmt.Errorf("invalid lifecycle request: %w", err)
		}
	}
	cfg := defaultPluginConfig()
	legacyMode := detectLegacyFormat(req.ConfigYAML)
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return pluginConfig{}, fmt.Errorf("parse plugin config: %w", err)
		}
	}
	cfg.legacyMode = legacyMode

	if legacyMode {
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
		cfg.Language = normalizeLanguageSetting(cfg.Language)

		loc, err := resolveLocation(cfg.Timezone)
		if err != nil {
			return pluginConfig{}, err
		}
		cfg.location = loc
		cfg.Timezone = loc.String()
		return cfg, nil
	}

	// New (v0.3.0) format: flatten Advanced.* onto the shared fields (with
	// their v0.3.0 defaults, not the legacy defaultPluginConfig() ones) so
	// the rest of the codebase reads cfg.BaseURL/cfg.Message/... exactly as
	// before regardless of which format was used.
	if len(cfg.TimeRaw) == 0 {
		cfg.TimeRaw = flexStringList{defaultTime}
	}
	if strings.TrimSpace(cfg.Advanced.Timezone) == "" {
		cfg.Timezone = "" // resolved to time.Local below
	} else {
		cfg.Timezone = cfg.Advanced.Timezone
	}
	if strings.TrimSpace(cfg.Advanced.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	} else {
		cfg.BaseURL = cfg.Advanced.BaseURL
	}
	cfg.APIKey = cfg.Advanced.APIKey
	if strings.TrimSpace(cfg.Advanced.Message) == "" {
		cfg.Message = defaultMessage
	} else {
		cfg.Message = cfg.Advanced.Message
	}
	if cfg.Advanced.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	} else {
		cfg.MaxTokens = cfg.Advanced.MaxTokens
	}
	if cfg.Advanced.MaxRounds <= 0 {
		cfg.MaxRounds = defaultMaxRounds
	} else {
		cfg.MaxRounds = cfg.Advanced.MaxRounds
	}
	if cfg.Advanced.CatchUpMinutes <= 0 {
		cfg.CatchUpMinutes = defaultCatchUpMinutes
	} else {
		cfg.CatchUpMinutes = cfg.Advanced.CatchUpMinutes
	}
	cfg.Language = normalizeLanguageSetting(cfg.Advanced.Language)
	cfg.Log = true
	if cfg.Advanced.Log != nil {
		cfg.Log = *cfg.Advanced.Log
	}

	var loc *time.Location
	if strings.TrimSpace(cfg.Timezone) == "" {
		loc = time.Local
	} else {
		resolved, err := resolveLocation(cfg.Timezone)
		if err != nil {
			return pluginConfig{}, err
		}
		loc = resolved
	}
	cfg.location = loc
	cfg.Timezone = loc.String()

	return cfg, nil
}

// modelSource labels identify which tier of the model priority chain
// resolved a due target's model. Exposed as plain strings since they are
// serialized directly into the status JSON.
const (
	modelSourcePanel    = "panel"
	modelSourceAccount  = "account"
	modelSourceGlobal   = "global"
	modelSourceProvider = "provider"
	modelSourceAuto     = "auto"
)

// mergedProviderModels merges advanced.models with the top-level `model:`
// map form (equivalent, provided as a convenience so a beginner can write
// everything under one key). The top-level map form wins on a key
// collision, since it is the more visible of the two.
func mergedProviderModels(cfg pluginConfig) map[string]string {
	out := make(map[string]string, len(cfg.Advanced.Models)+len(cfg.Model.Map))
	for k, v := range cfg.Advanced.Models {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	for k, v := range cfg.Model.Map {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// matchAccountOverride returns the accounts[] entry matching name, if any.
// Later entries in the list win over earlier ones for the same file,
// mirroring the legacy auths[] semantics.
func matchAccountOverride(accounts []accountOverrideNew, name string) (accountOverrideNew, bool) {
	var found accountOverrideNew
	ok := false
	for _, a := range accounts {
		if globMatch(a.Match, name) {
			found = a
			ok = true
		}
	}
	return found, ok
}

// resolveModelSpecTier resolves the explicit model (if any) for one
// new-format auth, following the priority chain: panel override
// (auth-scoped, then global-scoped) > account-object model > top-level
// `model:` (scalar, non-"auto") > advanced.models[provider] (merged with
// the top-level `model:` map form) > auto. spec=="" means no tier resolved
// an explicit model; the caller must fall through to candidate-list
// auto-selection (see selectModel in candidates.go).
func resolveModelSpecTier(cfg pluginConfig, ov modelOverrides, name, provider string, account accountOverrideNew) (spec, source string) {
	if m := strings.TrimSpace(ov.Auths[name]); m != "" {
		return m, modelSourcePanel
	}
	if m := strings.TrimSpace(ov.Global); m != "" {
		return m, modelSourcePanel
	}
	if m := strings.TrimSpace(account.Model); m != "" {
		return m, modelSourceAccount
	}
	if m := strings.TrimSpace(cfg.Model.Scalar); m != "" && !strings.EqualFold(m, "auto") {
		return m, modelSourceGlobal
	}
	providerMap := mergedProviderModels(cfg)
	if m := strings.TrimSpace(providerMap[strings.ToLower(strings.TrimSpace(provider))]); m != "" {
		return m, modelSourceProvider
	}
	return "", modelSourceAuto
}

// newAuthResolution is the new-format equivalent of effectiveAuthConfig: the
// per-account schedule/model resolution result, before the model spec's
// "auto" case has been resolved against a live GET /v1/models (that step
// needs the precheck result, so it happens later in runner.go).
type newAuthResolution struct {
	// Selected is false when no accounts[] entry matches this auth at all
	// (including the "accounts: [] / omitted entirely" case), meaning this
	// account is not warmed up.
	Selected bool
	// TimeRaw is the raw HH:MM/cron strings to schedule against (the
	// account's own `time:` override, or the top-level `time:` otherwise).
	TimeRaw         []string
	ModelSpec       string
	ModelSource     string
	ReasoningEffort string
}

// resolveNewAuth computes the new-format schedule/model resolution for one
// auth file. ov is the current panel-override snapshot (empty/zero value is
// fine when no engine/overrides store is available, e.g. in tests).
func resolveNewAuth(cfg pluginConfig, ov modelOverrides, name, provider string) newAuthResolution {
	account, matched := matchAccountOverride(cfg.Accounts, name)
	if !matched {
		return newAuthResolution{Selected: false}
	}
	timesRaw := account.Time
	if len(timesRaw) == 0 {
		timesRaw = cfg.TimeRaw
	}
	modelSpec, source := resolveModelSpecTier(cfg, ov, name, provider, account)
	reasoning := ""
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		reasoning = codexReasoningEffort
	}
	return newAuthResolution{
		Selected:        true,
		TimeRaw:         append([]string(nil), timesRaw...),
		ModelSpec:       modelSpec,
		ModelSource:     source,
		ReasoningEffort: reasoning,
	}
}

// normalizeLanguageSetting validates the `language:` config value against
// "auto" and the four supported language codes (case-insensitively),
// returning its canonical form. An empty or unrecognized value quietly
// becomes "auto" rather than failing the whole plugin.register/reconfigure
// call over a typo in one optional field.
func normalizeLanguageSetting(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return "auto"
	}
	if l, ok := normalizeLangTag(raw); ok {
		return string(l)
	}
	return "auto"
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
