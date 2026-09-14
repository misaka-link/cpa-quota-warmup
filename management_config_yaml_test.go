package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// base64URLNoPad mirrors the panel JS's own encoding (see panel.go's
// toBase64Url): standard base64 with the RFC 4648 §5 substitution and no "="
// padding -- exactly base64.RawURLEncoding.
func base64URLNoPad(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestConfigYAMLReadReturnsContentPathAndMtime(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	raw := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:  # 中文注释\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}

	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var payload configYAMLPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Content != raw {
		t.Fatalf("Content = %q, want %q", payload.Content, raw)
	}
	if payload.Path != warmupPath {
		t.Fatalf("Path = %q, want %q", payload.Path, warmupPath)
	}
	if payload.Mtime == "" {
		t.Fatalf("expected a non-empty Mtime")
	}
	if payload.Error != "" {
		t.Fatalf("unexpected Error: %s", payload.Error)
	}
}

// TestConfigYAMLSaveRoundTripsChineseCommentsQuotesBackslashesAndMultiline
// covers the v0.5.0 spec's explicitly-requested round-trip case: Chinese
// comments, quotes, a backslash, a literal "#" (inside a quoted scalar, so it
// is not itself a YAML comment), and a multi-line document, all surviving
// the base64url encode (client) / decode (server) / write / re-read cycle
// byte for byte.
func TestConfigYAMLSaveRoundTripsChineseCommentsQuotesBackslashesAndMultiline(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}

	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var readPayload configYAMLPayload
	if err := json.Unmarshal(body, &readPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	edited := "defaults:\n" +
		"  time: \"05:30\"\n" +
		"  model: auto\n" +
		"accounts:\n" +
		"  a.json:  # 中文注释：预热 a 账号\n" +
		"    enabled: true\n" +
		"    time: \"05:30, 10:30\"\n" +
		"    model: auto\n" +
		"    message: \"hello \\\"world\\\" \\\\ and # not a comment\"\n"

	saveStatus, saveBody := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{
		"content": {base64URLNoPad(edited)},
		"mtime":   {readPayload.Mtime},
	})
	if saveStatus != 200 {
		t.Fatalf("config-yaml save: status=%d body=%s", saveStatus, saveBody)
	}
	var saveResult configYAMLSaveResult
	if err := json.Unmarshal(saveBody, &saveResult); err != nil {
		t.Fatalf("decode save result: %v", err)
	}
	if saveResult.Mtime == "" || saveResult.Mtime == readPayload.Mtime {
		t.Fatalf("expected a fresh Mtime after save, got %q (was %q)", saveResult.Mtime, readPayload.Mtime)
	}

	onDisk, err := os.ReadFile(warmupPath)
	if err != nil {
		t.Fatalf("read back warmup file: %v", err)
	}
	if string(onDisk) != edited {
		t.Fatalf("on-disk content after save = %q, want %q", onDisk, edited)
	}

	// And a subsequent read reflects exactly what was saved, closing the
	// round trip end to end (not just the write side).
	status, body = callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml re-read: status=%d body=%s", status, body)
	}
	var reread configYAMLPayload
	if err := json.Unmarshal(body, &reread); err != nil {
		t.Fatalf("decode re-read: %v", err)
	}
	if reread.Content != edited {
		t.Fatalf("re-read Content = %q, want %q", reread.Content, edited)
	}
}

// TestConfigYAMLSaveNormalizesCRLFToLF covers item 3's "保留 CRLF→LF 归一化"
// requirement: a Windows-side <textarea> may submit "\r\n" line endings, and
// the file on disk must always end up LF-only.
func TestConfigYAMLSaveNormalizesCRLFToLF(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}
	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var readPayload configYAMLPayload
	if err := json.Unmarshal(body, &readPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	withCRLF := "defaults:\r\n  time: \"05:30\"\r\n  model: auto\r\naccounts:\r\n  a.json:\r\n    enabled: false\r\n    time: \"05:30\"\r\n    model: auto\r\n"
	saveStatus, saveBody := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{
		"content": {base64URLNoPad(withCRLF)},
		"mtime":   {readPayload.Mtime},
	})
	if saveStatus != 200 {
		t.Fatalf("config-yaml save: status=%d body=%s", saveStatus, saveBody)
	}
	onDisk, err := os.ReadFile(warmupPath)
	if err != nil {
		t.Fatalf("read back warmup file: %v", err)
	}
	if string(onDisk) != initial {
		t.Fatalf("expected CRLF to be normalized to LF on disk, got %q", onDisk)
	}
}

