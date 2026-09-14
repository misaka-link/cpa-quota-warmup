package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestWarmupFileManagerGeneratesFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{
		{Name: "codex-alice-team.json", Provider: "codex"},
		{Name: "antigravity-bob.json", Provider: "antigravity"},
		{Name: "runtime-only", Provider: "codex", RuntimeOnly: true},
	}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "cpa-quota-warmup 预热配置") {
		t.Fatalf("expected the header comment, got:\n%s", text)
	}
	if !strings.Contains(text, "# provider: codex") || !strings.Contains(text, "# provider: antigravity") {
		t.Fatalf("expected provider comments, got:\n%s", text)
	}
	if strings.Contains(text, "runtime-only") {
		t.Fatalf("expected runtime-only auths to be excluded, got:\n%s", text)
	}

	data, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error: %s", parseErr)
	}
	if len(data.Accounts) != 2 {
		t.Fatalf("Accounts = %+v, want exactly 2 entries", data.Accounts)
	}
	for name, acct := range data.Accounts {
		if acct.Enabled == nil || *acct.Enabled {
			t.Fatalf("account %s: expected enabled=false by default, got %+v", name, acct)
		}
	}
}

func TestWarmupFileManagerAppendsNewAccountsPreservingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	existing := "# my own header comment, must survive\n" +
		"defaults:\n" +
		"  time: \"05:30\"\n" +
		"  model: auto\n" +
		"accounts:\n" +
		"  codex-alice-team.json: # provider: codex\n" +
		"    enabled: true   # turned on by hand\n" +
		"    time: \"06:00\"\n" +
		"    model: gpt-5.6-luna\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{
		{Name: "codex-alice-team.json", Provider: "codex"},
		{Name: "antigravity-bob.json", Provider: "antigravity"},
	}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "my own header comment, must survive") {
		t.Fatalf("expected the user's own header comment to survive, got:\n%s", text)
	}
	if !strings.Contains(text, "turned on by hand") {
		t.Fatalf("expected the user's own inline comment to survive, got:\n%s", text)
	}
	if !strings.Contains(text, "gpt-5.6-luna") || !strings.Contains(text, "06:00") {
		t.Fatalf("expected the user's own values to survive, got:\n%s", text)
	}
	if !strings.Contains(text, "antigravity-bob.json") {
		t.Fatalf("expected the new account to be appended, got:\n%s", text)
	}

	data, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error: %s", parseErr)
	}
	alice := data.Accounts["codex-alice-team.json"]
	if alice.Enabled == nil || !*alice.Enabled || alice.Model != "gpt-5.6-luna" {
		t.Fatalf("alice = %+v, want the user's own values preserved", alice)
	}
	bob, ok := data.Accounts["antigravity-bob.json"]
	if !ok || bob.Enabled == nil || *bob.Enabled {
		t.Fatalf("bob = %+v (present=%v), want a freshly appended disabled section", bob, ok)
	}
}

func TestWarmupFileManagerMarksAndUnmarksVanishedAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{{Name: "codex-alice-team.json", Provider: "codex"}}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (create): %v", err)
	}

	// The account disappears from host.auth.list.
	if err := m.ensureFresh(nil); err != nil {
		t.Fatalf("ensureFresh (vanish): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), warmupFileVanishedMarker) {
		t.Fatalf("expected the vanished marker, got:\n%s", raw)
	}

	// The account comes back.
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (return): %v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), warmupFileVanishedMarker) {
		t.Fatalf("expected the vanished marker to be removed once the account returns, got:\n%s", raw)
	}
}

