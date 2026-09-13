package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var errAllocateHostRequest = errors.New("allocate host callback request buffer")

// hostEnvelope mirrors pluginabi.Envelope's wire shape for decoding responses
// coming back from callHostAPI.
type hostEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *hostError      `json:"error,omitempty"`
}

type hostError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func decodeHostEnvelope(method string, raw []byte, callCode int) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, callCode)
	}
	var env hostEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("host callback %s failed: %s (%s)", method, strings.TrimSpace(env.Error.Message), env.Error.Code)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, callCode)
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

// hostAuthListResponse mirrors internal/pluginhost's rpcHostAuthListResponse.
type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// hostAuthList calls host.auth.list and returns every credential the host
// currently knows about, including disabled/unavailable ones (callers filter).
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := callHostAPI(pluginabi.MethodHostAuthList, []byte("{}"))
	if err != nil {
		return nil, err
	}
	var resp hostAuthListResponse
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("decode host.auth.list response: %w", err)
		}
	}
	return resp.Files, nil
}

// hostLogRequest mirrors internal/pluginhost's rpcHostLogRequest wire shape.
type hostLogRequest struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// hostLog writes one line to the host's journal via host.log. CPA's log
// formatter does not render structured fields, so every caller is expected to
// have already folded anything worth keeping into message itself; fields here
// only exist for the (currently unused) structured consumers on the host side.
func hostLog(level, message string) {
	req := hostLogRequest{Level: level, Message: logPrefix + message}
	payload, err := json.Marshal(req)
	if err != nil {
		return
	}
	// Logging must never be allowed to fail loudly: it runs from a background
	// goroutine with nobody to report an error to.
	_, _ = callHostAPI(pluginabi.MethodHostLog, payload)
}
