package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	contentTypeJSON = "application/json; charset=utf-8"
	contentTypeHTML = "text/html; charset=utf-8"
	langQueryKey    = "lang"
	// resourceRunPath is only reachable over GET: CPA's resource route
	// dispatcher (internal/pluginhost's ServeResourceHTTP, verified against
	// v7.2.158) hard-codes `if !strings.EqualFold(r.Method, http.MethodGet)
	// { return false }` before it ever builds a ManagementRequest, so a POST
	// route under /v0/resource/plugins/... is not reachable at all -- only a
	// Management API route (under /v0/management/, and authenticated) can be
	// POST. Since this endpoint is meant to be unauthenticated like status,
	// it is exposed as GET with the auth filter in the query string instead
	// of as a POST body.
	resourceRunPath    = "/run"
	resourceStatusPath = "/status"
	resourcePanelPath  = "/panel"
	resourceSetPath    = "/set"
	runQueryAuthKey    = "auth"
	runRequestBudget   = 4 * time.Minute

	// setQuery* are the /set route's query parameters (also GET-only, for
	// the same reason resourceRunPath is -- see the comment above).
	setQueryScopeKey = "scope"
	setQueryAuthKey  = "auth"
	setQueryModelKey = "model"
	setScopeGlobal   = "global"
	setScopeAuth     = "auth"
)

func managementRegistration() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{
			{
				// The visible page: menu label lives here, not on the JSON
				// feed below, matching every other plugin panel in this
				// deployment (cpa-usage-panel, cpa-context-vm).
				Path:        resourcePanelPath,
				Menu:        "配额预热",
				Description: "每账号预热计划、下次触发时间与最近结果 / Per-account warmup schedule, next trigger times, and recent results.",
			},
			{
				// No menu label: this is the page's own JSON data feed.
				Path:        resourceStatusPath,
				Description: "配额预热状态 JSON（供页面与脚本使用）/ Quota warmup status as JSON (for the panel page and scripts).",
			},
			{
				// No menu label: this is an action endpoint, not a page.
				Path:        resourceRunPath,
				Description: "立即触发一轮预热（仅 GET；可选 ?auth=<glob>）/ Trigger an immediate out-of-schedule warmup round (GET only; optional ?auth=<glob>).",
			},
			{
				// No menu label: this is an action endpoint, not a page.
				Path:        resourceSetPath,
				Description: "设置/清除某账号或全局的预热模型（仅 GET；?scope=global|auth&auth=<name>&model=<id|auto>）/ Set or clear the warmup model for one account or globally (GET only; ?scope=global|auth&auth=<name>&model=<id|auto>).",
			},
		},
	}
}

func handleManagementRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
	}
	trimmed := strings.TrimRight(req.Path, "/")
	var resp pluginapi.ManagementResponse
	switch {
	case strings.HasSuffix(trimmed, resourceRunPath):
		resp = handleRunRequest(req)
	case strings.HasSuffix(trimmed, resourceSetPath):
		resp = handleSetRequest(req)
	case strings.HasSuffix(trimmed, resourceStatusPath):
		resp = handleStatusRequest(req)
	default:
		// Includes resourcePanelPath and any unrecognized suffix (e.g. the
		// plugin's bare resource root, which CPA never routes here anyway
		// since ResourceRoute.Path cannot be empty -- see panel.go).
		resp = handlePanelRequest(req)
	}
	return okEnvelope(resp)
}

func jsonManagementResponse(status int, payload any) pluginapi.ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusInternalServerError,
			Headers:    http.Header{"Content-Type": []string{contentTypeJSON}},
			Body:       []byte(`{"error":"failed to encode response"}`),
		}
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{contentTypeJSON},
			"Cache-Control": []string{"no-store"},
		},
		Body: body,
	}
}

