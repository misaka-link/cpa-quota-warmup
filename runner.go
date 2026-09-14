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
func unavailableModelWarning(l lang, model string) string {
	return tr(l, msgModelNotExposed, model)
}

// modelUnavailableWarning is unavailableModelWarning's new-format
// counterpart: t.Model is empty for a due target whose "auto" candidate-list
// resolution found nothing live (see resolveAutoModels), in which case there
// is no single model name to report -- msgNoCandidateModelAvailable names
// the provider's whole candidate list instead.
func modelUnavailableWarning(l lang, t dueTarget) string {
	if strings.TrimSpace(t.Model) == "" {
		return tr(l, msgNoCandidateModelAvailable, t.Provider)
	}
	return unavailableModelWarning(l, t.Model)
}

// groupDueTargetsNew is the new-format (v0.3.0) equivalent of
// groupDueTargets: it resolves each host auth against accounts[]/time/model
// (resolveNewAuth in config.go), evaluates its cron/HH:MM expression(s)
// against now, and returns due targets grouped by provider. A due target's
// Model is left empty when its resolution tier was "auto" -- see
// resolveAutoModels, which must run after GET /v1/models has been
// attempted, since the actual candidate can only be picked once the live
// model list (or its absence) is known.
func groupDueTargetsNew(entries []pluginapi.HostAuthFileEntry, cfg pluginConfig, ov modelOverrides, now time.Time, catchUp time.Duration, state *stateStore) (map[string][]dueTarget, []string) {
	resolve := func(name, provider string) newAuthResolution { return resolveNewAuth(cfg, ov, name, provider) }
	return groupDueTargetsFromResolver(entries, resolve, cfg.location, now, catchUp, state)
}

// groupDueTargetsFile is groupDueTargetsNew's v0.4.0 file-mode counterpart:
// schedule/model resolution comes from the maintained quota-warmup.yaml
// (resolveFileAuth in warmupfile.go) instead of cfg.Accounts/TimeRaw/Model.
func groupDueTargetsFile(entries []pluginapi.HostAuthFileEntry, data warmupFileData, loc *time.Location, now time.Time, catchUp time.Duration, state *stateStore) (map[string][]dueTarget, []string) {
	resolve := func(name, provider string) newAuthResolution { return resolveFileAuth(data, name, provider) }
	return groupDueTargetsFromResolver(entries, resolve, loc, now, catchUp, state)
}

// groupDueTargetsFromResolver is the shared cron-evaluation/state-dedup
// engine behind both groupDueTargetsNew (v3InlineMode) and
// groupDueTargetsFile (v0.4.0 file mode): given a per-account resolver, walk
// the host's auth list, evaluate each selected account's time expression(s)
// against now, and return the due ones grouped by provider. A due target's
// Model is left empty when its resolution tier was "auto" -- see
// resolveAutoModels, which must run after GET /v1/models has been
// attempted, since the actual candidate can only be picked once the live
// model list (or its absence) is known.
func groupDueTargetsFromResolver(entries []pluginapi.HostAuthFileEntry, resolve func(name, provider string) newAuthResolution, loc *time.Location, now time.Time, catchUp time.Duration, state *stateStore) (groups map[string][]dueTarget, invalidExprs []string) {
	groups = make(map[string][]dueTarget)
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Disabled || entry.Unavailable {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		res := resolve(name, entry.Provider)
		if !res.Selected {
			continue
		}
		exprs, invalid := parseTimeExprs(res.TimeRaw)
		for _, bad := range invalid {
			invalidExprs = append(invalidExprs, fmt.Sprintf("%s: %q", name, bad))
		}
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		for _, expr := range exprs {
			slot, due := expr.lastTriggerAtOrBefore(now, loc, catchUp)
			if !due {
				continue
			}
			hhmm := slot.Format("15:04")
			dateKey := slotDateKey(slot, loc)
			dedupKey := name + "|" + dateKey + "|" + hhmm
			if seen[dedupKey] || state.isRecorded(name, dateKey, hhmm) {
				continue
			}
			seen[dedupKey] = true
			groups[provider] = append(groups[provider], dueTarget{
				Name:            name,
				AuthID:          entry.ID,
				Provider:        provider,
				Model:           res.ModelSpec, // "" means auto -- resolved by resolveAutoModels
				ReasoningEffort: res.ReasoningEffort,
				HHMM:            hhmm,
				SlotAt:          slot,
				DateKey:         dateKey,
			})
		}
	}
	return groups, invalidExprs
}

