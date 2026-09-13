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
		{"antigravity-szxypy@gmail.com.json", "antigravity-szxypy@gmail.com.json", true},
		{"antigravity-szxypy@gmail.com.json", "antigravity-other@gmail.com.json", false},
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
