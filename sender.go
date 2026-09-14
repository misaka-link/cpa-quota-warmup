package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	sessionTagPrefix      = "cpa-quota-warmup-"
	warmupRequestTimeout  = 60 * time.Second
	modelsPrecheckTimeout = 10 * time.Second
	chatCompletionsPath   = "/v1/chat/completions"
	modelsPath            = "/v1/models"
)

// chatSender issues one warmup chat-completion request tagged with a unique
// session id and reports the HTTP status observed. It is an interface so the
// round-coverage logic in runner.go can be tested without a live CPA server.
type chatSender interface {
	sendWarmup(ctx context.Context, req warmupSendRequest) warmupSendResult
}

// modelLister lists the model ids this CPA instance currently exposes, used
// to precheck a configured model before spending a warmup request on it. It
// is a separate interface from chatSender so round-coverage tests (which
// only ever need sendWarmup) do not have to stub out a method they never
// call.
type modelLister interface {
	AvailableModels(ctx context.Context) (map[string]bool, error)
}

// warmupClient is everything the engine needs from "this CPA instance,
// authenticated with this api key": sending warmup requests and listing
// currently available models for the precheck. httpChatSender implements
// both with the same underlying http.Client/base URL/api key.
type warmupClient interface {
	chatSender
	modelLister
}

// warmupSendRequest describes one outbound warmup attempt.
type warmupSendRequest struct {
	Model           string
	ReasoningEffort string
	SessionTag      string
	Message         string
	MaxTokens       int
}

// warmupSendResult is what sendWarmup observed. StatusCode is 0 when the
// request never produced an HTTP response at all (dial/timeout/transport
// error); Err then carries the reason.
type warmupSendResult struct {
	StatusCode int
	Err        error
}

// httpChatSender is the production chatSender: a plain net/http POST to this
// CPA instance's own /v1/chat/completions, exactly like any other client.
//
// host.model.execute is deliberately not used here: it runs with
// InternalSource=true, and the host does not publish a usage record for
// internally sourced executions (see sdk/api handlers), which would make it
// impossible to tell which account the warmup landed on.
type httpChatSender struct {
	client  *http.Client
	baseURL string
	apiKey  string
}

func newHTTPChatSender(baseURL, apiKey string) *httpChatSender {
	return &httpChatSender{
		client:  &http.Client{Timeout: warmupRequestTimeout},
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
	}
}

func (s *httpChatSender) sendWarmup(ctx context.Context, req warmupSendRequest) warmupSendResult {
	body := map[string]any{
		"model":    req.Model,
		"messages": []map[string]string{{"role": "user", "content": req.Message}},
		"stream":   false,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return warmupSendResult{Err: fmt.Errorf("encode warmup request: %w", err)}
	}

	ctx, cancel := context.WithTimeout(ctx, warmupRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+chatCompletionsPath, bytes.NewReader(encoded))
	if err != nil {
		return warmupSendResult{Err: fmt.Errorf("build warmup request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	// A fresh, random session id on every single request makes each one look
	// like a brand-new session to the host's session-affinity binding, so it
	// falls back to the host's round-robin selector (or whatever scheduler
	// plugin is active, e.g. cpa-affinity-router for antigravity/gemini-*)
	// instead of sticking to whichever account served the last warmup call.
	httpReq.Header.Set("X-Session-ID", req.SessionTag)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return warmupSendResult{Err: fmt.Errorf("warmup request: %w", err)}
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	// Draining and discarding is enough: the round outcome comes from the
	// host's own usage.handle callback, not from parsing this response body.
	return warmupSendResult{StatusCode: resp.StatusCode}
}

// modelsListResponse is the relevant subset of GET /v1/models' OpenAI-shaped
// response body: {"object":"list","data":[{"id":"...","owned_by":"...",...}]}.
type modelsListResponse struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// modelInfo is one entry of a GET /v1/models listing, kept for display
// purposes (the panel's model datalist groups options by OwnedBy).
type modelInfo struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// fetchModels performs the actual GET /v1/models call shared by
// AvailableModels and ListModelsDetailed below.
func (s *httpChatSender) fetchModels(ctx context.Context) (modelsListResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsPrecheckTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+modelsPath, nil)
	if err != nil {
		return modelsListResponse{}, fmt.Errorf("build models request: %w", err)
	}
	if s.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return modelsListResponse{}, fmt.Errorf("models request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return modelsListResponse{}, fmt.Errorf("models request: unexpected status %d", resp.StatusCode)
	}

	var parsed modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return modelsListResponse{}, fmt.Errorf("decode models response: %w", err)
	}
	return parsed, nil
}

// ListModelsDetailed returns every model GET /v1/models currently lists,
// with its owned_by label, for the panel's model picker (status route's
// available_models). Unlike AvailableModels, callers here typically want to
// distinguish "list is empty" from "list is unavailable", so this returns
// (nil, err) rather than treating a failure as "the empty set".
func (s *httpChatSender) ListModelsDetailed(ctx context.Context) ([]modelInfo, error) {
	parsed, err := s.fetchModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, modelInfo{ID: id, OwnedBy: strings.TrimSpace(m.OwnedBy)})
	}
	return out, nil
}

// AvailableModels calls this CPA instance's own GET /v1/models with the same
// api key the warmup requests use, and returns the set of model ids it
// currently lists. Callers must treat a non-nil error as "precheck
// unavailable, send anyway" rather than as "no models available": a
// transient failure to list models must never block a warmup request that
// would otherwise have gone out.
func (s *httpChatSender) AvailableModels(ctx context.Context) (map[string]bool, error) {
	parsed, err := s.fetchModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(parsed.Data))
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// newSessionTag returns a short random tag safe to use as an X-Session-ID
// header value and as a plain-text log fragment.
func newSessionTag() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is effectively unheard-of on any real host; fall
		// back to a fixed-but-still-unique-enough value derived from time so
		// a warmup round never silently reuses one session id across targets.
		return sessionTagPrefix + fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return sessionTagPrefix + hex.EncodeToString(buf[:])
}
