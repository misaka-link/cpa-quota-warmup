package main

import (
	"context"
	"encoding/json"
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
	runQueryAuthKey    = "auth"
	runRequestBudget   = 4 * time.Minute
)

func managementRegistration() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{
			{
				// The visible page: menu label lives here, not on the JSON
				// feed below, matching every other plugin panel in this
				// deployment (cpa-usage-panel, cpa-context-vm).
				Path:        resourcePanelPath,
				Menu:        "配额预热 / Quota Warmup",
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
	Enabled        bool   `json:"enabled"`
	Timezone       string `json:"timezone"`
	BaseURL        string `json:"base_url"`
	Message        string `json:"message"`
	MaxTokens      int    `json:"max_tokens"`
	MaxRounds      int    `json:"max_rounds"`
	CatchUpMinutes int    `json:"catch_up_minutes"`
	Language       string `json:"language"`
}

type authStatus struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider,omitempty"`
	Enabled     bool     `json:"enabled"`
	Model       string   `json:"model,omitempty"`
	Times       []string `json:"times,omitempty"`
	NextTrigger string   `json:"next_trigger,omitempty"`
	Skipped     string   `json:"skipped,omitempty"`
}

type statusPayload struct {
	Lang          string        `json:"lang"`
	Config        configSummary `json:"config"`
	Auths         []authStatus  `json:"auths,omitempty"`
	AuthsError    string        `json:"auths_error,omitempty"`
	Recent        []slotRecord  `json:"recent"`
	LastTick      string        `json:"last_tick,omitempty"`
	LastTickError string        `json:"last_tick_error,omitempty"`
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
		Lang: string(l),
		Config: configSummary{
			Enabled:        cfg.Enabled,
			Timezone:       cfg.Timezone,
			BaseURL:        cfg.BaseURL,
			Message:        cfg.Message,
			MaxTokens:      cfg.MaxTokens,
			MaxRounds:      cfg.MaxRounds,
			CatchUpMinutes: cfg.CatchUpMinutes,
			Language:       cfg.Language,
		},
		Recent: e.state.snapshot(),
	}

	e.lastTickMu.Lock()
	if !e.lastTick.IsZero() {
		payload.LastTick = e.lastTick.Format(time.RFC3339)
	}
	payload.LastTickError = e.lastError
	e.lastTickMu.Unlock()

	entries, err := e.auths.ListAuths()
	if err != nil {
		payload.AuthsError = err.Error()
		return jsonManagementResponse(http.StatusOK, payload)
	}

	now := time.Now().In(cfg.location)
	catchUp := time.Duration(cfg.CatchUpMinutes) * time.Minute
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