func TestWarmupFileManagerParseFailureKeepsLastGoodConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{{Name: "codex-alice-team.json", Provider: "codex"}}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (create): %v", err)
	}
	if err := m.setAccount("codex-alice-team.json", warmupFieldUpdate{Enabled: boolPtr(true)}, "codex"); err != nil {
		t.Fatalf("setAccount: %v", err)
	}
	goodData, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error before corruption: %s", parseErr)
	}
	if acct := goodData.Accounts["codex-alice-team.json"]; acct.Enabled == nil || !*acct.Enabled {
		t.Fatalf("expected the account to be enabled before corruption, got %+v", acct)
	}

	// Corrupt the file on disk (simulating a user typo) and make sure a
	// fresh manager instance (as if the plugin restarted) or a subsequent
	// ensureFresh call never panics/discards data it doesn't have yet.
	if err := os.WriteFile(path, []byte("accounts: [this is not a valid mapping\n"), 0o644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (corrupted): %v", err)
	}
	staleData, parseErr := m.snapshot()
	if parseErr == "" {
		t.Fatalf("expected a parse error to be reported after corrupting the file")
	}
	if acct := staleData.Accounts["codex-alice-team.json"]; acct.Enabled == nil || !*acct.Enabled {
		t.Fatalf("expected the last-known-good data to be kept after a parse failure, got %+v", acct)
	}

	// Fix the file; the manager should recover on the next ensureFresh.
	if err := os.WriteFile(path, []byte("defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  codex-alice-team.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"), 0o644); err != nil {
		t.Fatalf("fix file: %v", err)
	}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (fixed): %v", err)
	}
	fixedData, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("expected no parse error after fixing the file, got %s", parseErr)
	}
	if acct := fixedData.Accounts["codex-alice-team.json"]; acct.Enabled == nil || *acct.Enabled {
		t.Fatalf("expected the fixed file's own value (enabled=false) to be picked up, got %+v", acct)
	}
}

func TestWarmupFileManagerSetAccountPreservesCommentsAndWritesBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{{Name: "codex-alice-team.json", Provider: "codex"}}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}

	enabled := true
	timeVal := "10:30"
	model := "gpt-5.6-luna"
	if err := m.setAccount("codex-alice-team.json", warmupFieldUpdate{Enabled: &enabled, Time: &timeVal, Model: &model}, "codex"); err != nil {
		t.Fatalf("setAccount: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "# provider: codex") {
		t.Fatalf("expected the provider comment to survive the edit, got:\n%s", text)
	}
	if !strings.Contains(text, "10:30") || !strings.Contains(text, "gpt-5.6-luna") || !strings.Contains(text, "enabled: true") {
		t.Fatalf("expected the new values to be written, got:\n%s", text)
	}

	data, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error: %s", parseErr)
	}
	acct := data.Accounts["codex-alice-team.json"]
	if acct.Enabled == nil || !*acct.Enabled || acct.Model != "gpt-5.6-luna" || len(acct.Time) != 1 || acct.Time[0] != "10:30" {
		t.Fatalf("acct = %+v, want the updated values", acct)
	}
}

func TestWarmupFileManagerSetAccountCreatesMissingSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	if err := m.ensureFresh(nil); err != nil {
		t.Fatalf("ensureFresh: %v", err)
	}
	model := "kimi-k2.8"
	if err := m.setAccount("brand-new.json", warmupFieldUpdate{Model: &model}, "kimi"); err != nil {
		t.Fatalf("setAccount: %v", err)
	}
	data, parseErr := m.snapshot()
	if parseErr != "" {
		t.Fatalf("unexpected parse error: %s", parseErr)
	}
	acct, ok := data.Accounts["brand-new.json"]
	if !ok || acct.Model != "kimi-k2.8" {
		t.Fatalf("acct = %+v (present=%v), want a freshly created section with model=kimi-k2.8", acct, ok)
	}
}

func TestWarmupFileManagerOnlyWritesWhenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	entries := []pluginapi.HostAuthFileEntry{{Name: "codex-alice-team.json", Provider: "codex"}}
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (create): %v", err)
	}
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Calling ensureFresh again with the exact same entries must not
	// rewrite the file (mtime should not advance).
	if err := m.ensureFresh(entries); err != nil {
		t.Fatalf("ensureFresh (no-op): %v", err)
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("expected no rewrite when nothing changed: mtime1=%v mtime2=%v", info1.ModTime(), info2.ModTime())
	}
}

