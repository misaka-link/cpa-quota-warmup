package main

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"codex-*-prolite.json", "codex-acct-prolite.json", true},
		{"codex-*-prolite.json", "codex-acct-team.json", false},
		{"antigravity-alice@example.com.json", "antigravity-alice@example.com.json", true},
		{"antigravity-alice@example.com.json", "antigravity-bob@example.com.json", false},
		{"*", "anything.json", true},
		{"", "anything.json", false},
		{"[", "anything.json", false}, // malformed pattern never matches, never panics
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
