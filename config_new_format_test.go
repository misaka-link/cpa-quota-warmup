package main

import "testing"

// TestDecodeConfigMinimalThreeLine is the v0.3.0 headline case: enabled +
// time + accounts, nothing else.
func TestDecodeConfigMinimalThreeLine(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
enabled: true
time: "05:30"
accounts: ["codex-*-team.json"]
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if cfg.legacyMode {
		t.Fatalf("expected the minimal config to be treated as new format")
	}
	if len(cfg.TimeRaw) != 1 || cfg.TimeRaw[0] != "05:30" {
		t.Fatalf("TimeRaw = %v", cfg.TimeRaw)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Match != "codex-*-team.json" {
		t.Fatalf("Accounts = %+v", cfg.Accounts)
	}
	res := resolveNewAuth(cfg, modelOverrides{}, "codex-alice-team.json", "codex")
	if !res.Selected {
		t.Fatalf("expected codex-alice-team.json to match the accounts[] glob")
	}
	if len(res.TimeRaw) != 1 || res.TimeRaw[0] != "05:30" {
		t.Fatalf("resolved TimeRaw = %v, want the top-level time", res.TimeRaw)
	}
	if res.ModelSpec != "" || res.ModelSource != modelSourceAuto {
		t.Fatalf("expected auto model selection, got spec=%q source=%q", res.ModelSpec, res.ModelSource)
	}
	if res.ReasoningEffort != codexReasoningEffort {
		t.Fatalf("expected codex to auto-attach reasoning-effort=%q, got %q", codexReasoningEffort, res.ReasoningEffort)
	}

	// An account not matching any glob is not selected at all.
	if other := resolveNewAuth(cfg, modelOverrides{}, "antigravity-bob.json", "antigravity"); other.Selected {
		t.Fatalf("expected an unmatched account to not be selected: %+v", other)
	}
}

// TestDecodeConfigTimeAcceptsCommaSeparatedString covers "05:30, 10:30".
func TestDecodeConfigTimeAcceptsCommaSeparatedString(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
time: "05:30, 10:30"
accounts: ["*"]
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if len(cfg.TimeRaw) != 2 || cfg.TimeRaw[0] != "05:30" || cfg.TimeRaw[1] != "10:30" {
		t.Fatalf("TimeRaw = %v", cfg.TimeRaw)
	}
}

// TestDecodeConfigTimeAcceptsList covers time: ["05:30", "10:30"].
func TestDecodeConfigTimeAcceptsList(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
time: ["05:30", "10:30"]
accounts: ["*"]
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if len(cfg.TimeRaw) != 2 || cfg.TimeRaw[0] != "05:30" || cfg.TimeRaw[1] != "10:30" {
		t.Fatalf("TimeRaw = %v", cfg.TimeRaw)
	}
}

// TestDecodeConfigAccountsWildcardString covers accounts: "*" (bare scalar,
// not a one-element list) meaning "every account".
func TestDecodeConfigAccountsWildcardString(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`accounts: "*"`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Match != "*" {
		t.Fatalf("Accounts = %+v", cfg.Accounts)
	}
	if res := resolveNewAuth(cfg, modelOverrides{}, "anything.json", "codex"); !res.Selected {
		t.Fatalf("expected accounts: \"*\" to match every account")
	}
}

// TestDecodeConfigMissingAccountsWarmsNothing covers the "empty/missing
// accounts = warm up nothing" rule.
func TestDecodeConfigMissingAccountsWarmsNothing(t *testing.T) {
	cfg, err := decodeConfig(nil)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	if len(cfg.Accounts) != 0 {
		t.Fatalf("expected no accounts by default, got %+v", cfg.Accounts)
	}
	if res := resolveNewAuth(cfg, modelOverrides{}, "anything.json", "codex"); res.Selected {
		t.Fatalf("expected nothing to be selected when accounts is empty")
	}
}

// TestDecodeConfigAccountObjectOverride covers the per-account
// {match,time,model} object form, including its own time/model overriding
// the top-level ones.
func TestDecodeConfigAccountObjectOverride(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
time: "05:30"
model: "auto"
accounts:
  - "codex-*-team.json"
  - match: "codex-alice-team.json"
    time: "10:30"
    model: "gpt-5.6-luna"
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}

	// alice: matched by both the plain glob and the specific override; the
	// later (more specific) entry wins.
	alice := resolveNewAuth(cfg, modelOverrides{}, "codex-alice-team.json", "codex")
	if !alice.Selected {
		t.Fatalf("expected alice to be selected")
	}
	if len(alice.TimeRaw) != 1 || alice.TimeRaw[0] != "10:30" {
		t.Fatalf("alice TimeRaw = %v, want the account override [10:30]", alice.TimeRaw)
	}
	if alice.ModelSpec != "gpt-5.6-luna" || alice.ModelSource != modelSourceAccount {
		t.Fatalf("alice model = %q source=%q, want gpt-5.6-luna/account", alice.ModelSpec, alice.ModelSource)
	}

	// bob: only matched by the plain glob, uses the top-level time/model.
	bob := resolveNewAuth(cfg, modelOverrides{}, "codex-bob-team.json", "codex")
	if !bob.Selected {
		t.Fatalf("expected bob to be selected")
	}
	if len(bob.TimeRaw) != 1 || bob.TimeRaw[0] != "05:30" {
		t.Fatalf("bob TimeRaw = %v, want the top-level time [05:30]", bob.TimeRaw)
	}
	if bob.ModelSpec != "" || bob.ModelSource != modelSourceAuto {
		t.Fatalf("bob model spec=%q source=%q, want auto (model: auto at top level)", bob.ModelSpec, bob.ModelSource)
	}
}

