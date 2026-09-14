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

// uiPlaceholderPattern matches a {{ui_*}} placeholder specifically (as
// opposed to placeholderPattern above, which also matches non-UI keys like
// {{HTML_LANG}}/{{I18N_JSON}} that are never meant to carry a data-i18n
// attribute since they are not user-facing translated text).
var uiPlaceholderPattern = regexp.MustCompile(`\{\{(ui_[a-zA-Z_]+)\}\}`)

// TestPanelTemplateStaticTextHasDataI18nAttributes guards against the v0.2.0
// regression this was fixed for: the server-side {{ui_*}} substitution only
// renders correctly for whatever language the *server* guessed from
// Accept-Language (a headless browser's own OS/CLI default, not whatever an
// operator's cli-proxy-language actually says), and static text nodes were
// never revisited once the page's own JS learned the real client-side
// language.
//
// This checks every individual occurrence, not just "is this key tagged
// somewhere in the template": several keys (ui_page_title, ui_col_provider,
// ui_col_model, ui_loading) appear more than once, on different elements
// (e.g. <title> and <h1> both render ui_page_title; the accounts and recent
// tables both have a "provider"/"model" column), and a set-membership check
// would miss one of those elements losing its attribute as long as some
// other element with the same key still has one.
func TestPanelTemplateStaticTextHasDataI18nAttributes(t *testing.T) {
	locs := uiPlaceholderPattern.FindAllStringSubmatchIndex(panelHTMLTemplate, -1)
	if len(locs) == 0 {
		t.Fatalf("found no {{ui_*}} placeholders in panelHTMLTemplate -- test fixture or template is broken")
	}
	for _, loc := range locs {
		placeholderStart, key := loc[0], panelHTMLTemplate[loc[2]:loc[3]]
		tag := enclosingTag(panelHTMLTemplate, placeholderStart)
		if tag == "" {
			t.Errorf("{{%s}} at byte %d is not inside a recognizable opening tag", key, placeholderStart)
			continue
		}
		if !strings.Contains(tag, `data-i18n="`+key+`"`) && !strings.Contains(tag, `data-i18n-placeholder="`+key+`"`) {
			t.Errorf("the element rendering {{%s}} (%s) has no matching data-i18n/data-i18n-placeholder attribute, so it will never be re-translated client-side", key, tag)
		}
	}
}

// TestPanelTemplateHasNoActionsOrModelSourceHeader guards the accounts-table
// column history: v0.4.1 removed the old dedicated "model source" column
// (its value surfaces as the model input's title attribute instead) in favor
// of a new "Actions" column holding Save/Auto buttons. v0.6.0 went further
// and removed the Actions column too: every field now auto-saves itself on
// change/blur/Enter (see panel.go's "change"/"keydown" delegation), so there
// is no longer anything left to put in a dedicated actions column at all --
// "自动"/Auto is now a small inline text button next to the model input
// itself. This test's name and assertions were updated in lockstep with
// that removal (a stale assertion insisting the now-intentionally-removed
// column still exist would just force reintroducing dead UI clutter).
func TestPanelTemplateHasNoActionsOrModelSourceHeader(t *testing.T) {
	if strings.Contains(panelHTMLTemplate, "ui_col_actions") {
		t.Errorf("expected panelHTMLTemplate to no longer contain a ui_col_actions header (the Actions column was removed in v0.6.0), but it does")
	}
	if strings.Contains(panelHTMLTemplate, "ui_col_model_source") {
		t.Errorf("expected panelHTMLTemplate to no longer contain a ui_col_model_source header, but it does")
	}
}

// enclosingTag returns the nearest "<...>" opening tag that starts before
// pos in s (an ordinary text position, not inside a tag), i.e. the tag whose
// content includes the text at pos. Returns "" if none is found nearby.
func enclosingTag(s string, pos int) string {
	open := strings.LastIndex(s[:pos], "<")
	if open < 0 {
		return ""
	}
	closeAt := strings.Index(s[open:], ">")
	if closeAt < 0 {
		return ""
	}
	return s[open : open+closeAt+1]
}

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
		if !strings.Contains(body, "<title data-i18n=\"ui_page_title\">"+want+"</title>") {
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
