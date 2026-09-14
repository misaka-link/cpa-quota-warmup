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
	// v0.4.0's shipped default is file mode: just `enabled: true` (plus a
	// commented-out config-file example), no legacy or v3.0 inline keys.
	if cfg.legacyMode || cfg.v3InlineMode {
		t.Fatalf("expected the shipped default config to use file mode, got legacyMode=%v v3InlineMode=%v", cfg.legacyMode, cfg.v3InlineMode)
	}
	if !cfg.Enabled {
		t.Fatalf("expected enabled: true")
	}
	if cfg.ConfigFilePath != "" {
		t.Fatalf("ConfigFilePath = %q, want empty (config-file is commented out, defaulting to <cwd>/quota-warmup.yaml)", cfg.ConfigFilePath)
	}
	path, err := resolveWarmupFilePath(cfg)
	if err != nil {
		t.Fatalf("resolveWarmupFilePath: %v", err)
	}
	if !strings.HasSuffix(path, string(os.PathSeparator)+warmupFileName) {
		t.Fatalf("resolveWarmupFilePath = %q, want it to end in /%s", path, warmupFileName)
	}
}