// resolveAutoModels fills in the Model field for every due target whose
// resolution tier was "auto" (Model == ""), using the provider's built-in
// candidate list (candidates.go) against the live GET /v1/models result.
// precheckOK indicates whether that call itself succeeded (even with zero
// models) -- see selectModel for the fallback-to-candidate[0] behavior when
// it did not. Targets with an already-explicit Model pass through
// unchanged (their own live-availability check happens afterwards, via the
// same filterUnavailableModels the legacy path uses).
func resolveAutoModels(groups map[string][]dueTarget, available map[string]bool, precheckOK bool) (resolved map[string][]dueTarget, unresolved []dueTarget) {
	resolved = make(map[string][]dueTarget, len(groups))
	for provider, targets := range groups {
		for _, t := range targets {
			if strings.TrimSpace(t.Model) != "" {
				resolved[provider] = append(resolved[provider], t)
				continue
			}
			if model, ok := selectModel(provider, available, precheckOK); ok {
				t.Model = model
				resolved[provider] = append(resolved[provider], t)
			} else {
				unresolved = append(unresolved, t)
			}
		}
	}
	return resolved, unresolved
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
func runProviderGroup(ctx context.Context, sender chatSender, ring *usageRing, l lang, message string, maxTokens, maxRounds int, targets []dueTarget) map[string]roundOutcome {
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
				hostLog("warn", tr(l, msgWarmupSendFailed, t.Provider, t.Model, result.Err))
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
			Warning: tr(l, msgNotCoveredAfterRounds, round),
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
	settings  atomic.Pointer[pluginConfig]
	ring      *usageRing
	state     *stateStore
	overrides *overridesStore
	// warmupFile is non-nil only in v0.4.0 file mode (see
	// pluginConfig.legacyMode/v3InlineMode): it owns the externally
	// maintained quota-warmup.yaml. nil in legacy/v3InlineMode.
	warmupFile *warmupFileManager
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

func newEngine(cfg pluginConfig, state *stateStore, overrides *overridesStore, auths authLister) *engine {
	e := &engine{
		ring:       newUsageRing(),
		state:      state,
		overrides:  overrides,
		auths:      auths,
		newSender:  func(baseURL, apiKey string) warmupClient { return newHTTPChatSender(baseURL, apiKey) },
		manualLast: map[string]time.Time{},
	}
	e.settings.Store(&cfg)
	return e
}

// overridesSnapshot returns the current panel model-override state, or the
// zero value if this engine was built without an overrides store (e.g. some
// older/simplified test helpers).
func (e *engine) overridesSnapshot() modelOverrides {
	if e.overrides == nil {
		return modelOverrides{}
	}
	return e.overrides.snapshot()
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
	l := logLanguage(cfg)
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
			hostLog("warn", tr(l, msgAuthListFailed, err))
		}
		return
	}

	switch {
	case cfg.legacyMode:
		e.tickLegacy(cfg, l, entries)
	case cfg.v3InlineMode:
		e.tickNew(cfg, l, entries)
	default:
		e.tickFile(cfg, l, entries)
	}
}

// tickLegacy is the pre-v0.3.0 scheduling/model-resolution flow, unchanged.
func (e *engine) tickLegacy(cfg pluginConfig, l lang, entries []pluginapi.HostAuthFileEntry) {
	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
	now := time.Now().In(cfg.location)
	groups, skippedNoModel := groupDueTargets(entries, cfg, now, catchUp, e.state)
	for _, s := range skippedNoModel {
		if cfg.Log {
			hostLog("warn", tr(l, msgNoModelForProvider, s))
		}
	}
	if len(groups) == 0 {
		return
	}

	apiKey, err := resolveAPIKey(cfg)
	if err != nil {
		hostLog("error", tr(l, msgAPIKeyResolveFailed, len(groups), err))
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
				Warning:     unavailableModelWarning(l, t.Model),
			}
			if err := e.state.record(rec, now, cfg.location); err != nil && cfg.Log {
				hostLog("error", tr(l, msgStatePersistFailed, t.Name, t.HHMM, err))
			}
			if cfg.Log {
				hostLog("warn", tr(l, msgAuthModelUnavailable, t.Name, t.Provider, t.Model, t.HHMM, rec.Warning))
			}
		}
	} else if cfg.Log {
		hostLog("warn", tr(l, msgModelPrecheckFailed, errModels))
	}

	for provider, targets := range sendGroups {
		outcomes := runProviderGroup(e.ctx, sender, e.ring, l, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
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
				hostLog("error", tr(l, msgStatePersistFailed, t.Name, t.HHMM, err))
			}
			if cfg.Log {
				if outcome.Covered {
					hostLog("info", tr(l, msgWarmedAccount, t.Name, provider, t.Model, t.HHMM, outcome.StatusCode, outcome.Rounds))
				} else {
					hostLog("warn", tr(l, msgWarmupDidNotCover, t.Name, provider, t.Model, t.HHMM, outcome.Warning))
				}
			}
		}
	}
}

