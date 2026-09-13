package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	tickInterval          = 30 * time.Second
	roundCoveragePoll     = 200 * time.Millisecond
	roundCoverageWindow   = 3 * time.Second
	usageWindowSlop       = 2 * time.Second
	manualTriggerCooldown = 60 * time.Second
)

// dueTarget is one auth file whose effective schedule has a slot due right
// now (or being forced by a manual trigger).
type dueTarget struct {
	Name            string
	AuthID          string
	Provider        string
	Model           string
	ReasoningEffort string
	HHMM            string
	SlotAt          time.Time
	DateKey         string
}

// groupDueTargets walks the host's current auth list, resolves each one's
// effective schedule, and returns the ones due at now (crossed their slot,
// still inside catchUp, not already recorded for today) grouped by provider.
// Disabled/unavailable auths and auths with no resolvable model are skipped
// (the latter logged by the caller, since this function has no logger).
func groupDueTargets(entries []pluginapi.HostAuthFileEntry, cfg pluginConfig, now time.Time, catchUp time.Duration, state *stateStore) (map[string][]dueTarget, []string) {
	groups := make(map[string][]dueTarget)
	var skippedNoModel []string
	for _, entry := range entries {
		if entry.Disabled || entry.Unavailable {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		effective, ok := resolveAuthConfig(cfg, name, entry.Provider)
		if !ok {
			if strings.TrimSpace(entry.Provider) != "" {
				providerDef := cfg.Providers[strings.ToLower(strings.TrimSpace(entry.Provider))]
				if strings.TrimSpace(providerDef.Model) == "" {
					skippedNoModel = append(skippedNoModel, fmt.Sprintf("%s(provider=%s)", name, entry.Provider))
				}
			}
			continue
		}
		for _, hhmm := range effective.Times {
			slot, due := isDue(now, hhmm, cfg.location, catchUp)
			if !due {
				continue
			}
			dateKey := slotDateKey(slot, cfg.location)
			if state.isRecorded(name, dateKey, hhmm) {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(entry.Provider))
			target := dueTarget{
				Name:            name,
				AuthID:          entry.ID,
				Provider:        provider,
				Model:           effective.Model,
				ReasoningEffort: effective.ReasoningEffort,
				HHMM:            hhmm,
				SlotAt:          slot,
				DateKey:         dateKey,
			}
			groups[provider] = append(groups[provider], target)
		}
	}
	return groups, skippedNoModel
}

// filterUnavailableModels splits due targets into ones whose Model is present
// in available (sendable) and ones that are not (unavailable). Callers must
// only invoke this once GET /v1/models has actually succeeded; when the
// precheck itself fails, use groups unfiltered instead of calling this at
// all -- an unreachable precheck must never block a warmup request that
// would otherwise have gone out.
func filterUnavailableModels(groups map[string][]dueTarget, available map[string]bool) (sendable map[string][]dueTarget, unavailable []dueTarget) {
	sendable = make(map[string][]dueTarget, len(groups))
	for provider, targets := range groups {
		for _, t := range targets {
			if available[t.Model] {
				sendable[provider] = append(sendable[provider], t)
			} else {
				unavailable = append(unavailable, t)
			}
		}
	}
	return sendable, unavailable
}

// unavailableModelWarning is the Warning text recorded/logged for a due
// target whose configured model the precheck found this CPA instance does
// not currently expose.
func unavailableModelWarning(model string) string {
	return fmt.Sprintf("model %s not exposed by this CPA instance (see GET /v1/models)", model)
}

// roundOutcome is what a warmup round loop learned about one due target.
type roundOutcome struct {
	Covered    bool
	Rounds     int
	StatusCode int
	Warning    string
}

// runProviderGroup fires up to maxRounds rounds of warmup requests for one
// provider's set of due targets, sending len(pending) requests per round
// (one per not-yet-covered target) so the host's round-robin/affinity
// selector has a chance to spread them across every account, then checking
// usage.handle coverage by AuthID after each round. Targets still uncovered
// after maxRounds are returned with a Warning.
func runProviderGroup(ctx context.Context, sender chatSender, ring *usageRing, message string, maxTokens, maxRounds int, targets []dueTarget) map[string]roundOutcome {
	outcomes := make(map[string]roundOutcome, len(targets))
	pending := append([]dueTarget(nil), targets...)
	hitStatus := make(map[string]int) // AuthID -> observed status code

	round := 0
	for len(pending) > 0 && round < maxRounds {
		round++
		tags := make([]string, len(pending))
		wantAuthIDs := make(map[string]bool, len(pending))
		windowStart := time.Now()
		for i, t := range pending {
			tag := newSessionTag()
			tags[i] = tag
			wantAuthIDs[t.AuthID] = true
			result := sender.sendWarmup(ctx, warmupSendRequest{
				Model:           t.Model,
				ReasoningEffort: t.ReasoningEffort,
				SessionTag:      tag,
				Message:         message,
				MaxTokens:       maxTokens,
			})
			if result.Err != nil {
				hostLog("warn", fmt.Sprintf("warmup request for provider=%s model=%s failed to send: %v", t.Provider, t.Model, result.Err))
			}
		}
		windowEnd := time.Now()

		hitAuthIDs := waitForSessionCoverage(ring, tags, wantAuthIDs, roundCoverageWindow)
		for authID, entry := range hitAuthIDs {
			hitStatus[authID] = entry.StatusCode
		}
		// Fallback: a host build that does not preserve our session tag would
		// leave hitAuthIDs empty even though real usage records exist; try a
		// time+model correlation instead so coverage checking degrades rather
		// than silently never completing.
		if len(hitAuthIDs) == 0 {
			claimed := map[string]bool{}
			for _, t := range pending {
				if entry, ok := ring.byWindow(t.Model, windowStart, windowEnd, usageWindowSlop, claimed); ok {
					claimed[entry.AuthID] = true
					hitStatus[entry.AuthID] = entry.StatusCode
				}
			}
		}

		next := pending[:0]
		for _, t := range pending {
			if _, ok := hitStatus[t.AuthID]; ok {
				outcomes[t.Name] = roundOutcome{Covered: true, Rounds: round, StatusCode: hitStatus[t.AuthID]}
				continue
			}
			next = append(next, t)
		}
		pending = next
	}

	for _, t := range pending {
		outcomes[t.Name] = roundOutcome{
			Covered: false,
			Rounds:  round,
			Warning: fmt.Sprintf("not covered by any usage record after %d round(s)", round),
		}
	}
	return outcomes
}

// waitForSessionCoverage polls ring for up to timeout, collecting every usage
// entry recorded under any of tags, and returns them keyed by AuthID.
//
// A single tag (one outbound warmup request) can produce more than one usage
// record: the host may retry that same request against a second account
// after the first attempt fails (observed in production -- a codex request
// hit a team account's usage_limit_reached 429, then the host retried the
// same request, same SessionID, against a different account and got a 200).
// Stopping at the first match per tag would silently miss whichever account
// was not the last one to respond, so this keeps re-scanning every tag for
// the whole window rather than retiring a tag on its first hit. It only
// returns early once every AuthID in wantAuthIDs has been observed; failed
// attempts count as observed too (a 429 still means the account's window was
// touched), only the caller decides what "covered" means from the status
// code.
func waitForSessionCoverage(ring *usageRing, tags []string, wantAuthIDs map[string]bool, timeout time.Duration) map[string]usageEntry {
	deadline := time.Now().Add(timeout)
	found := make(map[string]usageEntry)
	for {
		for _, tag := range tags {
			for _, entry := range ring.allBySessionTag(tag) {
				if _, already := found[entry.AuthID]; !already {
					found[entry.AuthID] = entry
				}
			}
		}
		allCovered := true
		for authID := range wantAuthIDs {
			if _, ok := found[authID]; !ok {
				allCovered = false
				break
			}
		}
		if allCovered || time.Now().After(deadline) {
			return found
		}
		time.Sleep(roundCoveragePoll)
	}
}

// engine owns the background scheduler goroutine and everything it needs:
// the live config, the usage ring, and the persisted schedule state.
type engine struct {
	settings   atomic.Pointer[pluginConfig]
	ring       *usageRing
	state      *stateStore
	auths      authLister
	newSender  func(baseURL, apiKey string) warmupClient
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	manualMu   sync.Mutex
	manualLast map[string]time.Time
	lastTickMu sync.Mutex
	lastTick   time.Time
	lastError  string
}

// authLister abstracts host.auth.list so tests can supply a fixed list
// instead of making a real cgo call.
type authLister interface {
	ListAuths() ([]pluginapi.HostAuthFileEntry, error)
}

type hostAuthLister struct{}

func (hostAuthLister) ListAuths() ([]pluginapi.HostAuthFileEntry, error) { return hostAuthList() }

func newEngine(cfg pluginConfig, state *stateStore, auths authLister) *engine {
	e := &engine{
		ring:       newUsageRing(),
		state:      state,
		auths:      auths,
		newSender:  func(baseURL, apiKey string) warmupClient { return newHTTPChatSender(baseURL, apiKey) },
		manualLast: map[string]time.Time{},
	}
	e.settings.Store(&cfg)
	return e
}

func (e *engine) start() {
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				e.tick()
			}
		}
	}()
}