// requestLang resolves the language for one request given whatever engine
// config is available (nil when the engine is not running -- e.g. the
// service just started or was just shut down -- in which case only the
// request's own query/header/env signals are used, as if language: "auto").
func requestLang(cfg *pluginConfig, req pluginapi.ManagementRequest) lang {
	effective := pluginConfig{Language: "auto"}
	if cfg != nil {
		effective = *cfg
	}
	var acceptLanguage string
	if req.Headers != nil {
		acceptLanguage = req.Headers.Get("Accept-Language")
	}
	var queryLang string
	if req.Query != nil {
		queryLang = req.Query.Get(langQueryKey)
	}
	return requestLanguage(effective, queryLang, acceptLanguage)
}

type configSummary struct {
	Enabled    bool     `json:"enabled"`
	LegacyMode bool     `json:"legacy_mode"`
	Time       []string `json:"time,omitempty"`
	Model      string   `json:"model,omitempty"`
	Accounts   []string `json:"accounts,omitempty"`
	Timezone   string   `json:"timezone"`
	// TimezoneAuto is true when the new-format config left advanced.timezone
	// unset, meaning Timezone above is whatever the host process's own local
	// timezone (time.Local) resolved to, not an explicit setting.
	TimezoneAuto   bool   `json:"timezone_auto"`
	BaseURL        string `json:"base_url"`
	Message        string `json:"message"`
	MaxTokens      int    `json:"max_tokens"`
	MaxRounds      int    `json:"max_rounds"`
	CatchUpMinutes int    `json:"catch_up_minutes"`
	Language       string `json:"language"`
}

func buildConfigSummary(cfg pluginConfig) configSummary {
	cs := configSummary{
		Enabled:        cfg.Enabled,
		LegacyMode:     cfg.legacyMode,
		Timezone:       cfg.Timezone,
		BaseURL:        cfg.BaseURL,
		Message:        cfg.Message,
		MaxTokens:      cfg.MaxTokens,
		MaxRounds:      cfg.MaxRounds,
		CatchUpMinutes: cfg.CatchUpMinutes,
		Language:       cfg.Language,
	}
	if cfg.legacyMode {
		cs.Time = cfg.Default.Times
		return cs
	}
	cs.Time = cfg.TimeRaw
	cs.TimezoneAuto = strings.TrimSpace(cfg.Advanced.Timezone) == ""
	if m := strings.TrimSpace(cfg.Model.Scalar); m != "" {
		cs.Model = m
	} else if len(cfg.Model.Map) == 0 {
		cs.Model = "auto"
	}
	for _, a := range cfg.Accounts {
		cs.Accounts = append(cs.Accounts, a.Match)
	}
	return cs
}

type authStatus struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider,omitempty"`
	Enabled     bool     `json:"enabled"`
	Model       string   `json:"model,omitempty"`
	ModelSource string   `json:"model_source,omitempty"`
	Times       []string `json:"times,omitempty"`
	NextTrigger string   `json:"next_trigger,omitempty"`
	Skipped     string   `json:"skipped,omitempty"`
	Warning     string   `json:"warning,omitempty"`
}

type statusPayload struct {
	Lang            string        `json:"lang"`
	Config          configSummary `json:"config"`
	Auths           []authStatus  `json:"auths,omitempty"`
	AuthsError      string        `json:"auths_error,omitempty"`
	Recent          []slotRecord  `json:"recent"`
	LastTick        string        `json:"last_tick,omitempty"`
	LastTickError   string        `json:"last_tick_error,omitempty"`
	AvailableModels []modelInfo   `json:"available_models"`
}

func handleStatusRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)
	payload := statusPayload{
		Lang:            string(l),
		Config:          buildConfigSummary(cfg),
		Recent:          e.state.snapshot(),
		AvailableModels: []modelInfo{},
	}

	e.lastTickMu.Lock()
	if !e.lastTick.IsZero() {
		payload.LastTick = e.lastTick.Format(time.RFC3339)
	}
	payload.LastTickError = e.lastError
	e.lastTickMu.Unlock()

	// available_models feeds both the status JSON directly and this
	// function's own auto-model-selection display below. A failure here
	// (no api-key resolvable, GET /v1/models unreachable, ...) is never
	// fatal -- it just means available_models stays empty and any
	// auto-selected model falls back to its candidate-list default, exactly
	// like a live warmup tick would.
	var availableSet map[string]bool
	precheckOK := false
	if !cfg.legacyMode {
		if apiKey, err := resolveAPIKey(cfg); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), modelsPrecheckTimeout)
			models, errModels := newHTTPChatSender(cfg.BaseURL, apiKey).ListModelsDetailed(ctx)
			cancel()
			if errModels == nil {
				precheckOK = true
				payload.AvailableModels = models
				availableSet = make(map[string]bool, len(models))
				for _, m := range models {
					availableSet[m.ID] = true
				}
			}
		}
	}

	entries, err := e.auths.ListAuths()
	if err != nil {
		payload.AuthsError = err.Error()
		return jsonManagementResponse(http.StatusOK, payload)
	}

	now := time.Now().In(cfg.location)
	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
	if cfg.legacyMode {
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			as := authStatus{Name: name, Provider: entry.Provider}
			switch {
			case entry.Disabled || entry.Unavailable:
				as.Skipped = tr(l, msgSkippedDisabled)
			default:
				if effective, ok := resolveAuthConfig(cfg, name, entry.Provider); ok {
					as.Enabled = true
					as.Model = effective.Model
					as.Times = effective.Times
					if next := nextTriggerFor(effective.Times, now, cfg.location, catchUp, e.state, name); !next.IsZero() {
						as.NextTrigger = next.Format(time.RFC3339)
					}
				} else {
					as.Skipped = tr(l, msgSkippedNoModel)
				}
			}
			payload.Auths = append(payload.Auths, as)
		}
		return jsonManagementResponse(http.StatusOK, payload)
	}

	ov := e.overridesSnapshot()
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		as := authStatus{Name: name, Provider: entry.Provider}
		switch {
		case entry.Disabled || entry.Unavailable:
			as.Skipped = tr(l, msgSkippedDisabled)
		default:
			res := resolveNewAuth(cfg, ov, name, entry.Provider)
			if !res.Selected {
				as.Skipped = tr(l, msgSkippedNotInAccounts)
				break
			}
			as.Enabled = true
			as.Times = res.TimeRaw
			as.ModelSource = res.ModelSource
			if res.ModelSpec != "" {
				as.Model = res.ModelSpec
			} else if model, ok := selectModel(entry.Provider, availableSet, precheckOK); ok {
				as.Model = model
			}
			exprs, invalid := parseTimeExprs(res.TimeRaw)
			if len(invalid) > 0 {
				as.Warning = tr(l, msgInvalidTimeExpr, strings.Join(invalid, ", "))
			}
			if next := nextTriggerForCron(exprs, now, cfg.location, catchUp, e.state, name); !next.IsZero() {
				as.NextTrigger = next.Format(time.RFC3339)
			}
		}
		payload.Auths = append(payload.Auths, as)
	}

	return jsonManagementResponse(http.StatusOK, payload)
}

// nextTriggerFor returns the next instant (today, if still due or upcoming;
// otherwise tomorrow) any of times would fire for name, given what has
// already been recorded in state.
func nextTriggerFor(times []string, now time.Time, loc *time.Location, catchUp time.Duration, state *stateStore, name string) time.Time {
	var best time.Time
	for _, hhmm := range times {
		slot, ok := slotTime(now, hhmm, loc)
		if !ok {
			continue
		}
		var candidate time.Time
		switch {
		case slot.After(now):
			candidate = slot
		case !state.isRecorded(name, slotDateKey(slot, loc), hhmm) && now.Sub(slot) <= catchUp:
			candidate = slot
		default:
			candidate = slot.AddDate(0, 0, 1)
		}
		if best.IsZero() || candidate.Before(best) {
			best = candidate
		}
	}
	return best
}