// TestResolveModelSpecTierPriorityChain exercises the full priority chain:
// panel > account > top-level model > advanced.models/model-map > auto.
func TestResolveModelSpecTierPriorityChain(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
accounts: ["*"]
model: "global-model"
advanced:
  models:
    codex: "provider-model"
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}

	// No overrides at all: falls through to the top-level model (global).
	spec, source := resolveModelSpecTier(cfg, modelOverrides{}, "a.json", "codex", accountOverrideNew{})
	if spec != "global-model" || source != modelSourceGlobal {
		t.Fatalf("got spec=%q source=%q, want global-model/global", spec, source)
	}

	// Account-object model outranks the top-level model.
	spec, source = resolveModelSpecTier(cfg, modelOverrides{}, "a.json", "codex", accountOverrideNew{Model: "account-model"})
	if spec != "account-model" || source != modelSourceAccount {
		t.Fatalf("got spec=%q source=%q, want account-model/account", spec, source)
	}

	// A panel override outranks everything, including the account model.
	ov := modelOverrides{Auths: map[string]string{"a.json": "panel-model"}}
	spec, source = resolveModelSpecTier(cfg, ov, "a.json", "codex", accountOverrideNew{Model: "account-model"})
	if spec != "panel-model" || source != modelSourcePanel {
		t.Fatalf("got spec=%q source=%q, want panel-model/panel", spec, source)
	}

	// A panel *global* override also outranks the account model, per the
	// coordinator's flat "panel > account > global > provider > auto" list.
	ov = modelOverrides{Global: "panel-global-model"}
	spec, source = resolveModelSpecTier(cfg, ov, "a.json", "codex", accountOverrideNew{Model: "account-model"})
	if spec != "panel-global-model" || source != modelSourcePanel {
		t.Fatalf("got spec=%q source=%q, want panel-global-model/panel", spec, source)
	}

	// With no top-level model set (or "auto"), advanced.models[provider] applies.
	raw2 := lifecycleRequestJSON(t, []byte(`
accounts: ["*"]
model: "auto"
advanced:
  models:
    codex: "provider-model"
`))
	cfg2, err := decodeConfig(raw2)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	spec, source = resolveModelSpecTier(cfg2, modelOverrides{}, "a.json", "codex", accountOverrideNew{})
	if spec != "provider-model" || source != modelSourceProvider {
		t.Fatalf("got spec=%q source=%q, want provider-model/provider", spec, source)
	}

	// No overrides anywhere at all: auto.
	spec, source = resolveModelSpecTier(cfg2, modelOverrides{}, "a.json", "kimi", accountOverrideNew{})
	if spec != "" || source != modelSourceAuto {
		t.Fatalf("got spec=%q source=%q, want auto", spec, source)
	}
}

// TestModelFieldMapFormEquivalentToAdvancedModels covers `model: {codex: ...}`
// being merged the same as advanced.models.
func TestModelFieldMapFormEquivalentToAdvancedModels(t *testing.T) {
	raw := lifecycleRequestJSON(t, []byte(`
accounts: ["*"]
model: { codex: "gpt-5.6-luna", kimi: "kimi-k2.8" }
`))
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	spec, source := resolveModelSpecTier(cfg, modelOverrides{}, "a.json", "codex", accountOverrideNew{})
	if spec != "gpt-5.6-luna" || source != modelSourceProvider {
		t.Fatalf("got spec=%q source=%q, want gpt-5.6-luna/provider", spec, source)
	}
	spec, source = resolveModelSpecTier(cfg, modelOverrides{}, "a.json", "kimi", accountOverrideNew{})
	if spec != "kimi-k2.8" || source != modelSourceProvider {
		t.Fatalf("got spec=%q source=%q, want kimi-k2.8/provider", spec, source)
	}
}

