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
	contentTypeJSON    = "application/json; charset=utf-8"
	resourceStatusPath = "/status"
	// resourceRunPath is only reachable over GET: CPA's resource route
	// dispatcher (internal/pluginhost's ServeResourceHTTP, verified against
	// v7.2.158) hard-codes `if !strings.EqualFold(r.Method, http.MethodGet)
	// { return false }` before it ever builds a ManagementRequest, so a POST
	// route under /v0/resource/plugins/... is not reachable at all -- only a
	// Management API route (under /v0/management/, and authenticated) can be
	// POST. Since this endpoint is meant to be unauthenticated like status,
	// it is exposed as GET with the auth filter in the query string instead
	// of as a POST body.
	resourceRunPath  = "/run"
	runQueryAuthKey  = "auth"
	runRequestBudget = 4 * time.Minute
)

func managementRegistration() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        resourceStatusPath,
				Menu:        "Quota Warmup",
				Description: "Per-auth warmup schedule, next trigger times, and recent results.",
			},
			{
				// No menu label: this is an action endpoint, not a page.
				Path:        resourceRunPath,
				Description: "Trigger an immediate out-of-schedule warmup round (GET only; optional ?auth=<glob> narrows which auth files run).",
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
	if strings.HasSuffix(trimmed, resourceRunPath) {
		resp = handleRunRequest(req)
	} else {
		resp = handleStatusRequest()
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

type configSummary struct {
	Enabled        bool   `json:"enabled"`
	Timezone       string `json:"timezone"`
	BaseURL        string `json:"base_url"`
	Message        string `json:"message"`
	MaxTokens      int    `json:"max_tokens"`
	MaxRounds      int    `json:"max_rounds"`
	CatchUpMinutes int    `json:"catch_up_minutes"`
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
	Config        configSummary `json:"config"`
	Auths         []authStatus  `json:"auths,omitempty"`
	AuthsError    string        `json:"auths_error,omitempty"`
	Recent        []slotRecord  `json:"recent"`
	LastTick      string        `json:"last_tick,omitempty"`
	LastTickError string        `json:"last_tick_error,omitempty"`
}

func handleStatusRequest() pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": "engine not running"})
	}
	cfg := e.config()
	payload := statusPayload{
		Config: configSummary{
			Enabled:        cfg.Enabled,
			Timezone:       cfg.Timezone,
			BaseURL:        cfg.BaseURL,
			Message:        cfg.Message,
			MaxTokens:      cfg.MaxTokens,
			MaxRounds:      cfg.MaxRounds,
			CatchUpMinutes: cfg.CatchUpMinutes,
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
			as.Skipped = "disabled or unavailable"
		default:
			if effective, ok := resolveAuthConfig(cfg, name, entry.Provider); ok {
				as.Enabled = true
				as.Model = effective.Model
				as.Times = effective.Times
				if next := nextTriggerFor(effective.Times, now, cfg.location, catchUp, e.state, name); !next.IsZero() {
					as.NextTrigger = next.Format(time.RFC3339)
				}
			} else {
				as.Skipped = "not enabled or no resolvable model for its provider"
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
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": "engine not running"})
	}
	glob := ""
	if req.Query != nil {
		glob = req.Query.Get(runQueryAuthKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runRequestBudget)
	defer cancel()
	result, err := e.manualTrigger(ctx, glob)
	if err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return jsonManagementResponse(http.StatusOK, result)
}