func TestResolveWarmupFilePathDefaultsAndOverrides(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p, err := resolveWarmupFilePath(pluginConfig{})
	if err != nil {
		t.Fatalf("resolveWarmupFilePath: %v", err)
	}
	if want := filepath.Join(cwd, warmupFileName); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}

	p, err = resolveWarmupFilePath(pluginConfig{ConfigFilePath: "custom.yaml"})
	if err != nil {
		t.Fatalf("resolveWarmupFilePath: %v", err)
	}
	if want := filepath.Join(cwd, "custom.yaml"); p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}

	p, err = resolveWarmupFilePath(pluginConfig{ConfigFilePath: "/abs/custom.yaml"})
	if err != nil {
		t.Fatalf("resolveWarmupFilePath: %v", err)
	}
	if p != "/abs/custom.yaml" {
		t.Fatalf("path = %q, want /abs/custom.yaml", p)
	}
}

func TestResolveFileAuthRespectsEnabledAndDefaults(t *testing.T) {
	data := warmupFileData{
		Defaults: warmupFileDefaults{Time: flexStringList{"05:30"}, Model: "auto"},
		Accounts: map[string]warmupFileAccount{
			"a.json": {Enabled: boolPtr(true)},
			"b.json": {Enabled: boolPtr(false)},
		},
	}
	a := resolveFileAuth(data, "a.json", "codex")
	if !a.Selected || len(a.TimeRaw) != 1 || a.TimeRaw[0] != "05:30" || a.ModelSpec != "" || a.ModelSource != modelSourceAuto {
		t.Fatalf("a = %+v", a)
	}
	if a.ReasoningEffort != codexReasoningEffort {
		t.Fatalf("expected codex auto reasoning-effort, got %q", a.ReasoningEffort)
	}
	if b := resolveFileAuth(data, "b.json", "codex"); b.Selected {
		t.Fatalf("expected b.json (enabled=false) to not be selected, got %+v", b)
	}
	if c := resolveFileAuth(data, "c.json", "codex"); c.Selected {
		t.Fatalf("expected an account absent from the file to not be selected, got %+v", c)
	}
}

// TestMtimeTokenDistinguishesWritesWithIdenticalModTime locks in the fix for
// a real, reproducible bug found while testing this feature: two atomic
// (temp file + rename) writes executed back-to-back with no artificial
// delay were observed to land on the exact same OS-reported ModTime on this
// project's own dev filesystem, which would make /config-yaml/save's
// optimistic-concurrency check silently treat a genuine same-instant
// conflict as "unchanged" if the token were derived from ModTime alone.
// mtimeToken's writeSeq suffix must disambiguate them even when ModTime is
// identical.
func TestMtimeTokenDistinguishesWritesWithIdenticalModTime(t *testing.T) {
	same := time.Date(2026, 9, 14, 8, 0, 0, 123456789, time.UTC)
	t0 := mtimeToken(same, 0)
	t1 := mtimeToken(same, 1)
	if t0 == t1 {
		t.Fatalf("mtimeToken(sameTime, 0) == mtimeToken(sameTime, 1) == %q, want distinct tokens", t0)
	}
}

// TestOverwriteRawAlwaysProducesADistinctToken exercises the realistic path
// (two real writes through the manager, not a synthetic ModTime) and asserts
// the invariant the fix above guarantees unconditionally: every successful
// overwriteRaw call returns a token that differs from the immediately
// preceding one, regardless of whether the underlying filesystem's clock
// resolution happened to collide for these two particular writes.
func TestOverwriteRawAlwaysProducesADistinctToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-warmup.yaml")
	m := newWarmupFileManager(path)
	first, err := m.overwriteRaw([]byte("defaults:\n  time: \"05:30\"\n  model: auto\naccounts: {}\n"))
	if err != nil {
		t.Fatalf("first overwriteRaw: %v", err)
	}
	second, err := m.overwriteRaw([]byte("defaults:\n  time: \"06:00\"\n  model: auto\naccounts: {}\n"))
	if err != nil {
		t.Fatalf("second overwriteRaw: %v", err)
	}
	if first == second {
		t.Fatalf("two successive overwriteRaw calls returned the same token %q, want distinct tokens", first)
	}
}