func (e *engine) stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

func (e *engine) config() pluginConfig {
	return *e.settings.Load()
}

func (e *engine) tick() {
	cfg := e.config()
	if !cfg.Enabled {
		return
	}
	entries, err := e.auths.ListAuths()
	e.lastTickMu.Lock()
	e.lastTick = time.Now()
	if err != nil {
		e.lastError = err.Error()
	} else {
		e.lastError = ""
	}
	e.lastTickMu.Unlock()
	if err != nil {
		if cfg.Log {
			hostLog("warn", fmt.Sprintf("host.auth.list failed, skipping this tick: %v", err))
		}
		return
	}

	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
	now := time.Now().In(cfg.location)
	groups, skippedNoModel := groupDueTargets(entries, cfg, now, catchUp, e.state)
	for _, s := range skippedNoModel {
		if cfg.Log {
			hostLog("warn", fmt.Sprintf("auth %s has no configured model for its provider, skipping", s))
		}
	}
	if len(groups) == 0 {
		return
	}

	apiKey, err := resolveAPIKey(cfg)
	if err != nil {
		hostLog("error", fmt.Sprintf("cannot resolve api-key, skipping %d due provider group(s): %v", len(groups), err))
		return
	}
	sender := e.newSender(cfg.BaseURL, apiKey)

	sendGroups := groups
	if available, errModels := sender.AvailableModels(e.ctx); errModels == nil {
		var unavailable []dueTarget
		sendGroups, unavailable = filterUnavailableModels(groups, available)
		for _, t := range unavailable {
			rec := slotRecord{
				Auth:        t.Name,
				Date:        t.DateKey,
				Time:        t.HHMM,
				Provider:    t.Provider,
				Model:       t.Model,
				TriggeredAt: t.SlotAt,
				Covered:     false,
				Warning:     unavailableModelWarning(t.Model),
			}
			if err := e.state.record(rec, now, cfg.location); err != nil && cfg.Log {
				hostLog("error", fmt.Sprintf("failed to persist state for auth=%s slot=%s: %v", t.Name, t.HHMM, err))
			}
			if cfg.Log {
				hostLog("warn", fmt.Sprintf("auth=%s provider=%s model=%s slot=%s: %s", t.Name, t.Provider, t.Model, t.HHMM, rec.Warning))
			}
		}
	} else if cfg.Log {
		hostLog("warn", fmt.Sprintf("model precheck (GET /v1/models) failed, sending warmup requests without it: %v", errModels))
	}

	for provider, targets := range sendGroups {
		outcomes := runProviderGroup(e.ctx, sender, e.ring, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
		for _, t := range targets {
			outcome := outcomes[t.Name]
			rec := slotRecord{
				Auth:        t.Name,
				Date:        t.DateKey,
				Time:        t.HHMM,
				Provider:    provider,
				Model:       t.Model,
				TriggeredAt: t.SlotAt,
				Covered:     outcome.Covered,
				Rounds:      outcome.Rounds,
				StatusCode:  outcome.StatusCode,
				Warning:     outcome.Warning,
			}
			if err := e.state.record(rec, now, cfg.location); err != nil && cfg.Log {
				hostLog("error", fmt.Sprintf("failed to persist state for auth=%s slot=%s: %v", t.Name, t.HHMM, err))
			}
			if cfg.Log {
				if outcome.Covered {
					hostLog("info", fmt.Sprintf("warmed auth=%s provider=%s model=%s slot=%s status=%d rounds=%d",
						t.Name, provider, t.Model, t.HHMM, outcome.StatusCode, outcome.Rounds))
				} else {
					hostLog("warn", fmt.Sprintf("warmup did not cover auth=%s provider=%s model=%s slot=%s: %s",
						t.Name, provider, t.Model, t.HHMM, outcome.Warning))
				}
			}
		}
	}
}

// manualRunResult is what management.go's POST run route reports back.
type manualRunResult struct {
	Attempted []string                `json:"attempted"`
	Skipped   map[string]string       `json:"skipped,omitempty"`
	Outcomes  map[string]roundOutcome `json:"outcomes,omitempty"`
}

// manualTrigger runs an immediate, out-of-schedule warmup for every enabled
// auth matching authGlob (empty matches all), ignoring the current
// time-of-day gate but still honoring each auth's 60-second manual-trigger
// cooldown. It does not touch the persisted schedule state: a manual run is
// deliberately independent of whether today's real slot has already fired.
func (e *engine) manualTrigger(ctx context.Context, authGlob string) (manualRunResult, error) {
	cfg := e.config()
	entries, err := e.auths.ListAuths()
	if err != nil {
		return manualRunResult{}, fmt.Errorf("host.auth.list: %w", err)
	}

	result := manualRunResult{Skipped: map[string]string{}, Outcomes: map[string]roundOutcome{}}
	groups := make(map[string][]dueTarget)
	targetByName := make(map[string]dueTarget)
	now := time.Now()

	e.manualMu.Lock()
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || !stateKeyProviderGlob(authGlob, name) {
			continue
		}
		if entry.Disabled || entry.Unavailable {
			result.Skipped[name] = "disabled or unavailable"
			continue
		}
		effective, ok := resolveAuthConfig(cfg, name, entry.Provider)
		if !ok {
			result.Skipped[name] = "not enabled or no resolvable model"
			continue
		}
		if last, seen := e.manualLast[name]; seen && now.Sub(last) < manualTriggerCooldown {
			result.Skipped[name] = "manual trigger cooldown (60s) not elapsed"
			continue
		}
		e.manualLast[name] = now
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		target := dueTarget{
			Name:            name,
			AuthID:          entry.ID,
			Provider:        provider,
			Model:           effective.Model,
			ReasoningEffort: effective.ReasoningEffort,
			HHMM:            "manual",
			SlotAt:          now,
			DateKey:         slotDateKey(now, cfg.location),
		}
		groups[provider] = append(groups[provider], target)
		targetByName[name] = target
		result.Attempted = append(result.Attempted, name)
	}
	e.manualMu.Unlock()

	if len(groups) == 0 {
		return result, nil
	}

	apiKey, err := resolveAPIKey(cfg)
	if err != nil {
		return manualRunResult{}, fmt.Errorf("resolve api-key: %w", err)
	}
	sender := e.newSender(cfg.BaseURL, apiKey)

	sendGroups := groups
	if available, errModels := sender.AvailableModels(ctx); errModels == nil {
		var unavailable []dueTarget
		sendGroups, unavailable = filterUnavailableModels(groups, available)
		for _, t := range unavailable {
			outcome := roundOutcome{Covered: false, Warning: unavailableModelWarning(t.Model)}
			result.Outcomes[t.Name] = outcome
			if cfg.Log {
				hostLog("warn", fmt.Sprintf("auth=%s provider=%s model=%s slot=manual: %s", t.Name, t.Provider, t.Model, outcome.Warning))
			}
		}
	} else if cfg.Log {
		hostLog("warn", fmt.Sprintf("model precheck (GET /v1/models) failed, sending warmup requests without it: %v", errModels))
	}

	for _, targets := range sendGroups {
		outcomes := runProviderGroup(ctx, sender, e.ring, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
		for name, outcome := range outcomes {
			result.Outcomes[name] = outcome
			if !cfg.Log {
				continue
			}
			t := targetByName[name]
			if outcome.Covered {
				hostLog("info", fmt.Sprintf("warmed auth=%s provider=%s model=%s slot=manual status=%d rounds=%d",
					name, t.Provider, t.Model, outcome.StatusCode, outcome.Rounds))
			} else {
				hostLog("warn", fmt.Sprintf("warmup did not cover auth=%s provider=%s model=%s slot=manual: %s",
					name, t.Provider, t.Model, outcome.Warning))
			}
		}
	}
	return result, nil
}