// tickNew is the v0.3.0 scheduling/model-resolution flow: accounts[]/time
// cron-based due detection (groupDueTargetsNew), then candidate-list
// auto-model-selection against a live GET /v1/models (resolveAutoModels),
// then the same explicit-model precheck filter the legacy path uses
// (filterUnavailableModels), then the same send/coverage/persist loop.
func (e *engine) tickNew(cfg pluginConfig, l lang, entries []pluginapi.HostAuthFileEntry) {
	if len(cfg.Accounts) == 0 {
		if cfg.Log {
			hostLog("info", tr(l, msgNoAccountsConfigured))
		}
		return
	}
	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
	now := time.Now().In(cfg.location)
	ov := e.overridesSnapshot()
	groups, invalidExprs := groupDueTargetsNew(entries, cfg, ov, now, catchUp, e.state)
	for _, w := range invalidExprs {
		if cfg.Log {
			hostLog("warn", tr(l, msgInvalidTimeExpr, w))
		}
	}
	e.runTickForGroups(cfg, l, now, groups)
}

// tickFile is the v0.4.0 file-mode scheduling flow: it keeps the maintained
// quota-warmup.yaml in sync with the current host auth list (generating it
// on first use, appending new accounts, annotating vanished ones), then
// resolves due targets against whatever it last understood -- a parse
// failure just now is logged but never clears the last-known-good schedule
// (see warmupFileManager's doc comment), so a typo in the file can never
// stop the scheduler.
func (e *engine) tickFile(cfg pluginConfig, l lang, entries []pluginapi.HostAuthFileEntry) {
	if e.warmupFile == nil {
		return
	}
	if err := e.warmupFile.ensureFresh(entries); err != nil && cfg.Log {
		hostLog("error", tr(l, msgWarmupFileError, e.warmupFile.path, err))
	}
	data, parseErr := e.warmupFile.snapshot()
	if parseErr != "" && cfg.Log {
		hostLog("error", tr(l, msgWarmupFileParseFailed, e.warmupFile.path, parseErr))
	}

	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
	now := time.Now().In(cfg.location)
	groups, invalidExprs := groupDueTargetsFile(entries, data, cfg.location, now, catchUp, e.state)
	for _, w := range invalidExprs {
		if cfg.Log {
			hostLog("warn", tr(l, msgInvalidTimeExpr, w))
		}
	}
	e.runTickForGroups(cfg, l, now, groups)
}

// runTickForGroups is the shared tail of tickNew and tickFile: it resolves
// any still-"auto" models against a live GET /v1/models, prechecks explicit
// ones, sends the sendable groups, and persists/logs every outcome
// (including the ones dropped because of an unavailable model).
func (e *engine) runTickForGroups(cfg pluginConfig, l lang, now time.Time, groups map[string][]dueTarget) {
	if len(groups) == 0 {
		return
	}

	apiKey, err := resolveAPIKey(cfg)
	if err != nil {
		hostLog("error", tr(l, msgAPIKeyResolveFailed, len(groups), err))
		return
	}
	sender := e.newSender(cfg.BaseURL, apiKey)

	available, errModels := sender.AvailableModels(e.ctx)
	precheckOK := errModels == nil
	if !precheckOK && cfg.Log {
		hostLog("warn", tr(l, msgModelPrecheckFailed, errModels))
	}
	resolvedGroups, autoUnresolved := resolveAutoModels(groups, available, precheckOK)
	sendGroups := resolvedGroups
	var explicitUnavailable []dueTarget
	if precheckOK {
		sendGroups, explicitUnavailable = filterUnavailableModels(resolvedGroups, available)
	}
	unavailable := append(append([]dueTarget(nil), autoUnresolved...), explicitUnavailable...)
	for _, t := range unavailable {
		warning := modelUnavailableWarning(l, t)
		rec := slotRecord{
			Auth:        t.Name,
			Date:        t.DateKey,
			Time:        t.HHMM,
			Provider:    t.Provider,
			Model:       t.Model,
			TriggeredAt: t.SlotAt,
			Covered:     false,
			Warning:     warning,
		}
		if err := e.state.record(rec, now, cfg.location); err != nil && cfg.Log {
			hostLog("error", tr(l, msgStatePersistFailed, t.Name, t.HHMM, err))
		}
		if cfg.Log {
			hostLog("warn", tr(l, msgAuthModelUnavailable, t.Name, t.Provider, t.Model, t.HHMM, warning))
		}
	}

	for provider, targets := range sendGroups {
		outcomes := runProviderGroup(e.ctx, sender, e.ring, l, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
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
				hostLog("error", tr(l, msgStatePersistFailed, t.Name, t.HHMM, err))
			}
			if cfg.Log {
				if outcome.Covered {
					hostLog("info", tr(l, msgWarmedAccount, t.Name, provider, t.Model, t.HHMM, outcome.StatusCode, outcome.Rounds))
				} else {
					hostLog("warn", tr(l, msgWarmupDidNotCover, t.Name, provider, t.Model, t.HHMM, outcome.Warning))
				}
			}
		}
	}
}