// nextTriggerForCron is nextTriggerFor's new-format (cron/HH:MM expression)
// equivalent: today's most recent trigger if it is still due (within
// catchUp) and not already recorded, otherwise the soonest future trigger,
// across every expression, earliest wins.
func nextTriggerForCron(exprs []*cronExpr, now time.Time, loc *time.Location, catchUp time.Duration, state *stateStore, name string) time.Time {
	var best time.Time
	for _, expr := range exprs {
		var candidate time.Time
		if due, ok := expr.lastTriggerAtOrBefore(now, loc, catchUp); ok {
			dateKey := slotDateKey(due, loc)
			hhmm := due.Format("15:04")
			if !state.isRecorded(name, dateKey, hhmm) {
				candidate = due
			}
		}
		if candidate.IsZero() {
			next, ok := expr.nextTrigger(now, loc)
			if !ok {
				continue
			}
			candidate = next
		}
		if best.IsZero() || candidate.Before(best) {
			best = candidate
		}
	}
	return best
}

func handleRunRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)
	glob := ""
	if req.Query != nil {
		glob = req.Query.Get(runQueryAuthKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runRequestBudget)
	defer cancel()
	result, err := e.manualTrigger(ctx, glob, l)
	if err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": tr(l, msgRunFailed, err.Error()), "lang": string(l)})
	}
	return jsonManagementResponse(http.StatusOK, result)
}

// setResult is what the /set route reports back.
type setResult struct {
	Lang    string `json:"lang"`
	Scope   string `json:"scope"`
	Auth    string `json:"auth,omitempty"`
	Model   string `json:"model"`
	Message string `json:"message"`
	Warning string `json:"warning,omitempty"`
}

// handleSetRequest persists (or, for model=auto, clears) a panel model
// override -- see overrides.go for the on-disk format and config.go's
// resolveModelSpecTier for how it outranks every other tier of the model
// priority chain. Like /run, this is GET-only (see resourceRunPath's doc
// comment for why POST is not reachable on a resource route at all).
func handleSetRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)

	var scope, authName, model string
	if req.Query != nil {
		scope = strings.TrimSpace(req.Query.Get(setQueryScopeKey))
		authName = strings.TrimSpace(req.Query.Get(setQueryAuthKey))
		model = strings.TrimSpace(req.Query.Get(setQueryModelKey))
	}
	if scope != setScopeGlobal && scope != setScopeAuth {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetInvalidScope), "lang": string(l)})
	}
	if scope == setScopeAuth && authName == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetAuthRequired), "lang": string(l)})
	}
	if model == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetModelRequired), "lang": string(l)})
	}

	// "auto" clears the override (deletes the entry) rather than storing the
	// literal string -- see modelOverrides' doc comment in overrides.go.
	clearing := strings.EqualFold(model, "auto")
	warning := ""
	if !clearing {
		apiKey, err := resolveAPIKey(cfg)
		if err != nil {
			warning = tr(l, msgSetPrecheckFailedWarning, err)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), modelsPrecheckTimeout)
			available, errModels := newHTTPChatSender(cfg.BaseURL, apiKey).AvailableModels(ctx)
			cancel()
			if errModels != nil {
				warning = tr(l, msgSetPrecheckFailedWarning, errModels)
			} else if !available[model] {
				return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetModelNotAvailable, model), "lang": string(l)})
			}
		}
	}

	if e.overrides == nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("overrides store not initialized").Error(), "lang": string(l)})
	}
	saveValue := model
	if clearing {
		saveValue = ""
	}
	var saveErr error
	if scope == setScopeGlobal {
		saveErr = e.overrides.setGlobal(saveValue)
	} else {
		saveErr = e.overrides.setAuth(authName, saveValue)
	}
	if saveErr != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": saveErr.Error(), "lang": string(l)})
	}

	message := tr(l, msgSetSaved)
	if clearing {
		message = tr(l, msgSetCleared)
	}
	result := setResult{Lang: string(l), Scope: scope, Auth: authName, Model: model, Message: message, Warning: warning}
	return jsonManagementResponse(http.StatusOK, result)
}