// TestSelectModelAutoCandidates exercises the built-in cheapest-first
// candidate lists and the precheck-unreachable fallback.
func TestSelectModelAutoCandidates(t *testing.T) {
	model, ok := selectModel("codex", nil, false)
	if !ok || model != "gpt-5.3-codex-spark" {
		t.Fatalf("precheck unreachable: got model=%q ok=%v, want the first candidate", model, ok)
	}

	available := map[string]bool{"gpt-5.6-luna": true, "gpt-6-astra": true}
	model, ok = selectModel("codex", available, true)
	if !ok || model != "gpt-5.6-luna" {
		t.Fatalf("got model=%q ok=%v, want the cheapest available candidate gpt-5.6-luna", model, ok)
	}

	model, ok = selectModel("codex", map[string]bool{"unrelated-model": true}, true)
	if ok {
		t.Fatalf("expected ok=false when none of the candidates are available, got model=%q", model)
	}

	if _, ok := selectModel("no-such-provider", map[string]bool{}, true); ok {
		t.Fatalf("expected ok=false for an unknown provider")
	}
}

// TestDecodeConfigOldAndNewFormatsAreEquivalent is the coordinator-required
// proof that an old-format config and its hand-translated new-format
// equivalent resolve the same effective schedule/model for the same set of
// accounts.
func TestDecodeConfigOldAndNewFormatsAreEquivalent(t *testing.T) {
	oldRaw := lifecycleRequestJSON(t, []byte(`
timezone: "UTC"
default:
  enabled: false
  times: ["05:30"]
providers:
  codex: { model: "gpt-5.6-luna", reasoning-effort: "low" }
auths:
  - match: "codex-*-team.json"
    enabled: true
`))
	oldCfg, err := decodeConfig(oldRaw)
	if err != nil {
		t.Fatalf("decodeConfig(old): %v", err)
	}
	if !oldCfg.legacyMode {
		t.Fatalf("expected the old-format config to be detected as legacy")
	}
	oldEff, ok := resolveAuthConfig(oldCfg, "codex-alice-team.json", "codex")
	if !ok {
		t.Fatalf("expected the old-format config to resolve codex-alice-team.json")
	}
	if _, ok := resolveAuthConfig(oldCfg, "codex-solo.json", "codex"); ok {
		t.Fatalf("expected a non-team codex account to stay disabled under the old config")
	}

	newRaw := lifecycleRequestJSON(t, []byte(`
time: "05:30"
model: "gpt-5.6-luna"
accounts: ["codex-*-team.json"]
advanced:
  timezone: "UTC"
`))
	newCfg, err := decodeConfig(newRaw)
	if err != nil {
		t.Fatalf("decodeConfig(new): %v", err)
	}
	if newCfg.legacyMode {
		t.Fatalf("expected the new-format config to not be detected as legacy")
	}
	newEff := resolveNewAuth(newCfg, modelOverrides{}, "codex-alice-team.json", "codex")
	if !newEff.Selected {
		t.Fatalf("expected the new-format config to select codex-alice-team.json")
	}
	if other := resolveNewAuth(newCfg, modelOverrides{}, "codex-solo.json", "codex"); other.Selected {
		t.Fatalf("expected a non-team codex account to stay unselected under the new config")
	}

	if len(oldEff.Times) != len(newEff.TimeRaw) || oldEff.Times[0] != newEff.TimeRaw[0] {
		t.Fatalf("times differ: old=%v new=%v", oldEff.Times, newEff.TimeRaw)
	}
	if oldEff.Model != newEff.ModelSpec {
		t.Fatalf("models differ: old=%q new=%q", oldEff.Model, newEff.ModelSpec)
	}
	if oldEff.ReasoningEffort != codexReasoningEffort {
		t.Fatalf("old-format reasoning-effort = %q, want %q", oldEff.ReasoningEffort, codexReasoningEffort)
	}
	if newEff.ReasoningEffort != codexReasoningEffort {
		t.Fatalf("new-format reasoning-effort = %q, want %q (auto-attached for codex)", newEff.ReasoningEffort, codexReasoningEffort)
	}
	if oldCfg.location.String() != newCfg.location.String() {
		t.Fatalf("timezones differ: old=%v new=%v", oldCfg.location, newCfg.location)
	}
}

func TestSplitCommaListKeepsCronExpressionsWhole(t *testing.T) {
	cases := map[string][]string{
		"05:30":               {"05:30"},
		"05:30, 10:30":        {"05:30", "10:30"},
		"30 5,10,15,20 * * *": {"30 5,10,15,20 * * *"},
		"0 */5 * * *":         {"0 */5 * * *"},
		"  ":                  nil,
	}
	for in, want := range cases {
		got := splitCommaList(in)
		if len(got) != len(want) {
			t.Fatalf("splitCommaList(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitCommaList(%q) = %v, want %v", in, got, want)
			}
		}
	}
}
