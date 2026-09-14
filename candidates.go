package main

import "strings"

// providerCandidates is the built-in "cheapest first" model candidate list
// per provider (v0.3.0), used when no explicit model is configured anywhere
// in the resolution chain (see resolveModelSpec in config.go). The first
// candidate present in a live GET /v1/models result is selected; when the
// precheck itself is unreachable, candidate[0] is used instead (see
// selectModel below). Lists are exactly as specified by the coordinator and
// must not be reordered or edited without an explicit request.
var providerCandidates = map[string][]string{
	"codex":       {"gpt-5.3-codex-spark", "gpt-5.6-luna", "gpt-5.5", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"},
	"antigravity": {"gemini-3.1-flash-lite", "gemini-3-flash", "gemini-3.6-flash-high", "gemini-3.7-flash-high", "gemini-3.8-flash-high", "claude-sonnet-4-6"},
	"kimi":        {"kimi-k2", "kimi-k2.5", "kimi-k2.6", "kimi-k2.8", "kimi-k2.7-code", "kimi-k2.8-code", "kimi-k3", "kimi-k3-256k"},
	"xai":         {"grok-3-mini", "grok-3-mini-fast", "grok-4.3", "grok-4.5", "grok-4.6", "grok-build-0.1"},
	"claude":      {"claude-3-5-haiku-20241022", "claude-haiku-4-5-20251001", "claude-sonnet-4-6"},
	"gemini-cli":  {"gemini-2.5-flash-lite", "gemini-2.5-flash", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-3.5-flash-lite", "gemini-3.5-flash"},
	"aistudio":    {"gemini-2.5-flash-lite", "gemini-2.5-flash", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-3.5-flash-lite", "gemini-3.5-flash"},
	"vertex":      {"gemini-2.5-flash-lite", "gemini-2.5-flash", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-3.5-flash-lite", "gemini-3.5-flash"},
}

// codexReasoningEffort is auto-attached to every codex warmup request in the
// new (v0.3.0) config format, unless something else in the resolution chain
// already set a reasoning effort. The legacy format's explicit
// providers.codex.reasoning-effort / auths[].reasoning-effort keys are
// unaffected by this and keep working exactly as before.
const codexReasoningEffort = "low"

// providerCandidateList returns the built-in candidate list for provider
// (case-insensitive), or nil if the provider has no known candidate list.
func providerCandidateList(provider string) []string {
	return providerCandidates[strings.ToLower(strings.TrimSpace(provider))]
}

// selectModel picks a model for provider from its built-in candidate list.
//
//   - precheckOK=false (GET /v1/models itself failed/unreachable): always
//     returns candidate[0], per spec -- an unreachable precheck must never
//     block a warmup that would otherwise go out.
//   - precheckOK=true: returns the first candidate present in available.
//     ok=false when the provider has no candidate list, or precheck
//     succeeded but none of the candidates are currently exposed (the
//     caller should skip+warn, same as an explicitly-configured unavailable
//     model).
func selectModel(provider string, available map[string]bool, precheckOK bool) (model string, ok bool) {
	candidates := providerCandidateList(provider)
	if len(candidates) == 0 {
		return "", false
	}
	if !precheckOK {
		return candidates[0], true
	}
	for _, c := range candidates {
		if available[c] {
			return c, true
		}
	}
	return "", false
}