// manualRunResult is what management.go's run route reports back.
type manualRunResult struct {
	Lang      string                  `json:"lang"`
	Attempted []string                `json:"attempted"`
	Skipped   map[string]string       `json:"skipped,omitempty"`
	Outcomes  map[string]roundOutcome `json:"outcomes,omitempty"`
}

// manualTrigger runs an immediate, out-of-schedule warmup for every enabled
// auth matching authGlob (empty matches all), ignoring the current
// time-of-day gate but still honoring each auth's 60-second manual-trigger
// cooldown. It does not touch the persisted schedule state: a manual run is
// deliberately independent of whether today's real slot has already fired.
// l is the language already negotiated for this request (see
// requestLanguage in i18n.go); manualTrigger has no request of its own to
// negotiate from.
func (e *engine) manualTrigger(ctx context.Context, authGlob string, l lang) (manualRunResult, error) {
	cfg := e.config()
	switch {
	case cfg.legacyMode:
		return e.manualTriggerLegacy(ctx, cfg, authGlob, l)
	case cfg.v3InlineMode:
		return e.manualTriggerNew(ctx, cfg, authGlob, l)
	default:
		return e.manualTriggerFile(ctx, cfg, authGlob, l)
	}
}

// manualTriggerLegacy is the pre-v0.3.0 manual-run flow, unchanged.
func (e *engine) manualTriggerLegacy(ctx context.Context, cfg pluginConfig, authGlob string, l lang) (manualRunResult, error) {
	entries, err := e.auths.ListAuths()
	if err != nil {
		return manualRunResult{}, fmt.Errorf("host.auth.list: %w", err)
	}

	result := manualRunResult{Lang: string(l), Skipped: map[string]string{}, Outcomes: map[string]roundOutcome{}}
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
			result.Skipped[name] = tr(l, msgSkippedDisabled)
			continue
		}
		effective, ok := resolveAuthConfig(cfg, name, entry.Provider)
		if !ok {
			result.Skipped[name] = tr(l, msgSkippedNoModel)
			continue
		}
		if last, seen := e.manualLast[name]; seen && now.Sub(last) < manualTriggerCooldown {
			result.Skipped[name] = tr(l, msgSkippedCooldown)
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
			outcome := roundOutcome{Covered: false, Warning: unavailableModelWarning(l, t.Model)}
			result.Outcomes[t.Name] = outcome
			if cfg.Log {
				hostLog("warn", tr(l, msgAuthModelUnavailable, t.Name, t.Provider, t.Model, "manual", outcome.Warning))
			}
		}
	} else if cfg.Log {
		hostLog("warn", tr(l, msgModelPrecheckFailed, errModels))
	}

	for _, targets := range sendGroups {
		outcomes := runProviderGroup(ctx, sender, e.ring, l, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
		for name, outcome := range outcomes {
			result.Outcomes[name] = outcome
			if !cfg.Log {
				continue
			}
			t := targetByName[name]
			if outcome.Covered {
				hostLog("info", tr(l, msgWarmedAccount, name, t.Provider, t.Model, "manual", outcome.StatusCode, outcome.Rounds))
			} else {
				hostLog("warn", tr(l, msgWarmupDidNotCover, name, t.Provider, t.Model, "manual", outcome.Warning))
			}
		}
	}
	return result, nil
}

// manualTriggerNew is the v0.3.0 manual-run flow: accounts[]/model
// resolution (resolveNewAuth) instead of resolveAuthConfig, delegating to
// the shared runManualTrigger for everything after that.
func (e *engine) manualTriggerNew(ctx context.Context, cfg pluginConfig, authGlob string, l lang) (manualRunResult, error) {
	entries, err := e.auths.ListAuths()
	if err != nil {
		return manualRunResult{}, fmt.Errorf("host.auth.list: %w", err)
	}
	ov := e.overridesSnapshot()
	resolve := func(name, provider string) newAuthResolution { return resolveNewAuth(cfg, ov, name, provider) }
	return e.runManualTrigger(ctx, cfg, l, authGlob, entries, resolve)
}

