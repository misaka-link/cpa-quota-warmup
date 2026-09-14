package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestOpsMergeConfigBlockParsesAsExpected extracts the BLOCK constant from
// ops/merge-config.py (the exact YAML shipped to real deployments) and
// feeds it through decodeConfig, so a future edit to that script that
// silently breaks parsing (or drifts from the documented minimal-config
// shape) fails CI instead of only being caught during a real deploy.
func TestOpsMergeConfigBlockParsesAsExpected(t *testing.T) {
	raw, err := os.ReadFile("ops/merge-config.py")
	if err != nil {
		t.Fatalf("read ops/merge-config.py: %v", err)
	}
	m := regexp.MustCompile(`(?s)BLOCK = """(.*?)"""`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("could not find the BLOCK = \"\"\"...\"\"\" constant in ops/merge-config.py")
	}
	block := m[1]

	// Dedent: BLOCK's first line is "    cpa-quota-warmup:" and every
	// following line is indented two extra spaces under that key. Strip the
	// wrapper key and the leading "      " (6 spaces) from every line so
	// decodeConfig sees a plain top-level YAML document.
	lines := strings.Split(block, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "cpa-quota-warmup:" {
		t.Fatalf("expected BLOCK's first line to be the cpa-quota-warmup: key, got %q", lines[0])
	}
	var dedented []string
	for _, line := range lines[1:] {
		dedented = append(dedented, strings.TrimPrefix(line, "      "))
	}
	yamlDoc := strings.Join(dedented, "\n")

	raw2 := lifecycleRequestJSON(t, []byte(yamlDoc))
	cfg, err := decodeConfig(raw2)
	if err != nil {
		t.Fatalf("decodeConfig(ops/merge-config.py's BLOCK): %v\nyaml:\n%s", err, yamlDoc)
	}
	if cfg.legacyMode {
		t.Fatalf("expected the shipped default config to use the new format")
	}
	if !cfg.Enabled {
		t.Fatalf("expected enabled: true")
	}
	if len(cfg.TimeRaw) != 1 || cfg.TimeRaw[0] != "05:30" {
		t.Fatalf("TimeRaw = %v, want [05:30]", cfg.TimeRaw)
	}
	if cfg.Model.Scalar != "auto" {
		t.Fatalf("Model.Scalar = %q, want auto", cfg.Model.Scalar)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Match != "codex-*-team.json" {
		t.Fatalf("Accounts = %+v, want a single codex-*-team.json glob", cfg.Accounts)
	}

	res := resolveNewAuth(cfg, modelOverrides{}, "codex-alice-team.json", "codex")
	if !res.Selected {
		t.Fatalf("expected the shipped config to select codex-*-team.json accounts")
	}
	if res.ModelSpec != "" || res.ModelSource != modelSourceAuto {
		t.Fatalf("expected auto model selection, got spec=%q source=%q", res.ModelSpec, res.ModelSource)
	}
	if res.ReasoningEffort != codexReasoningEffort {
		t.Fatalf("expected codex reasoning-effort=%q, got %q", codexReasoningEffort, res.ReasoningEffort)
	}
}
