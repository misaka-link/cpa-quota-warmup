package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// placeholderPattern matches an unreplaced {{msg_key}} template placeholder.
// A naive substring search for "{{" or "}}" is not safe here: the injected
// window.__I18N__ JSON catalog is a map of maps, so its own compact encoding
// legitimately ends with adjacent "}}" (closing the innermost language
// object, then the outer object) with no space in between.
var placeholderPattern = regexp.MustCompile(`\{\{[a-zA-Z_.]+\}\}`)

func TestRenderPanelHTMLNoLeftoverPlaceholders(t *testing.T) {
	catalogJSON, err := i18nCatalogJSON()
	if err != nil {
		t.Fatalf("i18nCatalogJSON: %v", err)
	}
	for _, l := range supportedLanguages {
		body := string(renderPanelHTML(l, catalogJSON))
		if m := placeholderPattern.FindAllString(body, -1); len(m) > 0 {
			t.Errorf("language %s: rendered panel HTML still has unreplaced placeholder(s): %v", l, m)
		}
	}
}

func TestRenderPanelHTMLReadsCliProxyStorageKeys(t *testing.T) {
	catalogJSON, err := i18nCatalogJSON()
	if err != nil {
		t.Fatalf("i18nCatalogJSON: %v", err)
	}
	body := string(renderPanelHTML(langEN, catalogJSON))
	if !strings.Contains(body, "cli-proxy-language") {
		t.Fatalf("panel HTML does not read the cli-proxy-language localStorage key")
	}
	if !strings.Contains(body, "cli-proxy-theme") {
		t.Fatalf("panel HTML does not follow the cli-proxy-theme localStorage key")
	}
}

func TestRenderPanelHTMLEmbedsTranslatedTitlePerLanguage(t *testing.T) {
	catalogJSON, err := i18nCatalogJSON()
	if err != nil {
		t.Fatalf("i18nCatalogJSON: %v", err)
	}
	cases := map[lang]string{
		langEN:   messagesEN[msgUIPageTitle],
		langZhCN: messagesZhCN[msgUIPageTitle],
		langZhTW: messagesZhTW[msgUIPageTitle],
		langRU:   messagesRU[msgUIPageTitle],
	}
	for l, want := range cases {
		body := string(renderPanelHTML(l, catalogJSON))
		if !strings.Contains(body, "<title>"+want+"</title>") {
			t.Errorf("language %s: rendered HTML title does not contain %q", l, want)
		}
		if !strings.Contains(body, "<html lang=\""+string(l)+"\">") {
			t.Errorf("language %s: <html lang> attribute not set to %q", l, l)
		}
	}
}

func TestRenderPanelHTMLFallsBackToEnglishForUnknownLanguage(t *testing.T) {
	catalogJSON, err := i18nCatalogJSON()
	if err != nil {
		t.Fatalf("i18nCatalogJSON: %v", err)
	}
	body := string(renderPanelHTML(lang("xx"), catalogJSON))
	if !strings.Contains(body, messagesEN[msgUIPageTitle]) {
		t.Fatalf("unknown language should fall back to English text in the rendered HTML")
	}
}

func TestHandlePanelRequestServesHTML(t *testing.T) {
	t.Cleanup(shutdownEngine)
	shutdownEngine()

	req := pluginapi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/resource/plugins/cpa-quota-warmup/panel",
	}
	resp := handlePanelRequest(req)
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	ct := resp.Headers.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(resp.Body), "cli-proxy-language") {
		t.Fatalf("panel response body does not mention cli-proxy-language")
	}
}

// TestHandleManagementRequestRoutesPanelStatusAndRun exercises the actual
// path-suffix routing decision in management.go's handleManagementRequest,
// not just the individual handlers: .../status must produce JSON, .../run
// must produce JSON, and .../panel (or any other suffix) must produce the
// HTML page.
func TestHandleManagementRequestRoutesPanelStatusAndRun(t *testing.T) {
	t.Cleanup(shutdownEngine)
	shutdownEngine()

	call := func(path string) pluginapi.ManagementResponse {
		t.Helper()
		raw, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: path})
		respRaw, err := handleManagementRequest(raw)
		if err != nil {
			t.Fatalf("handleManagementRequest(%s): %v", path, err)
		}
		var envelope struct {
			Result pluginapi.ManagementResponse `json:"result"`
		}
		if err := json.Unmarshal(respRaw, &envelope); err != nil {
			t.Fatalf("decode envelope for %s: %v", path, err)
		}
		return envelope.Result
	}

	if ct := call("/v0/resource/plugins/cpa-quota-warmup/panel").Headers.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf(".../panel Content-Type = %q, want text/html", ct)
	}
	if ct := call("/v0/resource/plugins/cpa-quota-warmup/status").Headers.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf(".../status Content-Type = %q, want application/json", ct)
	}
	if ct := call("/v0/resource/plugins/cpa-quota-warmup/run").Headers.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf(".../run Content-Type = %q, want application/json", ct)
	}
}