// manualTriggerFile is the v0.4.0 file-mode manual-run flow: schedule/model
// resolution comes from the maintained quota-warmup.yaml (resolveFileAuth)
// instead of cfg.Accounts/TimeRaw/Model. It refreshes the file first (same
// as tickFile) so a manual "warm up now" click always sees the latest
// on-disk edits, including ones just saved through the panel's /set route.
func (e *engine) manualTriggerFile(ctx context.Context, cfg pluginConfig, authGlob string, l lang) (manualRunResult, error) {
	entries, err := e.auths.ListAuths()
	if err != nil {
		return manualRunResult{}, fmt.Errorf("host.auth.list: %w", err)
	}
	var data warmupFileData
	if e.warmupFile != nil {
		if err := e.warmupFile.ensureFresh(entries); err != nil && cfg.Log {
			hostLog("error", tr(l, msgWarmupFileError, e.warmupFile.path, err))
		}
		var parseErr string
		data, parseErr = e.warmupFile.snapshot()
		if parseErr != "" && cfg.Log {
			hostLog("error", tr(l, msgWarmupFileParseFailed, e.warmupFile.path, parseErr))
		}
	}
	resolve := func(name, provider string) newAuthResolution { return resolveFileAuth(data, name, provider) }
	return e.runManualTrigger(ctx, cfg, l, authGlob, entries, resolve)
}

// runManualTrigger is the shared tail of manualTriggerNew and
// manualTriggerFile: given a per-account resolver, fire an immediate,
// out-of-schedule warmup for every enabled+selected auth matching authGlob
// (empty matches all), ignoring the time-of-day gate but still honoring
// each auth's 60-second manual-trigger cooldown. It does not touch the
// persisted schedule state: a manual run is deliberately independent of
// whether today's real slot has already fired.
func (e *engine) runManualTrigger(ctx context.Context, cfg pluginConfig, l lang, authGlob string, entries []pluginapi.HostAuthFileEntry, resolve func(name, provider string) newAuthResolution) (manualRunResult, error) {
	result := manualRunResult{Lang: string(l), Skipped: map[string]string{}, Outcomes: map[string]roundOutcome{}}
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
			result.Skipped[name] = tr(l, msgSkippedDisabled)
			continue
		}
		res := resolve(name, entry.Provider)
		if !res.Selected {
			result.Skipped[name] = tr(l, msgSkippedNoModel)
			continue
		}
		if last, seen := e.manualLast[name]; seen && now.Sub(last) < manualTriggerCooldown {
			result.Skipped[name] = tr(l, msgSkippedCooldown)
			continue
		}
		e.manualLast[name] = now
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		target := dueTarget{
			Name:            name,
			AuthID:          entry.ID,
			Provider:        provider,
			Model:           res.ModelSpec,
			ReasoningEffort: res.ReasoningEffort,
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

	available, errModels := sender.AvailableModels(ctx)
	precheckOK := errModels == nil
	if !precheckOK && cfg.Log {
		hostLog("warn", tr(l, msgModelPrecheckFailed, errModels))
	}
	resolvedGroups, autoUnresolved := resolveAutoModels(groups, available, precheckOK)
	sendGroups := resolvedGroups
	var explicitUnavailable []dueTarget
	if precheckOK {
		sendGroups, explicitUnavailable = filterUnavailableModels(resolvedGroups, available)
	}
	for _, t := range append(append([]dueTarget(nil), autoUnresolved...), explicitUnavailable...) {
		warning := modelUnavailableWarning(l, t)
		result.Outcomes[t.Name] = roundOutcome{Covered: false, Warning: warning}
		if cfg.Log {
			hostLog("warn", tr(l, msgAuthModelUnavailable, t.Name, t.Provider, t.Model, "manual", warning))
		}
	}

	for _, targets := range sendGroups {
		outcomes := runProviderGroup(ctx, sender, e.ring, l, cfg.Message, cfg.MaxTokens, cfg.MaxRounds, targets)
		for name, outcome := range outcomes {
			result.Outcomes[name] = outcome
			if !cfg.Log {
				continue
			}
			t := targetByName[name]
			if outcome.Covered {
				hostLog("info", tr(l, msgWarmedAccount, name, t.Provider, t.Model, "manual", outcome.StatusCode, outcome.Rounds))
			} else {
				hostLog("warn", tr(l, msgWarmupDidNotCover, name, t.Provider, t.Model, "manual", outcome.Warning))
			}
		}
	}
	return result, nil
}