func TestConfigYAMLSaveRejectsInvalidTimeExpressionWithLineAndColumn(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}
	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var readPayload configYAMLPayload
	if err := json.Unmarshal(body, &readPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	bad := "defaults:\n  time: \"not-a-time\"\n  model: auto\naccounts: {}\n"
	saveStatus, saveBody := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{
		"content": {base64URLNoPad(bad)},
		"mtime":   {readPayload.Mtime},
	})
	if saveStatus != 400 {
		t.Fatalf("expected 400 for an invalid time expression, got %d body=%s", saveStatus, saveBody)
	}
	var errResult configYAMLErrorResult
	if err := json.Unmarshal(saveBody, &errResult); err != nil {
		t.Fatalf("decode error result: %v", err)
	}
	if errResult.Error == "" {
		t.Fatalf("expected a non-empty error message")
	}
	if errResult.Line != 2 {
		t.Fatalf("Line = %d, want 2 (the defaults.time line)", errResult.Line)
	}
	if errResult.Column < 1 {
		t.Fatalf("Column = %d, want >= 1", errResult.Column)
	}

	onDisk, err := os.ReadFile(warmupPath)
	if err != nil {
		t.Fatalf("read back warmup file: %v", err)
	}
	if string(onDisk) != initial {
		t.Fatalf("validation failure must not modify the file on disk: got %q, want %q", onDisk, initial)
	}
}

func TestConfigYAMLSaveRejectsMalformedYAMLSyntax(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}
	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var readPayload configYAMLPayload
	if err := json.Unmarshal(body, &readPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	malformed := "defaults: [this is not\n  a valid: mapping\n"
	saveStatus, saveBody := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{
		"content": {base64URLNoPad(malformed)},
		"mtime":   {readPayload.Mtime},
	})
	if saveStatus != 400 {
		t.Fatalf("expected 400 for malformed YAML syntax, got %d body=%s", saveStatus, saveBody)
	}
	var errResult configYAMLErrorResult
	if err := json.Unmarshal(saveBody, &errResult); err != nil {
		t.Fatalf("decode error result: %v", err)
	}
	if errResult.Error == "" || errResult.Line < 1 || errResult.Column < 1 {
		t.Fatalf("expected a non-empty error with Line/Column >= 1, got %+v", errResult)
	}
}

func TestConfigYAMLSaveRejectsStaleMtimeAsConflict(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}

	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}
	status, body := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
	if status != 200 {
		t.Fatalf("config-yaml read: status=%d body=%s", status, body)
	}
	var readPayload configYAMLPayload
	if err := json.Unmarshal(body, &readPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Someone else (or a background tick) changes the file after our read.
	// The sleep guards against filesystems with coarse mtime resolution
	// making the two writes indistinguishable, which would make this test
	// flaky rather than the conflict-detection logic itself being wrong.
	time.Sleep(2 * time.Millisecond)
	changed := initial + "  # someone else's edit\n"
	if err := os.WriteFile(warmupPath, []byte(changed), 0o644); err != nil {
		t.Fatalf("simulate a concurrent edit: %v", err)
	}

	saveStatus, saveBody := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{
		"content": {base64URLNoPad("defaults:\n  time: \"06:00\"\n  model: auto\naccounts: {}\n")},
		"mtime":   {readPayload.Mtime},
	})
	if saveStatus != 409 {
		t.Fatalf("expected 409 for a stale mtime, got %d body=%s", saveStatus, saveBody)
	}

	onDisk, err := os.ReadFile(warmupPath)
	if err != nil {
		t.Fatalf("read back warmup file: %v", err)
	}
	if string(onDisk) != changed {
		t.Fatalf("a rejected conflicting save must not touch the file: got %q, want %q", onDisk, changed)
	}
}

func TestConfigYAMLSaveRejectsMissingParams(t *testing.T) {
	e, warmupPath := registerFileMode(t, "")
	e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}
	initial := "defaults:\n  time: \"05:30\"\n  model: auto\naccounts:\n  a.json:\n    enabled: false\n    time: \"05:30\"\n    model: auto\n"
	if err := os.WriteFile(warmupPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write warmup file: %v", err)
	}

	status, _ := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{"mtime": {"whatever"}})
	if status != 400 {
		t.Fatalf("expected 400 for a missing content param, got %d", status)
	}
	status, _ = callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{"content": {base64URLNoPad("x: 1\n")}})
	if status != 400 {
		t.Fatalf("expected 400 for a missing mtime param, got %d", status)
	}
	status, _ = callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{"content": {"not valid base64url!!"}, "mtime": {"whatever"}})
	if status != 400 {
		t.Fatalf("expected 400 for invalid base64url content, got %d", status)
	}
}

func TestConfigYAMLRoutesRejectNonFileModes(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		e := registerNewFormat(t, "timezone: UTC\nproviders:\n  antigravity: { model: m }\n")
		e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "antigravity"}}}
		status, _ := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
		if status != 501 {
			t.Fatalf("expected 501 for config-yaml under legacy mode, got %d", status)
		}
		status, _ = callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml/save", url.Values{"content": {"x"}, "mtime": {"y"}})
		if status != 501 {
			t.Fatalf("expected 501 for config-yaml/save under legacy mode, got %d", status)
		}
	})
	t.Run("v3inline", func(t *testing.T) {
		e := registerNewFormat(t, "time: \"05:30\"\naccounts: [\"*\"]\n")
		e.auths = fakeAuthLister{entries: []pluginapi.HostAuthFileEntry{{Name: "a.json", Provider: "codex"}}}
		status, _ := callManagement(t, "/v0/resource/plugins/cpa-quota-warmup/config-yaml", nil)
		if status != 501 {
			t.Fatalf("expected 501 for config-yaml under v3inline mode, got %d", status)
		}
	})
}
