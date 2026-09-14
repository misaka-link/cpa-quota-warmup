package main

import (
	"reflect"
	"testing"
)

// TestModelsForProviderMapsOwnedByAliases exercises the owned_by -> provider
// mapping table: owned_by is an upstream format family (verified against a
// live instance), not a CPA provider id, so each provider's own accepted
// owned_by set must be honored exactly as specified.
func TestModelsForProviderMapsOwnedByAliases(t *testing.T) {
	cases := []struct {
		provider string
		ownedBy  string
		want     bool
	}{
		{"antigravity", "antigravity", true},
		{"antigravity", "openai", false},
		{"codex", "openai", true},
		{"codex", "codex", true},
		{"codex", "anthropic", false},
		{"kimi", "moonshot", true},
		{"kimi", "kimi", true},
		{"kimi", "openai", false},
		{"xai", "xai", true},
		{"xai", "openai", false},
		{"claude", "anthropic", true},
		{"claude", "claude", true},
		{"claude", "openai", false},
		{"gemini-cli", "google", true},
		{"gemini-cli", "gemini", true},
		{"aistudio", "google", true},
		{"vertex", "gemini", true},
		// A provider absent from providerOwnedByAliases falls back to a
		// literal owned_by == provider match.
		{"some-openai-compat-provider", "some-openai-compat-provider", true},
		{"some-openai-compat-provider", "openai", false},
	}
	for _, c := range cases {
		available := []modelInfo{{ID: "m-" + c.ownedBy, OwnedBy: c.ownedBy}}
		got := modelsForProvider(c.provider, available)
		gotHas := len(got) == 1 && got[0] == "m-"+c.ownedBy
		if gotHas != c.want {
			t.Errorf("modelsForProvider(%q, owned_by=%q) = %v, want match=%v", c.provider, c.ownedBy, got, c.want)
		}
	}
}

// TestModelsForProviderUnionDedupAndOrder covers the union of (a) the
// provider's own candidate list (restricted to what's available, in its
// original cheapest-first order) and (b) whatever else maps to it via
// owned_by (sorted alphabetically, appended after (a)), with any model
// appearing in both counted only once.
func TestModelsForProviderUnionDedupAndOrder(t *testing.T) {
	// codex's candidates (candidates.go): gpt-5.3-codex-spark, gpt-5.6-luna,
	// gpt-5.5, gpt-5.6-terra, gpt-5.6-sol, gpt-6-astra.
	available := []modelInfo{
		{ID: "gpt-5.6-sol", OwnedBy: "openai"},         // in candidates AND owned_by openai -- must appear once
		{ID: "gpt-5.3-codex-spark", OwnedBy: "openai"}, // in candidates, cheapest -- must lead
		{ID: "zzz-custom-openai-model", OwnedBy: "openai"},
		{ID: "aaa-custom-openai-model", OwnedBy: "openai"},
		{ID: "not-codex-at-all", OwnedBy: "anthropic"}, // wrong owned_by, must be excluded
	}
	got := modelsForProvider("codex", available)
	want := []string{
		// candidates present in available, in candidates.go's own order:
		"gpt-5.3-codex-spark", "gpt-5.6-sol",
		// remainder from owned_by, alphabetical, "gpt-5.6-sol" not repeated:
		"aaa-custom-openai-model", "zzz-custom-openai-model",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelsForProvider(codex) = %v, want %v", got, want)
	}
}

// TestModelsForProviderEmptyAndUnknown covers the no-match cases: an empty
// available list, and a provider string that is blank after trimming.
func TestModelsForProviderEmptyAndUnknown(t *testing.T) {
	if got := modelsForProvider("codex", nil); got != nil {
		t.Fatalf("modelsForProvider(codex, nil) = %v, want nil", got)
	}
	if got := modelsForProvider("  ", []modelInfo{{ID: "x", OwnedBy: "openai"}}); got != nil {
		t.Fatalf("modelsForProvider(blank) = %v, want nil", got)
	}
	// A provider with a candidate list but none of its candidates (or any
	// owned_by match) present in available -> nil, not an empty non-nil
	// slice with zero elements (callers that care about JSON shape, like
	// authStatusModels, normalize that themselves).
	if got := modelsForProvider("codex", []modelInfo{{ID: "totally-unrelated", OwnedBy: "somewhere-else"}}); got != nil {
		t.Fatalf("modelsForProvider(codex, unrelated) = %v, want nil", got)
	}
}

// TestModelsForProviderIsCaseInsensitive matches providerCandidateList's own
// case-insensitivity (selectModel/providerCandidateList already lower-case
// the provider before lookup).
func TestModelsForProviderIsCaseInsensitive(t *testing.T) {
	got := modelsForProvider("CoDeX", []modelInfo{{ID: "gpt-5.3-codex-spark", OwnedBy: "OpenAI"}})
	want := []string{"gpt-5.3-codex-spark"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modelsForProvider(CoDeX) = %v, want %v", got, want)
	}
}
