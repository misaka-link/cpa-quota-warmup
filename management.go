package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
	// scope/model are v3InlineMode's shape (unchanged from v0.3.0);
	// auth/enabled/time/model is v0.4.0 file mode's shape (writes directly
	// into quota-warmup.yaml instead of overrides.json).
	setQueryScopeKey   = "scope"
	setQueryAuthKey    = "auth"
	setQueryModelKey   = "model"
	setQueryEnabledKey = "enabled"
	setQueryTimeKey    = "time"
	setScopeGlobal     = "global"
	setScopeAuth       = "auth"

	// resourceConfigYAMLPath/resourceConfigYAMLSavePath are v0.5.0's panel
	// "编辑配置文件" (edit config file) online editor for quota-warmup.yaml:
	// a GET-only read (see resourceRunPath's doc comment for why every
	// action/data route on this resource base is GET, never POST) and a
	// GET-only save that takes the whole file's content as a base64url
	// query parameter rather than a request body, for the same reason.
	// Both are file-mode only (see handleConfigYAMLRequest).
	resourceConfigYAMLPath     = "/config-yaml"
	resourceConfigYAMLSavePath = "/config-yaml/save"

	// configYAMLQuery* are /config-yaml/save's query parameters. content is
	// base64url (RFC 4648 §5), no padding -- exactly what
	// base64.RawURLEncoding decodes, and exactly what a browser produces via
	// btoa(unescape(encodeURIComponent(text))) followed by the +/- , //_,
	// strip-"=" substitution (see panel.go's saveConfigYAML) -- chosen so the
	// full file content, including newlines and any character at all, can
	// ride a GET query string with zero URL-escaping concerns. mtime is the
	// opaque token mtimeString produced for whatever version of the file the
	// editor last read (via GET .../config-yaml or a prior successful save).
	configYAMLQueryContentKey = "content"
	configYAMLQueryMtimeKey   = "mtime"

	// configYAMLMaxContentBytes caps the *decoded* file content at 256 KiB,
	// per the v0.5.0 spec -- comfortably above any real quota-warmup.yaml
	// (one short section per auth file) while still bounding worst-case
	// memory/parse cost from an arbitrary GET query string.
	configYAMLMaxContentBytes = 256 * 1024
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
				Description: "保存面板编辑（仅 GET）。默认文件模式：?auth=<name>&enabled=<bool>&time=<...>&model=<id|auto>，直接写回 quota-warmup.yaml；旧版内联模式：?scope=global|auth&auth=<name>&model=<id|auto> / Save a panel edit (GET only). Default file mode: ?auth=<name>&enabled=<bool>&time=<...>&model=<id|auto>, written directly into quota-warmup.yaml. Legacy v3-inline mode: ?scope=global|auth&auth=<name>&model=<id|auto>.",
			},
			{
				// No menu label: this is the "编辑配置文件" editor's own JSON
				// data feed (file mode only).
				Path:        resourceConfigYAMLPath,
				Description: "quota-warmup.yaml 的原始内容（供面板『编辑配置文件』使用，仅文件模式）/ Raw contents of quota-warmup.yaml (for the panel's \"edit config file\" editor; file mode only).",
			},
			{
				// No menu label: this is an action endpoint, not a page.
				Path:        resourceConfigYAMLSavePath,
				Description: "保存面板『编辑配置文件』的修改（仅 GET）：?content=<base64url 编码的 YAML，无 padding>&mtime=<上次读取到的 mtime>；先校验（失败返回 {error,line,column} 且不写盘），mtime 不一致返回冲突提示，通过则原子写入并立即热加载 / Save an edit from the panel's \"edit config file\" editor (GET only): ?content=<base64url-encoded YAML, no padding>&mtime=<the mtime last read>. Validates first (failure returns {error,line,column}, no write); a stale mtime is rejected as a conflict; on success, writes atomically and hot-reloads immediately.",
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
	case strings.HasSuffix(trimmed, resourceConfigYAMLSavePath):
		resp = handleConfigYAMLSaveRequest(req)
	case strings.HasSuffix(trimmed, resourceConfigYAMLPath):
		resp = handleConfigYAMLRequest(req)
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
	Enabled bool `json:"enabled"`
	// Mode is "inline" (legacyMode or v3InlineMode: schedule/model live in
	// config.yaml itself) or "file" (v0.4.0 default: schedule/model live in
	// the externally maintained quota-warmup.yaml -- see ConfigFile below).
	Mode       string `json:"mode"`
	LegacyMode bool   `json:"legacy_mode"`
	// ConfigFile/ConfigFileError are only populated in file mode.
	ConfigFile      string   `json:"config_file,omitempty"`
	ConfigFileError string   `json:"config_file_error,omitempty"`
	Time            []string `json:"time,omitempty"`
	Model           string   `json:"model,omitempty"`
	Accounts        []string `json:"accounts,omitempty"`
	Timezone        string   `json:"timezone"`
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

func buildConfigSummary(cfg pluginConfig, e *engine) configSummary {
	cs := configSummary{
		Enabled:        cfg.Enabled,
		Mode:           "file",
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
		cs.Mode = "inline"
		cs.Time = cfg.Default.Times
		return cs
	}
	if cfg.v3InlineMode {
		cs.Mode = "inline"
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
	// File mode.
	cs.TimezoneAuto = strings.TrimSpace(cfg.Advanced.Timezone) == ""
	if e != nil && e.warmupFile != nil {
		cs.ConfigFile = e.warmupFile.path
		if _, parseErr := e.warmupFile.snapshot(); parseErr != "" {
			cs.ConfigFileError = parseErr
		}
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
		Config:          buildConfigSummary(cfg, e),
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

	if cfg.v3InlineMode {
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

	// File mode: schedule/model come from the maintained quota-warmup.yaml.
	// Every account shows its currently configured/inherited time and model
	// regardless of whether it is enabled, so the panel can prefill the edit
	// controls for a disabled row too.
	var fileData warmupFileData
	if e.warmupFile != nil {
		// Opportunistically generate/reconcile here too (not just from the
		// 30s tick): a status call right after plugin.register, before the
		// first tick has fired, should still show the freshly generated
		// file's accounts instead of an empty list. Cheap in the common
		// case (a single stat() once nothing has changed).
		_ = e.warmupFile.ensureFresh(entries)
		fileData, _ = e.warmupFile.snapshot() // parse error already surfaced via Config.ConfigFileError
	}
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
			acct, ok := fileData.Accounts[name]
			if !ok {
				as.Skipped = tr(l, msgSkippedNotInAccounts)
				break
			}
			enabled := acct.Enabled != nil && *acct.Enabled
			as.Enabled = enabled
			timeRaw := []string(acct.Time)
			if len(timeRaw) == 0 {
				timeRaw = []string(fileData.Defaults.Time)
			}
			if len(timeRaw) == 0 {
				timeRaw = []string{defaultTime}
			}
			as.Times = timeRaw
			model := strings.TrimSpace(acct.Model)
			if model == "" {
				model = strings.TrimSpace(fileData.Defaults.Model)
			}
			if model == "" || strings.EqualFold(model, "auto") {
				as.ModelSource = modelSourceAuto
				if resolved, ok := selectModel(entry.Provider, availableSet, precheckOK); ok {
					as.Model = resolved
				}
			} else {
				as.ModelSource = modelSourceAccount
				as.Model = model
			}
			if !enabled {
				as.Skipped = tr(l, msgSkippedFileDisabled)
				break
			}
			exprs, invalid := parseTimeExprs(timeRaw)
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
	Scope   string `json:"scope,omitempty"`
	Auth    string `json:"auth,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	Time    string `json:"time,omitempty"`
	Model   string `json:"model,omitempty"`
	Message string `json:"message"`
	Warning string `json:"warning,omitempty"`
}

// handleSetRequest dispatches the panel's "save" action by mode: legacyMode
// (v0.1/v0.2) has never had a way to persist a panel edit and still does
// not; v3InlineMode keeps v0.3.0's overrides.json-backed model-only editor
// (handleSetRequestInline) unchanged; file mode (v0.4.0 default) writes
// enabled/time/model directly into quota-warmup.yaml (handleSetRequestFile).
// Like /run, this is GET-only (see resourceRunPath's doc comment for why
// POST is not reachable on a resource route at all).
func handleSetRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)
	switch {
	case cfg.legacyMode:
		return jsonManagementResponse(http.StatusNotImplemented, map[string]string{"error": tr(l, msgSetUnsupportedLegacy), "lang": string(l)})
	case cfg.v3InlineMode:
		return handleSetRequestInline(e, cfg, l, req)
	default:
		return handleSetRequestFile(e, cfg, l, req)
	}
}

// handleSetRequestInline persists (or, for model=auto, clears) a panel model
// override -- see overrides.go for the on-disk format and config.go's
// resolveModelSpecTier for how it outranks every other tier of the model
// priority chain. Unchanged from v0.3.0.
func handleSetRequestInline(e *engine, cfg pluginConfig, l lang, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
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

// handleSetRequestFile is v0.4.0 file mode's /set handler: it writes
// enabled/time/model directly into the corresponding account section of
// quota-warmup.yaml (Node-level, preserving every comment -- see
// warmupFileManager.setAccount), so there is no separate override layer to
// maintain. At least one of enabled/time/model must be given; any omitted
// one is left untouched.
func handleSetRequestFile(e *engine, cfg pluginConfig, l lang, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if e.warmupFile == nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": "quota-warmup.yaml manager not initialized", "lang": string(l)})
	}

	var authName, enabledRaw, timeRaw, model string
	if req.Query != nil {
		authName = strings.TrimSpace(req.Query.Get(setQueryAuthKey))
		enabledRaw = strings.TrimSpace(req.Query.Get(setQueryEnabledKey))
		timeRaw = strings.TrimSpace(req.Query.Get(setQueryTimeKey))
		model = strings.TrimSpace(req.Query.Get(setQueryModelKey))
	}
	if authName == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetAuthRequired), "lang": string(l)})
	}

	var update warmupFieldUpdate
	if enabledRaw != "" {
		b, err := strconv.ParseBool(enabledRaw)
		if err != nil {
			return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetInvalidEnabled), "lang": string(l)})
		}
		update.Enabled = &b
	}
	if timeRaw != "" {
		update.Time = &timeRaw
	}
	warning := ""
	if model != "" {
		if !strings.EqualFold(model, "auto") {
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
		update.Model = &model
	}
	if update.Enabled == nil && update.Time == nil && update.Model == nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgSetNothingToUpdate), "lang": string(l)})
	}

	providerHint := ""
	if entries, err := e.auths.ListAuths(); err == nil {
		// Same opportunistic ensureFresh as handleStatusRequest: a /set call
		// right after plugin.register, before the first tick, must not fail
		// with "not loaded yet" just because nothing has generated the file
		// on disk yet.
		_ = e.warmupFile.ensureFresh(entries)
		for _, entry := range entries {
			if strings.TrimSpace(entry.Name) == authName {
				providerHint = entry.Provider
				break
			}
		}
	}
	if err := e.warmupFile.setAccount(authName, update, providerHint); err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error(), "lang": string(l)})
	}

	result := setResult{Lang: string(l), Auth: authName, Enabled: update.Enabled, Time: timeRaw, Model: model, Message: tr(l, msgSetSaved), Warning: warning}
	return jsonManagementResponse(http.StatusOK, result)
}

// configYAMLPayload is the /config-yaml route's read response: the panel's
// "编辑配置文件" editor fetches this once (on load, on "重新载入", and again
// after a successful save) and assigns Content to its <textarea> via
// .value = ..., never by interpolating it into the HTML template -- see
// panel.go's loadConfigYAML. Error (when non-empty) is the file's current
// parse error, if any -- content is still returned verbatim so the operator
// can fix it in place, exactly like the "解析失败绝不影响调度" contract for
// the scheduler itself (see warmupFileManager's own doc comment).
type configYAMLPayload struct {
	Lang    string `json:"lang"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Mtime   string `json:"mtime"`
	Error   string `json:"error,omitempty"`
}

// configYAMLSaveResult is /config-yaml/save's success response.
type configYAMLSaveResult struct {
	Lang    string `json:"lang"`
	Path    string `json:"path"`
	Mtime   string `json:"mtime"`
	Message string `json:"message"`
}

// configYAMLErrorResult is /config-yaml/save's validation-failure response:
// {error, line, column}, exactly the v0.5.0 spec's shape, so the panel's
// editor can render "第 N 行第 M 列：错误信息" without any further parsing.
type configYAMLErrorResult struct {
	Lang   string `json:"lang"`
	Error  string `json:"error"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// handleConfigYAMLRequest serves the panel's "编辑配置文件" editor its
// current content. File mode only -- v3InlineMode/legacyMode have no
// externally maintained file to edit here (v3InlineMode's own panel editing
// is model-only, via /set; see handleSetRequestInline).
func handleConfigYAMLRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)
	if cfg.legacyMode || cfg.v3InlineMode || e.warmupFile == nil {
		return jsonManagementResponse(http.StatusNotImplemented, map[string]string{"error": tr(l, msgConfigYAMLNotFileMode), "lang": string(l)})
	}

	// Same opportunistic ensureFresh as handleStatusRequest/
	// handleSetRequestFile: a read right after plugin.register, before the
	// first tick, should still see a freshly generated file rather than
	// erroring because nothing has created it on disk yet. Skipped (not
	// fatal) if host.auth.list itself is failing right now -- the read
	// below just falls back to whatever is already on disk.
	if entries, err := e.auths.ListAuths(); err == nil {
		_ = e.warmupFile.ensureFresh(entries)
	}

	raw, token, err := e.warmupFile.readForEditor()
	if err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": tr(l, msgConfigYAMLReadFailed, err), "lang": string(l)})
	}
	_, parseErr := e.warmupFile.snapshot()
	return jsonManagementResponse(http.StatusOK, configYAMLPayload{
		Lang:    string(l),
		Path:    e.warmupFile.path,
		Content: string(raw),
		Mtime:   token,
		Error:   parseErr,
	})
}

// handleConfigYAMLSaveRequest validates and, if valid, persists an edit from
// the panel's "编辑配置文件" editor. File mode only (see
// handleConfigYAMLRequest). Order of checks matches the v0.5.0 spec exactly:
// validate first (a syntax/structural/business-rule failure returns
// {error,line,column} without touching mtime at all), then check mtime for a
// conflict, then write.
func handleConfigYAMLSaveRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	e := activeEngine()
	if e == nil {
		l := requestLang(nil, req)
		return jsonManagementResponse(http.StatusServiceUnavailable, map[string]string{"error": tr(l, msgEngineNotRunning), "lang": string(l)})
	}
	cfg := e.config()
	l := requestLang(&cfg, req)
	if cfg.legacyMode || cfg.v3InlineMode || e.warmupFile == nil {
		return jsonManagementResponse(http.StatusNotImplemented, map[string]string{"error": tr(l, msgConfigYAMLNotFileMode), "lang": string(l)})
	}

	var contentRaw, mtimeRaw string
	if req.Query != nil {
		contentRaw = req.Query.Get(configYAMLQueryContentKey)
		mtimeRaw = strings.TrimSpace(req.Query.Get(configYAMLQueryMtimeKey))
	}
	if contentRaw == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgConfigYAMLMissingContent), "lang": string(l)})
	}
	if mtimeRaw == "" {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgConfigYAMLMissingMtime), "lang": string(l)})
	}

	decoded, err := base64.RawURLEncoding.DecodeString(contentRaw)
	if err != nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgConfigYAMLInvalidBase64), "lang": string(l)})
	}
	if len(decoded) > configYAMLMaxContentBytes {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": tr(l, msgConfigYAMLTooLarge), "lang": string(l)})
	}

	// Normalize CRLF (and lone CR) to LF before validating/writing, so a
	// Windows-side browser's <textarea> never leaves mixed line endings in
	// the file on disk.
	content := strings.ReplaceAll(strings.ReplaceAll(string(decoded), "\r\n", "\n"), "\r", "\n")

	if _, verr := validateWarmupYAMLContent([]byte(content)); verr != nil {
		return jsonManagementResponse(http.StatusBadRequest, configYAMLErrorResult{Lang: string(l), Error: verr.Message, Line: verr.Line, Column: verr.Column})
	}

	// Same opportunistic ensureFresh as the read side -- also makes sure
	// currentMtimeToken() below reflects any reconciliation (new/vanished
	// accounts) that just happened, not a stale pre-reconciliation token.
	if entries, err := e.auths.ListAuths(); err == nil {
		_ = e.warmupFile.ensureFresh(entries)
	}

	currentToken, statErr := e.warmupFile.currentMtimeToken()
	if statErr != nil || currentToken != mtimeRaw {
		return jsonManagementResponse(http.StatusConflict, map[string]string{"error": tr(l, msgConfigYAMLConflict), "lang": string(l)})
	}

	newMtime, writeErr := e.warmupFile.overwriteRaw([]byte(content))
	if writeErr != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": tr(l, msgConfigYAMLWriteFailed, writeErr), "lang": string(l)})
	}

	return jsonManagementResponse(http.StatusOK, configYAMLSaveResult{
		Lang:    string(l),
		Path:    e.warmupFile.path,
		Mtime:   newMtime,
		Message: tr(l, msgConfigYAMLSaved),
	})
}
