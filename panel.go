package main

import (
	"html"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handlePanelRequest serves the self-contained status page. It never touches
// CPA's own config or auth files -- everything it shows comes from GET
// .../status, fetched by the page's own JS after it loads.
func handlePanelRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var cfgPtr *pluginConfig
	if e := activeEngine(); e != nil {
		cfg := e.config()
		cfgPtr = &cfg
	}
	l := requestLang(cfgPtr, req)

	catalogJSON, err := i18nCatalogJSON()
	if err != nil {
		catalogJSON = []byte("{}")
	}

	body := renderPanelHTML(l, catalogJSON)
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{contentTypeHTML},
			"Cache-Control": []string{"no-store"},
		},
		Body: body,
	}
}

// renderPanelHTML fills panelHTMLTemplate's {{msg_key}} placeholders with l's
// translations (falling back to English for anything missing) for a
// flash-free initial paint, and separately injects the full multi-language
// catalog as window.__I18N__ so the page's own JS can re-render everything
// in the visitor's actual cli-proxy-language/navigator-detected language,
// which can differ from what the server guessed from Accept-Language.
//
// The server-side substitution above is not the only place these strings
// get set: every element carrying that substituted text also carries a
// data-i18n (or data-i18n-placeholder) attribute naming the same key, and
// the page's own JS re-applies t(key) to all of them as soon as it has
// resolved the visitor's actual language from cli-proxy-language/navigator.
// Without that second pass, a visitor whose Accept-Language disagrees with
// their stored console language (the common case for a headless browser,
// which sends its OS/CLI default Accept-Language regardless of what an
// operator set cli-proxy-language to) would see the server's first guess
// baked into static text forever, since only the *dynamically rendered*
// rows (config chips, accounts, recent) were ever re-translated client-side.
func renderPanelHTML(l lang, catalogJSON []byte) []byte {
	table, ok := messagesByLang[l]
	if !ok {
		table = messagesEN
	}
	replacements := make([]string, 0, len(messagesEN)*2+4)
	for key, enText := range messagesEN {
		text, ok := table[key]
		if !ok {
			text = enText
		}
		replacements = append(replacements, "{{"+string(key)+"}}", html.EscapeString(text))
	}
	replacements = append(replacements,
		"{{HTML_LANG}}", html.EscapeString(string(l)),
		"{{I18N_JSON}}", string(catalogJSON),
	)
	return []byte(strings.NewReplacer(replacements...).Replace(panelHTMLTemplate))
}

// panelHTMLTemplate is the entire self-contained panel page: inline CSS/JS,
// no external requests except the plugin's own GET .../status,
// GET .../run, GET .../set and GET .../config-yaml* endpoints (computed
// client-side, relative to this page's own URL, so it keeps working under
// any reverse-proxy prefix).
//
// The theme-following script block is adapted verbatim from
// cpa-usage-panel/panel.html (same STORAGE_KEY, same zustand-persist-or-bare
// value parsing) so this page matches whichever theme the Management Center
// is set to. The language-following script mirrors the equivalent real
// logic read out of a live /var/lib/cli-proxy-api/static/management.html
// build (localStorage key "cli-proxy-language", zustand-persist-or-bare
// value, region-based zh-TW detection, navigator.language fallback).
//
// v0.6.0 rewrote the CSS variables to mirror CPA's own Management Center
// design tokens verbatim (same custom-property names and values, extracted
// from a live management.html build), replacing this page's earlier
// bespoke palette (which used an orange accent unrelated to the console's
// own warm-gray primary color) -- see each :root/[data-theme] block below
// for the exact values. It also reworked the layout for embedding as
// /plugin-pages/cpa-quota-warmup/0 (an iframe sized to the console's own
// content area, not a standalone full-width page): no outer max-width, a
// compact chip-based config summary with rarely-touched settings collapsed
// into a <details>, a trimmed accounts table (no more dedicated "Actions"
// column -- every field auto-saves on change instead of via an explicit
// button), and a merged date+time column in the recent-results table.
const panelHTMLTemplate = `<!doctype html>
<html lang="{{HTML_LANG}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title data-i18n="ui_page_title">{{ui_page_title}}</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ccircle cx='8' cy='8' r='6' fill='none' stroke='%238b8680' stroke-width='2'/%3E%3Cpath d='M8 4v4l3 2' fill='none' stroke='%238b8680' stroke-width='2' stroke-linecap='round'/%3E%3C/svg%3E">
<script>
  // Mirror the CPA Management Center theme. It persists the choice under the
  // "cli-proxy-theme" key on this same origin and stamps data-theme on <html>:
  // "dark", "white", or no attribute for "auto" (follow the OS). Adapted
  // verbatim from cpa-usage-panel/panel.html.
  (function () {
    var STORAGE_KEY = "cli-proxy-theme";
    function storedTheme() {
      try {
        var raw = localStorage.getItem(STORAGE_KEY);
        if (!raw) { return "auto"; }
        var parsed = JSON.parse(raw);
        var value = parsed && parsed.state ? parsed.state.theme : parsed;
        return value === "dark" || value === "white" ? value : "auto";
      } catch (err) {
        return "auto";
      }
    }
    function applyTheme() {
      var theme = storedTheme();
      if (theme === "auto") {
        document.documentElement.removeAttribute("data-theme");
      } else {
        document.documentElement.setAttribute("data-theme", theme);
      }
    }
    applyTheme();
    window.addEventListener("storage", function (event) {
      if (!event.key || event.key === STORAGE_KEY) { applyTheme(); }
    });
    setInterval(applyTheme, 2000);
  })();
</script>
<script>
  // Mirror the CPA Management Center language. It persists the choice under
  // the "cli-proxy-language" key (zustand persist -- either a bare JSON
  // string or a {"state":{"language":...}} envelope, whichever this CPA
  // build wrote) as verified against a live management.html build. Falls
  // back to navigator.language using the same zh*/zh-TW-region/ru*/else-en
  // rule the console itself uses.
  (function () {
    var STORAGE_KEY = "cli-proxy-language";
    var VALID = ["zh-CN", "zh-TW", "en", "ru"];
    var TW_REGIONS = ["zh-tw", "zh-hk", "zh-mo", "zh-hant"];
    function isValid(v) { return VALID.indexOf(v) !== -1; }
    function parseStored(raw) {
      if (!raw) { return null; }
      try {
        var parsed = JSON.parse(raw);
        var value = (parsed && parsed.state && parsed.state.language) || (parsed && parsed.language) || parsed;
        if (typeof value === "string" && isValid(value)) { return value; }
      } catch (err) {
        if (isValid(raw)) { return raw; }
      }
      return null;
    }
    function fromNavigator() {
      if (typeof navigator === "undefined") { return "zh-CN"; }
      var tag = ((navigator.languages && navigator.languages[0]) || navigator.language || "zh-CN").toLowerCase();
      for (var i = 0; i < TW_REGIONS.length; i++) {
        if (tag.indexOf(TW_REGIONS[i]) === 0) { return "zh-TW"; }
      }
      if (tag.indexOf("zh") === 0) { return "zh-CN"; }
      if (tag.indexOf("ru") === 0) { return "ru"; }
      return "en";
    }
    var detected;
    try { detected = parseStored(localStorage.getItem(STORAGE_KEY)); } catch (err) { detected = null; }
    window.__cpaQuotaWarmupLang = detected || fromNavigator();
  })();
</script>
<style>
  /* Design tokens mirror CPA's own Management Center exactly (same custom
     property names and values, extracted from a live management.html
     build) so this page reads as part of the console when embedded as
     /plugin-pages/cpa-quota-warmup/0, not as a visually foreign iframe. */
  :root {
    color-scheme: light;
    --bg-primary: #f0eee8; --bg-secondary: #faf9f5; --bg-tertiary: #e9e6df; --bg-hover: #e9e6df;
    --text-primary: #2d2a26; --text-secondary: #6d6760; --text-tertiary: #a29c95;
    --border-color: #e3e1db; --border-primary: #d5d2cb;
    --primary-color: #8b8680; --primary-hover: #7f7a74;
    --success-color: #10b981; --error-color: #c65746;
    --warning-bg: #c657461f; --warning-text: var(--error-color);
    --success-badge-bg: #d1fae5; --success-badge-text: #065f46; --success-badge-border: #6ee7b7;
    --failure-badge-bg: #c6574624; --failure-badge-text: #8a3a30; --failure-badge-border: #c6574659;
    --count-badge-bg: #8b86802e;
    --shadow: 0 1px 2px 0 #00000014; --radius-md: 8px;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="white"]) {
      color-scheme: dark;
      --bg-primary: #1d1b18; --bg-secondary: #151412; --bg-tertiary: #262320; --bg-hover: #2e2a26;
      --text-primary: #f6f4f1; --text-secondary: #c9c3bb; --text-tertiary: #9c958d;
      --border-color: #3a3530; --border-primary: #4a453f;
      --primary-color: #8b8680; --primary-hover: #9a948e;
      --success-color: #10b981; --error-color: #c65746; --warning-text: #f1b0a6;
      --success-badge-bg: #064e3b4d; --success-badge-text: #6ee7b7; --success-badge-border: #059669;
      --failure-badge-bg: #c657463d; --failure-badge-text: #f1b0a6; --failure-badge-border: #c6574680;
      --shadow: 0 1px 3px 0 #0000004d;
    }
  }
  :root[data-theme="dark"] {
    color-scheme: dark;
    --bg-primary: #1d1b18; --bg-secondary: #151412; --bg-tertiary: #262320; --bg-hover: #2e2a26;
    --text-primary: #f6f4f1; --text-secondary: #c9c3bb; --text-tertiary: #9c958d;
    --border-color: #3a3530; --border-primary: #4a453f;
    --primary-color: #8b8680; --primary-hover: #9a948e;
    --success-color: #10b981; --error-color: #c65746; --warning-text: #f1b0a6;
    --success-badge-bg: #064e3b4d; --success-badge-text: #6ee7b7; --success-badge-border: #059669;
    --failure-badge-bg: #c657463d; --failure-badge-text: #f1b0a6; --failure-badge-border: #c6574680;
    --shadow: 0 1px 3px 0 #0000004d;
  }
  :root[data-theme="white"] {
    color-scheme: light;
    --bg-primary: #f0eee8; --bg-secondary: #faf9f5; --bg-tertiary: #e9e6df; --bg-hover: #e9e6df;
    --text-primary: #2d2a26; --text-secondary: #6d6760; --text-tertiary: #a29c95;
    --border-color: #e3e1db; --border-primary: #d5d2cb;
    --primary-color: #8b8680; --primary-hover: #7f7a74;
    --success-color: #10b981; --error-color: #c65746; --warning-text: var(--error-color);
    --success-badge-bg: #d1fae5; --success-badge-text: #065f46; --success-badge-border: #6ee7b7;
    --failure-badge-bg: #c6574624; --failure-badge-text: #8a3a30; --failure-badge-border: #c6574659;
    --shadow: 0 1px 2px 0 #00000014;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg-primary); color: var(--text-primary);
    font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC",
          "PingFang SC", "Microsoft YaHei", Roboto, Helvetica, Arial, sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  .wrap { max-width: none; padding: 16px 20px 40px; }
  header.top { display: flex; flex-wrap: wrap; gap: 16px; align-items: flex-end; justify-content: space-between; margin-bottom: 20px; }
  .title h1 { margin: 0; font-size: 21px; letter-spacing: -.01em; }
  .title p { margin: 4px 0 0; color: var(--text-secondary); font-size: 13px; }
  .btn {
    appearance: none; border: 1px solid var(--border-primary); background: var(--bg-secondary); color: var(--text-primary);
    font: inherit; font-size: 13px; padding: 0 12px; height: 32px; border-radius: 6px; cursor: pointer;
    display: inline-flex; align-items: center; box-sizing: border-box;
  }
  .btn:hover { background: var(--bg-hover); }
  .btn:focus-visible { outline: 2px solid var(--primary-color); outline-offset: 2px; }
  .btn-primary { background: var(--primary-color); border-color: var(--primary-color); color: #fff; }
  .btn-primary:hover { background: var(--primary-hover); border-color: var(--primary-hover); }
  input[type="text"], textarea {
    font: inherit; font-size: 13px; padding: 0 12px; height: 32px; border-radius: 6px;
    border: 1px solid var(--border-primary); background: var(--bg-secondary); color: var(--text-primary); min-width: 200px;
    box-sizing: border-box;
  }
  input[type="text"]:focus-visible, textarea:focus-visible, summary:focus-visible { outline: 2px solid var(--primary-color); outline-offset: 2px; }
  input[type="checkbox"] { width: 16px; height: 16px; vertical-align: middle; accent-color: var(--primary-color); }
  .model-edit { display: inline-flex; gap: 8px; align-items: center; flex-wrap: nowrap; }
  input.model-input { width: 14em; min-width: 0; }
  input.row-time { width: 12em; min-width: 0; }
  .combo { position: relative; display: inline-flex; align-items: stretch; }
  .combo .model-input { border-top-right-radius: 0; border-bottom-right-radius: 0; border-right: 0; }
  .combo-toggle {
    appearance: none; border: 1px solid var(--border-primary); background: var(--bg-secondary); color: var(--text-secondary);
    width: 24px; height: 32px; border-radius: 0 6px 6px 0; cursor: pointer; font-size: 10px;
    display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box;
  }
  .combo-toggle:hover { background: var(--bg-hover); }
  .combo-toggle:focus-visible { outline: 2px solid var(--primary-color); outline-offset: 2px; }
  .combo-menu {
    position: absolute; z-index: 1000; min-width: 220px; max-height: 280px; overflow: auto;
    background: var(--bg-secondary); border: 1px solid var(--border-primary); border-radius: 6px;
    box-shadow: var(--shadow-lg, 0 10px 18px -3px #0000001a); padding: 4px 0; font-size: 13px;
  }
  .combo-group { padding: 4px 12px; font-size: 11px; letter-spacing: .04em; text-transform: uppercase; color: var(--text-tertiary); }
  .combo-option { padding: 6px 12px; cursor: pointer; color: var(--text-primary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .combo-option:hover, .combo-option-active { background: var(--bg-hover); }
  .combo-option-current { font-weight: 700; }
  .combo-empty { padding: 6px 12px; color: var(--text-secondary); font-size: 12px; }
  .text-btn {
    appearance: none; border: 0; background: none; padding: 0; margin: 0;
    color: var(--primary-color); font: inherit; font-size: 12px; text-decoration: underline;
    cursor: pointer; white-space: nowrap;
  }
  .text-btn:focus-visible { outline: 2px solid var(--primary-color); outline-offset: 2px; }
  section.block { background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: var(--radius-md); padding: 16px; margin-bottom: 16px; box-shadow: var(--shadow); }
  .block-head { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; justify-content: space-between; margin-bottom: 12px; }
  .block-head h2 { margin: 0; font-size: 15px; font-weight: 620; }
  .block-head .controls { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .chips { display: flex; flex-wrap: wrap; gap: 8px; }
  .chip {
    display: inline-flex; align-items: center; gap: 4px; background: var(--bg-tertiary);
    border: 1px solid var(--border-color); border-radius: 999px; padding: 4px 12px; font-size: 12px;
  }
  .chip-label { color: var(--text-tertiary); }
  .chip-value { color: var(--text-primary); font-weight: 600; }
  .badge { display: inline-flex; align-items: center; border-radius: 999px; padding: 1px 8px; font-size: 12px; border: 1px solid transparent; }
  .badge-failure { background: var(--failure-badge-bg); color: var(--failure-badge-text); border-color: var(--failure-badge-border); }
  .count-badge { display: inline-flex; align-items: center; background: var(--count-badge-bg); color: var(--text-secondary); border-radius: 999px; padding: 1px 8px; font-size: 11px; }
  .status-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 4px; vertical-align: middle; }
  .status-dot-success { background: var(--success-color); }
  .status-muted { color: var(--text-secondary); }
  .row-flash { font-size: 11px; margin-left: 4px; }
  .row-flash-ok { color: var(--success-color); }
  .row-flash-fail { color: var(--error-color); }
  tr.row-saving { opacity: .55; transition: opacity .15s ease-out; }
  .covered-yes { color: var(--success-color); font-weight: 700; }
  .covered-no { color: var(--error-color); font-weight: 700; }
  details.advanced { margin-top: 12px; }
  details.advanced summary { cursor: pointer; color: var(--text-secondary); font-size: 13px; padding: 4px 0; }
  details.advanced summary:hover { color: var(--text-primary); }
  details.advanced .scroll { margin-top: 8px; }
  table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
  th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid var(--border-color); vertical-align: top; }
  th { font-size: 11px; letter-spacing: .05em; text-transform: uppercase; color: var(--text-tertiary); font-weight: 600; white-space: nowrap; }
  #accountsTable td { vertical-align: middle; }
  #accountsTable td.col-name, #accountsTable th.col-name { width: 260px; max-width: 260px; }
  #accountsTable th.col-status, #accountsTable td.col-status { min-width: 9em; white-space: normal; }
  #recentTable td.col-name { max-width: 220px; }
  .name-cell { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: middle; }
  tbody tr:last-child td { border-bottom: 0; }
  .scroll { overflow-x: auto; }
  table.kv td:first-child, table.kv th:first-child { width: 220px; color: var(--text-secondary); font-weight: 600; white-space: nowrap; }
  table.kv tr:last-child td, table.kv tr:last-child th { border-bottom: 0; }
  .yes { color: var(--success-color); }
  .no { color: var(--text-secondary); }
  .warn-text { color: var(--error-color); }
  .hint { color: var(--text-secondary); font-size: 12px; margin-top: 8px; }
  .empty { color: var(--text-secondary); padding: 20px 8px; text-align: center; }
  .err { background: color-mix(in srgb, var(--error-color) 12%, transparent); border: 1px solid var(--error-color); color: var(--error-color);
         border-radius: var(--radius-md); padding: 12px 16px; margin-bottom: 16px; font-size: 13px; }
  .err-warning { background: var(--warning-bg); border: 1px solid var(--failure-badge-border); color: var(--warning-text);
         border-radius: var(--radius-md); padding: 12px 16px; margin-bottom: 12px; font-size: 13px; }
  footer.meta { color: var(--text-secondary); font-size: 12px; }
  #runResult { margin-top: 12px; border-top: 1px solid var(--border-color); padding-top: 12px; }
  #runResult h3 { margin: 0 0 8px; font-size: 13px; font-weight: 620; }
  .config-yaml-editor {
    width: 100%; height: min(60vh, 520px); box-sizing: border-box; resize: vertical;
    font-family: ui-monospace, "SF Mono", "Cascadia Mono", "JetBrains Mono", "Fira Code", Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 12.5px; line-height: 1.5; tab-size: 2;
    padding: 12px; border-radius: 6px; border: 1px solid var(--border-color);
    background: var(--bg-tertiary); color: var(--text-primary);
  }
  [hidden] { display: none !important; }
</style>
</head>
<body>
<div class="wrap">
  <header class="top">
    <div class="title">
      <h1 data-i18n="ui_page_title">{{ui_page_title}}</h1>
      <p data-i18n="ui_page_subtitle">{{ui_page_subtitle}}</p>
    </div>
    <div class="controls">
      <button type="button" class="btn" id="refreshBtn" data-i18n="ui_refresh">{{ui_refresh}}</button>
      <button type="button" class="btn btn-primary" id="runBtn" data-i18n="ui_warm_up_now">{{ui_warm_up_now}}</button>
    </div>
  </header>

  <div class="err" id="error" hidden></div>

  <section class="block">
    <div class="block-head"><h2 data-i18n="ui_section_config">{{ui_section_config}}</h2></div>
    <div class="chips" id="configChips"></div>
    <div id="configGlobalModel" hidden></div>
    <details class="advanced" id="configAdvanced">
      <summary data-i18n="ui_advanced_settings">{{ui_advanced_settings}}</summary>
      <div class="scroll"><table class="kv"><tbody id="configAdvancedTable"></tbody></table></div>
    </details>
  </section>

  <section class="block">
    <div class="block-head">
      <h2 data-i18n="ui_section_accounts">{{ui_section_accounts}}</h2>
      <div class="controls">
        <input type="text" id="authFilter" placeholder="{{ui_auth_filter_placeholder}}" data-i18n-placeholder="ui_auth_filter_placeholder">
      </div>
    </div>
    <div class="scroll"><table id="accountsTable">
      <thead><tr>
        <th class="col-name" data-i18n="ui_col_name">{{ui_col_name}}</th><th data-i18n="ui_col_provider">{{ui_col_provider}}</th><th data-i18n="ui_label_enabled">{{ui_label_enabled}}</th><th data-i18n="ui_col_model">{{ui_col_model}}</th>
        <th data-i18n="ui_col_times">{{ui_col_times}}</th><th data-i18n="ui_col_next_trigger">{{ui_col_next_trigger}}</th><th class="col-status" data-i18n="ui_col_status">{{ui_col_status}}</th>
      </tr></thead>
      <tbody><tr><td colspan="7" class="empty" data-i18n="ui_loading">{{ui_loading}}</td></tr></tbody>
    </table></div>
    <div id="runResult" hidden>
      <h3 data-i18n="ui_run_result_title">{{ui_run_result_title}}</h3>
      <div id="runResultBody"></div>
    </div>
  </section>

  <section class="block" id="configYamlSection" hidden>
    <div class="block-head">
      <h2 data-i18n="ui_section_config_yaml">{{ui_section_config_yaml}}</h2>
      <div class="controls">
        <button type="button" class="btn" id="configYamlReloadBtn" data-i18n="ui_config_yaml_reload">{{ui_config_yaml_reload}}</button>
        <button type="button" class="btn btn-primary" id="configYamlSaveBtn" data-i18n="ui_config_yaml_save">{{ui_config_yaml_save}}</button>
      </div>
    </div>
    <div class="err-warning" id="configYamlError" hidden></div>
    <textarea id="configYamlEditor" class="config-yaml-editor" spellcheck="false"></textarea>
    <p class="hint" id="configYamlMeta"></p>
  </section>

  <section class="block">
    <div class="block-head"><h2 data-i18n="ui_section_recent">{{ui_section_recent}}</h2></div>
    <div class="scroll"><table id="recentTable">
      <thead><tr>
        <th data-i18n="ui_col_date">{{ui_col_date}}</th><th class="col-name" data-i18n="ui_col_auth">{{ui_col_auth}}</th>
        <th data-i18n="ui_col_provider">{{ui_col_provider}}</th><th data-i18n="ui_col_model">{{ui_col_model}}</th><th data-i18n="ui_col_covered">{{ui_col_covered}}</th>
        <th data-i18n="ui_col_rounds">{{ui_col_rounds}}</th><th data-i18n="ui_col_status_code">{{ui_col_status_code}}</th><th data-i18n="ui_col_warning">{{ui_col_warning}}</th>
      </tr></thead>
      <tbody><tr><td colspan="8" class="empty" data-i18n="ui_loading">{{ui_loading}}</td></tr></tbody>
    </table></div>
  </section>

  <footer class="meta" data-i18n="ui_footer_note">{{ui_footer_note}}</footer>
</div>
<div id="modelComboMenu" class="combo-menu" role="listbox" hidden></div>
<script id="i18n-data" type="application/json">{{I18N_JSON}}</script>
<script>
(function () {
  var catalog = {};
  try { catalog = JSON.parse(document.getElementById("i18n-data").textContent) || {}; } catch (err) { catalog = {}; }
  var lang = window.__cpaQuotaWarmupLang || "zh-CN";
  var dict = catalog[lang] || catalog.en || {};
  function t(key) { return (dict && dict[key]) || key; }
  document.documentElement.setAttribute("lang", lang);

  // The server already rendered every static label once, in whatever
  // language it guessed from Accept-Language (there is no localStorage to
  // read server-side). That guess is frequently wrong -- a headless browser
  // sends its own OS/CLI default Accept-Language regardless of what an
  // operator set cli-proxy-language to -- so every statically-labeled node
  // is re-applied here now that the real client-side language is known.
  // Anything rendered later (config chips, accounts, recent tables, the run
  // result) already goes through t() directly and needs no such pass.
  var i18nNodes = document.querySelectorAll("[data-i18n]");
  for (var ni = 0; ni < i18nNodes.length; ni++) {
    var node = i18nNodes[ni];
    node.textContent = t(node.getAttribute("data-i18n"));
  }
  var i18nPlaceholders = document.querySelectorAll("[data-i18n-placeholder]");
  for (var pi = 0; pi < i18nPlaceholders.length; pi++) {
    var placeholderNode = i18nPlaceholders[pi];
    placeholderNode.setAttribute("placeholder", t(placeholderNode.getAttribute("data-i18n-placeholder")));
  }
  document.title = t("ui_page_title");

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s === null || s === undefined ? "" : String(s);
    return d.innerHTML;
  }
  function boolText(b) { return b ? ('<span class="yes">' + esc(t("ui_yes")) + '</span>') : ('<span class="no">' + esc(t("ui_no")) + '</span>'); }
  function dash() { return esc(t("ui_none")); }

  // fmtDateTime formats an RFC3339 instant (next_trigger/last_tick) as a
  // compact local "MM-DD HH:MM" string; callers keep the original ISO
  // string as the element's title for anyone who wants the exact instant
  // (including its timezone offset).
  function fmtDateTime(iso) {
    if (!iso) { return ""; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return iso; }
    function p2(n) { return (n < 10 ? "0" : "") + n; }
    return p2(d.getMonth() + 1) + "-" + p2(d.getDate()) + " " + p2(d.getHours()) + ":" + p2(d.getMinutes());
  }

  // fmtRecentDateTime merges a slotRecord's separate "YYYY-MM-DD" Date and
  // "HH:MM" Time fields into one compact "MM-DD HH:MM" column.
  function fmtRecentDateTime(date, time) {
    var mmdd = date && date.length >= 10 ? date.slice(5) : (date || "");
    return time ? (mmdd ? (mmdd + " " + time) : time) : mmdd;
  }

  function showError(msg) {
    var box = document.getElementById("error");
    if (msg) { box.textContent = msg; box.hidden = false; } else { box.hidden = true; }
  }

  // --- console-API model lookup (v0.6.2 addendum) ----------------------
  //
  // The host ABI this plugin runs under exposes no "which models can this
  // specific auth actually reach" callback at all (see candidates.go's
  // modelsForProvider doc comment for the provider-inference fallback this
  // whole section exists to upgrade past when possible). The CPA console
  // itself already has exactly that, via its own account card's "模型"
  // button: GET /v0/management/auth-files/models?name=<auth>, returning
  // {"models":[{"id","display_name","type","owned_by"}]} (host source:
  // internal/api/handlers/management/auth_files.go, reading
  // registry.GetModelsForClient(authID)) -- authenticated with the
  // console's own management key (Authorization: Bearer <key>), which this
  // plugin's backend has no way to obtain (the host only stores its bcrypt
  // hash). This panel is served same-origin as the console as
  // /plugin-pages/cpa-quota-warmup/0 (an iframe), though, so when it is
  // actually embedded there it can read the *browser's* already-
  // authenticated session the exact same way the console's own JS does and
  // call this endpoint directly, client-side, never touching this plugin's
  // own backend at all.

  // consoleAuthPassphraseBytes returns the UTF-8 bytes of the XOR key the
  // console's own localStorage "cli-proxy-auth" encoding uses. Reproducible
  // here only because this page is embedded same-origin (location.host
  // matches) under the same browser session (navigator.userAgent matches)
  // as the console itself.
  function consoleAuthPassphraseBytes() {
    var passphrase = "cli-proxy-api-webui::secure-storage|" + window.location.host + "|" + navigator.userAgent;
    return new TextEncoder().encode(passphrase);
  }

  function base64ToBytes(b64) {
    var bin = atob(b64);
    var out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) { out[i] = bin.charCodeAt(i); }
    return out;
  }

  function xorBytes(data, key) {
    var out = new Uint8Array(data.length);
    for (var i = 0; i < data.length; i++) { out[i] = data[i] ^ key[i % key.length]; }
    return out;
  }

  // readConsoleManagementKey best-effort mirrors the CPA console's own
  // decoding of its localStorage "cli-proxy-auth" entry: a plaintext JSON
  // envelope, or (far more commonly) "enc::v1::" followed by
  // base64(XOR(utf8(JSON), consoleAuthPassphraseBytes())). Returns
  // {key, apiBase} on success, or null on *any* failure whatsoever (key
  // absent, wrong prefix, bad base64, bad JSON, no managementKey field, not
  // actually embedded in the console at all, TextEncoder/atob unsupported,
  // ...) -- this mechanism is always a best-effort upgrade, never a hard
  // requirement (see fetchConsoleModelsForAccount's own fallback). The
  // decoded key is only ever used as this function's own return value (an
  // Authorization header for a fetch call) -- never logged, never written
  // to the DOM, never sent to this plugin's own backend.
  function readConsoleManagementKey() {
    try {
      var manualKey = sessionStorage.getItem("quota-warmup-management-key") || localStorage.getItem("quota-warmup-management-key");
      if (manualKey) {
        return { key: manualKey, apiBase: window.location.origin };
      }
      var raw = localStorage.getItem("cli-proxy-auth");
      if (!raw) { return null; }
      var obj;
      if (raw.indexOf("enc::v1::") === 0) {
        var cipherBytes = base64ToBytes(raw.slice(9));
        var plainBytes = xorBytes(cipherBytes, consoleAuthPassphraseBytes());
        obj = JSON.parse(new TextDecoder("utf-8").decode(plainBytes));
      } else {
        obj = JSON.parse(raw);
      }
      var managementKey = obj && obj.state && obj.state.managementKey;
      if (!managementKey || typeof managementKey !== "string") { return null; }
      var apiBase = (obj.state.apiBase && String(obj.state.apiBase)) || window.location.origin;
      return { key: managementKey, apiBase: apiBase };
    } catch (err) {
      var manualKey = sessionStorage.getItem("quota-warmup-management-key") || localStorage.getItem("quota-warmup-management-key");
      if (manualKey) {
        return { key: manualKey, apiBase: window.location.origin };
      }
      return null;
    }
  }

  function getManagementApi() {
    var auth = readConsoleManagementKey();
    var apiOrigin = (auth && auth.apiBase) ? auth.apiBase.replace(/\/+$/, "") : window.location.origin;
    var mgmtBase = apiOrigin + "/v0/management/plugins/cpa-quota-warmup/";
    var headers = {};
    if (auth && auth.key) {
      headers["Authorization"] = "Bearer " + auth.key;
    }
    return { base: mgmtBase, headers: headers, auth: auth };
  }

  function promptForManagementKey(customMsg) {
    var key = prompt((customMsg ? (customMsg + "\n") : "") + "请输入 CPA 管理密钥 (Management Key)：");
    if (key && key.trim()) {
      sessionStorage.setItem("quota-warmup-management-key", key.trim());
      load();
      return true;
    }
    return false;
  }

  // accountModelsCache/consoleModelsTTLMs are declared further below,
  // alongside the rest of the model-combo state, since they are read
  // synchronously by openCombo -- see that function's own doc comment for
  // the full cache/fallback flow this feeds.

  // fetchConsoleModelsForAccount resolves to this account's real available
  // models (as combo entries: {id, group:"", displayName}) on success, or
  // null on any failure (no management key available, 401, network error,
  // malformed response, ...) -- callers fall back to the provider-inferred
  // list and surface a footer note when this resolves to null. Successful
  // results are cached for consoleModelsTTLMs.
  function fetchConsoleModelsForAccount(name) {
    var cached = accountModelsCache[name];
    if (cached && cached.expiresAt > Date.now()) {
      return Promise.resolve(cached.entries);
    }
    var auth = readConsoleManagementKey();
    if (!auth) { return Promise.resolve(null); }
    var url = auth.apiBase.replace(/\/+$/, "") + "/v0/management/auth-files/models?name=" + encodeURIComponent(name);
    return fetch(url, { headers: { "Authorization": "Bearer " + auth.key }, cache: "no-store" })
      .then(function (resp) {
        if (!resp.ok) { throw new Error("status " + resp.status); }
        return resp.json();
      })
      .then(function (body) {
        var entries = ((body && body.models) || []).map(function (m) {
          return { id: m.id, group: "", displayName: m.display_name || "" };
        });
        accountModelsCache[name] = { entries: entries, expiresAt: Date.now() + consoleModelsTTLMs };
        return entries;
      })
      .catch(function () { return null; });
  }

  // nameCellHTML truncates a long auth file name (a full email address plus
  // provider/team suffix routinely exceeds the column width) with an
  // ellipsis, keeping the untruncated name available as the title tooltip.
  function nameCellHTML(name) {
    return '<span class="name-cell" title="' + esc(name) + '">' + esc(name) + '</span>';
  }

  function providerBadgeHTML(provider) {
    return provider ? ('<span class="count-badge">' + esc(provider) + '</span>') : "";
  }

  // modelCellHTML builds the "<input> + ▾ + 自动" control used both for the
  // config section's global-model editor (scope="global") and each
  // inline-mode account row's model cell (scope="auth"). There is no
  // explicit Save button any more (see v0.6.0's auto-save-on-change rework
  // below): the input commits itself on change (blur) or Enter, and "自动"
  // is a small inline text button rather than a full-size one.
  // currentValue "auto" (or empty) leaves the input blank -- the
  // placeholder communicates that auto-selection is in effect -- so a
  // beginner can never accidentally type the literal word "auto" into a
  // real model name field. The ".combo"/".combo-toggle" wrapper drives the
  // custom dropdown (see modelComboMenu below) -- v0.6.1 replaced the
  // earlier native datalist-based suggestion list, which only popped up
  // when the current value was a *prefix* match of an option, so it
  // silently showed nothing once a field already held a full model name or
  // "auto".
  function modelCellHTML(scope, authName, currentValue, modelSource) {
    var attrs = 'data-scope="' + esc(scope) + '" data-auth="' + esc(authName || "") + '"';
    var value = (!currentValue || currentValue === "auto") ? "" : currentValue;
    var title = modelSource ? ' title="' + esc(modelSource) + '"' : "";
    return '<span class="model-edit">' +
      '<span class="combo"><input type="text" class="model-input" ' + attrs +
      ' value="' + esc(value) + '" placeholder="' + esc(t("ui_model_placeholder")) + '"' + title + ' autocomplete="off">' +
      '<button type="button" class="combo-toggle" aria-label="' + esc(t("ui_expand")) + '">▾</button></span>' +
      '<button type="button" class="text-btn model-auto-btn" ' + attrs + '>' + esc(t("ui_model_auto")) + '</button>' +
      '</span>';
  }

  // fileModelCellHTML is modelCellHTML's file-mode counterpart: no
  // data-scope/data-auth (file-mode rows save via their own <tr data-name>,
  // not the scope/auth-keyed /set call inline mode uses), and a distinct
  // ".row-model"/".row-model-auto" class pair so the two save paths' event
  // delegation never cross-fires on the same element.
  function fileModelCellHTML(currentValue, modelSource) {
    var value = (!currentValue || currentValue === "auto") ? "" : currentValue;
    var title = modelSource ? ' title="' + esc(modelSource) + '"' : "";
    return '<span class="model-edit">' +
      '<span class="combo"><input type="text" class="model-input row-model" value="' + esc(value) +
      '" placeholder="' + esc(t("ui_model_placeholder")) + '"' + title + ' autocomplete="off">' +
      '<button type="button" class="combo-toggle" aria-label="' + esc(t("ui_expand")) + '">▾</button></span>' +
      '<button type="button" class="text-btn row-model-auto">' + esc(t("ui_model_auto")) + '</button>' +
      '</span>';
  }

  // comboModels is the shared available_models list every model-input's
  // custom dropdown (see modelComboMenu below) reads from -- the accounts
  // table's per-row inputs and the config section's global-model input all
  // share this one array, refreshed on every load() (see setComboModels).
  var comboModels = [];
  function setComboModels(models) { comboModels = models || []; }

  // --- model combobox -------------------------------------------------
  //
  // v0.6.1 replaced the native datalist-based suggestion list every
  // model-input used to carry with this custom dropdown: a browser-native
  // datalist popup only appears when the field's *current* value is a
  // prefix match of one of its options, so once a field already held a
  // full model name (or the empty/"auto" placeholder state), clicking it
  // showed nothing at all -- confirmed on a live deployment with 17 real
  // options present. This dropdown always shows the full list on open,
  // filters by substring as the user types, and is a single DOM node
  // (modelComboMenu, defined once in the static HTML, absolutely
  // positioned over whichever input opened it) shared by every model-input
  // on the page rather than one suggestion list per row -- both because
  // the underlying model list is identical everywhere (comboModels) and so
  // it survives every table re-render untouched (renderAccounts only
  // replaces #accountsTable's <tbody>, never anything at body level).
  var comboMenuEl = document.getElementById("modelComboMenu");
  var comboActiveInput = null; // the <input> the open menu belongs to, or null when closed
  var comboCurrentValue = "auto"; // that input's value *at the moment the menu opened* (for the "current" highlight; deliberately not re-read on every keystroke, or the highlight would chase the in-progress filter text instead of marking the account's actual saved model)
  var comboItems = []; // flat list of {el, value} for the currently-rendered options, in visual/keyboard-nav order (group headings are not included -- they are not selectable)
  var comboActiveIndex = -1; // index into comboItems the keyboard cursor is on, -1 = none

  // accountModels (v0.6.2) maps an account name to its own provider-filtered
  // model list (status' per-account "models" field, computed server-side by
  // modelsForProvider in candidates.go) -- rebuilt fresh on every
  // renderAccounts call. This is the fallback source for a per-account
  // row's model combo whenever the console-API source below is unavailable
  // or fails; the config section's global-model editor (inline mode) is not
  // inside any accounts-table row and always uses the full comboModels list
  // instead (see comboEntriesForInput/openCombo).
  var accountModels = {};

  // accountModelsCache (v0.6.2 addendum) caches this account's *actual*
  // available models, fetched from the CPA console's own authenticated
  // GET /v0/management/auth-files/models?name=<auth> endpoint (see
  // readConsoleManagementKey's doc comment for how/why this panel can call
  // it at all) -- name -> { entries: [{id, group:"", displayName}], expiresAt }.
  // A 5-minute TTL keeps repeated opens of the same row from re-fetching on
  // every click while still picking up real changes reasonably soon.
  var accountModelsCache = {};
  var consoleModelsTTLMs = 5 * 60 * 1000;

  function comboAutoLabel() {
    return "auto (" + t("ui_model_auto_full") + ")";
  }

  // comboEntriesForInput normalizes the *fallback* (provider-inferred)
  // model source for input into a flat {id, group} array renderComboMenu
  // can render uniformly: a per-account row's own list (group always "" --
  // no owned_by grouping at all, since status' per-account "models" is
  // already just a flat list of ids, not {id, owned_by} objects, and the
  // row-level dropdown is not supposed to be grouped in the first place),
  // or -- when input is not inside an accounts-table row at all, i.e. the
  // config section's global-model editor -- the full comboModels list
  // grouped by owned_by. See openCombo for where the console-API source
  // (accountModelsCache) is preferred over this when available.
  function comboEntriesForInput(input) {
    var row = input.closest("tr[data-name]");
    if (row) {
      var name = row.getAttribute("data-name");
      var list = Object.prototype.hasOwnProperty.call(accountModels, name) ? accountModels[name] : [];
      return list.map(function (id) { return { id: id, group: "" }; });
    }
    return comboModels.map(function (m) { return { id: m.id, group: m.owned_by || "" }; });
  }

  // renderComboMenu (re)builds the menu's contents from entries (see
  // comboEntriesForInput) for the given filter query (substring match,
  // case-insensitive, against the model id, its group, and its display
  // name) and rebuilds comboItems/resets the keyboard cursor. The pinned
  // "auto (...)" entry is never filtered out ("固定" in the spec) -- it is
  // always a valid choice regardless of what has been typed. When nothing
  // else matches (an empty per-provider list, most commonly -- see the
  // spec's explicit "该 provider 暂无可用模型" requirement), a single
  // non-selectable gray line is shown instead of any group/option rows.
  // footerNote (optional), when given, is appended as one more such gray
  // line below everything else -- used for the "已按 provider 推断（未取到
  // 账号模型）" note when the console-API fetch failed.
  function renderComboMenu(query, entries, footerNote) {
    var q = (query || "").trim().toLowerCase();
    comboItems = [];
    var frag = document.createDocumentFragment();

    var autoOpt = document.createElement("div");
    autoOpt.className = "combo-option";
    autoOpt.setAttribute("role", "option");
    autoOpt.setAttribute("data-value", "auto");
    autoOpt.textContent = comboAutoLabel();
    if (comboCurrentValue === "auto") { autoOpt.classList.add("combo-option-current"); }
    comboItems.push({ el: autoOpt, value: "auto" });
    frag.appendChild(autoOpt);

    var groups = {}, order = [];
    (entries || []).forEach(function (e) {
      var g = e.group || "";
      var hay = (e.id + " " + g + " " + (e.displayName || "")).toLowerCase();
      if (q && hay.indexOf(q) === -1) { return; }
      if (!groups[g]) { groups[g] = []; order.push(g); }
      groups[g].push(e);
    });

    if (!order.length) {
      var empty = document.createElement("div");
      empty.className = "combo-empty";
      empty.textContent = t("ui_no_provider_models");
      frag.appendChild(empty);
    } else {
      order.forEach(function (g) {
        if (g) {
          var head = document.createElement("div");
          head.className = "combo-group";
          head.textContent = g;
          frag.appendChild(head);
        }
        groups[g].forEach(function (e) {
          var opt = document.createElement("div");
          opt.className = "combo-option";
          opt.setAttribute("role", "option");
          opt.setAttribute("data-value", e.id);
          opt.textContent = e.id;
          if (e.displayName && e.displayName !== e.id) { opt.title = e.displayName; }
          if (e.id === comboCurrentValue) { opt.classList.add("combo-option-current"); }
          comboItems.push({ el: opt, value: e.id });
          frag.appendChild(opt);
        });
      });
    }

    if (footerNote) {
      var foot = document.createElement("div");
      foot.className = "combo-empty";
      foot.textContent = footerNote;
      frag.appendChild(foot);
    }

    comboMenuEl.innerHTML = "";
    comboMenuEl.appendChild(frag);
    comboActiveIndex = -1;
  }

  // renderComboLoading shows just the pinned "auto" entry plus a "加载中…"
  // placeholder line while a console-API fetch (see openCombo) is in
  // flight, so the menu never appears to hang empty.
  function renderComboLoading() {
    comboItems = [];
    var frag = document.createDocumentFragment();
    var autoOpt = document.createElement("div");
    autoOpt.className = "combo-option";
    autoOpt.setAttribute("role", "option");
    autoOpt.setAttribute("data-value", "auto");
    autoOpt.textContent = comboAutoLabel();
    if (comboCurrentValue === "auto") { autoOpt.classList.add("combo-option-current"); }
    comboItems.push({ el: autoOpt, value: "auto" });
    frag.appendChild(autoOpt);
    var loading = document.createElement("div");
    loading.className = "combo-empty";
    loading.textContent = t("ui_loading");
    frag.appendChild(loading);
    comboMenuEl.innerHTML = "";
    comboMenuEl.appendChild(frag);
    comboActiveIndex = -1;
  }

  // positionComboMenu places the (already-visible, already-rendered) menu
  // directly under input, flipping above it instead when there is not
  // enough room below the viewport -- the spec's "若超出视口底部则向上弹".
  function positionComboMenu(input) {
    var rect = input.closest(".combo").getBoundingClientRect();
    var menuHeight = Math.min(comboMenuEl.scrollHeight, 280);
    var spaceBelow = window.innerHeight - rect.bottom;
    var top;
    if (spaceBelow < menuHeight + 8 && rect.top > menuHeight + 8) {
      top = rect.top + window.scrollY - menuHeight - 4;
    } else {
      top = rect.bottom + window.scrollY + 4;
    }
    comboMenuEl.style.top = top + "px";
    comboMenuEl.style.left = (rect.left + window.scrollX) + "px";
    comboMenuEl.style.minWidth = Math.max(rect.width, 220) + "px";
  }

  // openCombo always shows the *full* (unfiltered) list, regardless of the
  // input's current value -- per the spec, clicking the toggle or focusing/
  // clicking the input always expands the complete list; only subsequently
  // typing narrows it (see the "input" listener below).
  //
  // Per-account rows prefer the console-API source (this account's real
  // available models, fetched from the CPA console's own authenticated
  // endpoint -- see readConsoleManagementKey) over the provider-inferred
  // fallback (comboEntriesForInput) whenever a management key is available
  // at all: cached (still fresh) results render immediately and
  // synchronously; an uncached lookup shows a brief loading placeholder and
  // swaps in the real result (or falls back, with a footer note, on
  // failure) once the request settles. The config section's global-model
  // editor (no enclosing <tr data-name>) never attempts the console API at
  // all -- there is no single account to look it up for -- and always uses
  // the full comboModels list synchronously, exactly as before.
  function openCombo(input) {
    comboActiveInput = input;
    comboCurrentValue = (input.value || "").trim() || "auto";
    comboMenuEl.hidden = false;

    var row = input.closest("tr[data-name]");
    var name = row ? row.getAttribute("data-name") : null;
    if (!name) {
      renderComboMenu("", comboEntriesForInput(input));
      positionComboMenu(input);
      return;
    }

    var cached = accountModelsCache[name];
    if (cached && cached.expiresAt > Date.now()) {
      renderComboMenu("", cached.entries);
      positionComboMenu(input);
      return;
    }

    if (!readConsoleManagementKey()) {
      renderComboMenu("", comboEntriesForInput(input));
      positionComboMenu(input);
      return;
    }

    renderComboLoading();
    positionComboMenu(input);
    fetchConsoleModelsForAccount(name).then(function (entries) {
      // The operator may have already closed this combo, or opened a
      // different row's, by the time the request settles -- only apply the
      // result if it still belongs to the field the menu is showing.
      if (comboActiveInput !== input) { return; }
      // Only filter by what the operator has typed *since opening*; the
      // pre-existing value (e.g. "gpt-5.6-luna") must not hide the rest of
      // the list the way it would on a fresh open.
      var typed = (input.value || "").trim();
      var query = typed === comboCurrentValue || (typed === "" && comboCurrentValue === "auto") ? "" : typed;
      if (entries) {
        renderComboMenu(query, entries);
      } else {
        renderComboMenu(query, comboEntriesForInput(input), t("ui_model_fallback_hint"));
      }
      positionComboMenu(input);
    });
  }

  function closeCombo() {
    comboMenuEl.hidden = true;
    comboMenuEl.innerHTML = "";
    comboActiveInput = null;
    comboItems = [];
    comboActiveIndex = -1;
  }

  function comboMoveActive(delta) {
    if (!comboItems.length) { return; }
    if (comboActiveIndex >= 0 && comboItems[comboActiveIndex]) {
      comboItems[comboActiveIndex].el.classList.remove("combo-option-active");
    }
    comboActiveIndex = (comboActiveIndex + delta + comboItems.length) % comboItems.length;
    var active = comboItems[comboActiveIndex].el;
    active.classList.add("combo-option-active");
    if (active.scrollIntoView) { active.scrollIntoView({ block: "nearest" }); }
  }

  // comboSelect commits value into whichever input the open menu belongs
  // to. A real model id just becomes the input's new value, dispatching a
  // "change" event so the existing auto-save delegation (see "change"
  // listener below) picks it up exactly as if the operator had typed it and
  // blurred. "auto" is special-cased to instead simulate a click on this
  // same field's own "自动"/Auto text button: the generic change handler
  // deliberately treats an *empty* value as "nothing to do" (so blurring an
  // untouched, already-blank field never spams a save), and only the
  // dedicated auto button/click path is defined to mean "clear to auto".
  function comboSelect(value) {
    if (!comboActiveInput) { return; }
    var input = comboActiveInput;
    closeCombo();
    if (value === "auto") {
      var wrapper = input.closest(".model-edit");
      var autoBtn = wrapper ? wrapper.querySelector(".row-model-auto, .model-auto-btn") : null;
      if (autoBtn) { autoBtn.click(); }
      return;
    }
    input.value = value;
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  // Any click is checked against (in order): a combo option (select it), the
  // ▾ toggle (open its own input's combo), the model-input itself (open),
  // or -- if a combo is currently open and the click landed on neither the
  // menu nor any .combo -- treated as an outside click that closes it.
  document.addEventListener("click", function (event) {
    var target = event.target;

    var optionEl = target.closest ? target.closest(".combo-option") : null;
    if (optionEl && comboMenuEl.contains(optionEl)) {
      comboSelect(optionEl.getAttribute("data-value"));
      return;
    }

    var toggleBtn = target.closest ? target.closest(".combo-toggle") : null;
    if (toggleBtn) {
      var comboWrap = toggleBtn.closest(".combo");
      var input = comboWrap ? comboWrap.querySelector(".model-input") : null;
      if (input) { input.focus(); openCombo(input); }
      return;
    }

    if (target.classList && target.classList.contains("model-input")) {
      openCombo(target);
      return;
    }

    if (comboActiveInput && !comboMenuEl.contains(target) && !(target.closest && target.closest(".combo"))) {
      closeCombo();
    }
  });

  // Re-focusing a model-input (tabbing in, or clicking one that was not
  // already focused -- "focusin" is used instead of "focus" because plain
  // "focus" does not bubble, and this page listens at the document level so
  // it keeps working after every table re-render) also always opens the
  // full list, same as the click handler above.
  document.addEventListener("focusin", function (event) {
    var target = event.target;
    if (target.classList && target.classList.contains("model-input")) {
      openCombo(target);
    }
  });

  // Typing narrows the open menu to a case-insensitive substring match.
  document.addEventListener("input", function (event) {
    var target = event.target;
    if (target.classList && target.classList.contains("model-input") && comboActiveInput === target) {
      // Typing filters whichever source is already on screen -- the
      // console-API result if it is cached (even a still-loading fetch
      // will overwrite this once it settles, re-applying target.value at
      // that point, see openCombo), otherwise the provider-inferred
      // fallback. Never triggers a new console-API request itself.
      var row = target.closest("tr[data-name]");
      var name = row ? row.getAttribute("data-name") : null;
      var cached = name ? accountModelsCache[name] : null;
      if (cached && cached.expiresAt > Date.now()) {
        renderComboMenu(target.value, cached.entries);
      } else {
        renderComboMenu(target.value, comboEntriesForInput(target));
      }
      positionComboMenu(target);
    }
  });

  // chip renders one compact rounded pill in the config summary's chip row.
  function chip(label, value, title) {
    var titleAttr = title ? ' title="' + esc(title) + '"' : "";
    return '<span class="chip"' + titleAttr + '><span class="chip-label">' + esc(label) + '</span><span class="chip-value">' + value + '</span></span>';
  }

  // renderConfigChips renders the always-visible compact summary: mode,
  // this mode's primary schedule field (config file path for file mode,
  // the raw time expression for either inline mode), timezone, language,
  // and the most recent tick -- plus a v3InlineMode-only always-visible
  // global-model editor (the mode's single most important lever, so it is
  // deliberately not tucked into the collapsed "Advanced settings" below).
  // Rarely-touched settings (base URL, message, max tokens/rounds,
  // catch-up window) move to renderConfigAdvanced instead.
  function renderConfigChips(cfg, lastTick, lastTickError) {
    cfg = cfg || {};
    var isFile = cfg.mode === "file";
    var isV3Inline = cfg.mode === "inline" && !cfg.legacy_mode;
    var chips = [];
    chips.push(chip(t("ui_label_mode"), esc(isFile ? t("ui_mode_file") : t("ui_mode_inline"))));
    if (isFile) {
      chips.push(chip(t("ui_label_config_file"), esc(cfg.config_file) || dash(), cfg.config_file));
      if (cfg.config_file_error) {
        chips.push('<span class="badge badge-failure" title="' + esc(cfg.config_file_error) + '">' + esc(t("ui_label_parse_error")) + '</span>');
      }
    } else {
      chips.push(chip(t("ui_label_time"), esc((cfg.time || []).join(", ")) || dash()));
      if (isV3Inline) {
        if (cfg.accounts && cfg.accounts.length) {
          chips.push(chip(t("ui_label_accounts"), esc(cfg.accounts.join(", "))));
        } else {
          chips.push('<span class="badge badge-failure">' + esc(t("ui_no_accounts_hint")) + '</span>');
        }
      }
    }
    var tzText = esc(cfg.timezone) || dash();
    if (cfg.timezone_auto) {
      tzText += " (" + esc(t("ui_timezone_auto")) + ")";
    } else if (!cfg.legacy_mode) {
      tzText += " (" + esc(t("ui_timezone_manual")) + ")";
    }
    chips.push(chip(t("ui_label_timezone"), tzText));
    chips.push(chip(t("ui_label_language"), esc(cfg.language) || dash()));
    chips.push(chip(t("ui_label_last_tick"), lastTick ? esc(fmtDateTime(lastTick)) : dash(), lastTick || ""));
    if (lastTickError) {
      chips.push('<span class="badge badge-failure" title="' + esc(lastTickError) + '">' + esc(t("ui_label_last_tick_error")) + '</span>');
    }
    // v0.6.2 addendum: whether the account rows' model dropdown can reach
    // the console's own per-account model endpoint (see
    // readConsoleManagementKey) or is falling back to provider inference --
    // a coarse, page-wide indicator of which *mechanism* is available, not
    // a guarantee every single row's lookup will succeed (a management key
    // being present does not rule out an individual 401/network failure,
    // which is instead surfaced inline in that row's own combo menu).
    var modelSourceMode = readConsoleManagementKey() ? t("ui_model_source_console") : t("ui_model_source_provider");
    chips.push(chip(t("ui_label_model_source"), esc(modelSourceMode)));
    document.getElementById("configChips").innerHTML = chips.join("");

    var globalModelBox = document.getElementById("configGlobalModel");
    if (isV3Inline) {
      globalModelBox.hidden = false;
      globalModelBox.innerHTML = '<div class="hint" style="margin-top:12px">' + esc(t("ui_label_global_model")) + '</div>' + modelCellHTML("global", "", cfg.model);
    } else {
      globalModelBox.hidden = true;
      globalModelBox.innerHTML = "";
    }
  }

  // renderConfigAdvanced fills the collapsed "高级设置"/Advanced settings
  // <details> with the settings that are set once and rarely touched again.
  function renderConfigAdvanced(cfg) {
    cfg = cfg || {};
    var rows = [
      [t("ui_label_base_url"), esc(cfg.base_url) || dash()],
      [t("ui_label_message"), esc(cfg.message) || dash()],
      [t("ui_label_max_tokens"), esc(cfg.max_tokens)],
      [t("ui_label_max_rounds"), esc(cfg.max_rounds)],
      [t("ui_label_catch_up_minutes"), esc(cfg.catch_up_minutes)]
    ];
    var html = rows.map(function (r) {
      return "<tr><th>" + esc(r[0]) + "</th><td>" + r[1] + "</td></tr>";
    }).join("");
    document.getElementById("configAdvancedTable").innerHTML = html;
  }

  // statusCellHTML renders the "Status" column for either row type: a
  // normal enabled account gets a green dot plus "已启用"/Enabled; a
  // skipped one (not enabled, not in accounts[], etc.) gets its server-
  // provided reason in muted gray -- it is the normal, most common state
  // for a fresh install with many accounts, not an error, and should not
  // read like one. A real problem (an invalid time expression or an
  // unavailable model) is layered on top as a small failure-colored badge,
  // never replacing the base enabled/skipped state.
  function statusCellHTML(a) {
    var main = a.skipped
      ? ('<span class="status-muted">' + esc(a.skipped) + '</span>')
      : ('<span class="status-dot status-dot-success"></span><span>' + esc(t("ui_status_enabled")) + '</span>');
    var warn = a.warning ? (' <span class="badge badge-failure">' + esc(a.warning) + '</span>') : "";
    // model_hint (v0.6.2): a non-blocking gray note -- unlike Warning above,
    // this never means the pinned model is actually broken, only that it is
    // not on this account's own provider-filtered list (see
    // modelsForProvider's doc comment in candidates.go for why that can be
    // a false alarm), so it is deliberately styled muted rather than as a
    // failure badge.
    var hint = a.model_hint ? ('<br><span class="status-muted">' + esc(a.model_hint) + '</span>') : "";
    return main + warn + hint;
  }

  // fileRowHTML renders one file-mode account row: an editable enabled
  // checkbox, model input (with an inline "自动"/Auto text button), and
  // time input. v0.6.0 removed the row's old explicit "保存"/Save button:
  // every field now auto-saves itself on change (checkbox) or on blur/Enter
  // (text inputs) -- see the "change"/"keydown" delegation below.
  function fileRowHTML(a) {
    var enabledCell = '<input type="checkbox" class="row-enabled"' + (a.enabled ? " checked" : "") + '>';
    var modelCell = fileModelCellHTML(a.model, a.model_source);
    var timeCell = '<input type="text" class="row-time" value="' + esc((a.times || []).join(", ")) + '" placeholder="' + esc(t("ui_time_placeholder")) + '">';
    var nextTrigger = a.next_trigger ? ('<span title="' + esc(a.next_trigger) + '">' + esc(fmtDateTime(a.next_trigger)) + '</span>') : dash();
    return "<tr data-name=\"" + esc(a.name) + "\"><td class=\"col-name\">" + nameCellHTML(a.name) + "</td><td>" + providerBadgeHTML(a.provider) + "</td><td>" +
      enabledCell + "</td><td>" + modelCell + "</td><td>" +
      timeCell + "</td><td>" + nextTrigger + "</td><td class=\"col-status\">" + statusCellHTML(a) + "</td></tr>";
  }

  // inlineRowHTML renders one v0.3.0-style (inline-mode) account row: the
  // enabled/time columns are read-only display (this format has no
  // per-account enabled/time editing at all -- only the model, via the
  // panel/account/global/provider/auto override chain).
  function inlineRowHTML(a) {
    var modelCell = a.enabled ? modelCellHTML("auth", a.name, a.model, a.model_source) : (esc(a.model) || dash());
    var nextTrigger = a.next_trigger ? ('<span title="' + esc(a.next_trigger) + '">' + esc(fmtDateTime(a.next_trigger)) + '</span>') : dash();
    return "<tr data-name=\"" + esc(a.name) + "\"><td class=\"col-name\">" + nameCellHTML(a.name) + "</td><td>" + providerBadgeHTML(a.provider) + "</td><td>" + dash() + "</td><td>" +
      modelCell + "</td><td>" + esc((a.times || []).join(", ")) + "</td><td>" +
      nextTrigger + "</td><td class=\"col-status\">" + statusCellHTML(a) + "</td></tr>";
  }

  function renderAccounts(auths, isFile) {
    var rows = auths || [];
    // Rebuilt from scratch every render (not merged/accumulated) so a
    // vanished account's stale entry is never left behind.
    accountModels = {};
    rows.forEach(function (a) { accountModels[a.name] = a.models || []; });
    var body = rows.map(isFile ? fileRowHTML : inlineRowHTML).join("");
    document.querySelector("#accountsTable tbody").innerHTML = body || ('<tr><td colspan="7" class="empty">' + dash() + '</td></tr>');
  }

  // reloadTimer/scheduleReload debounce the full-table refresh that follows
  // a successful auto-save: reloading immediately (the old explicit-Save
  // behavior) would blow away the "已保存"/Saved flash the very save it is
  // reacting to just showed, and would refetch on every single keystroke's
  // worth of rapid edits across different fields/rows. Scheduling it after
  // a short delay lets the flash actually be seen and coalesces bursts of
  // edits into one reload.
  var reloadTimer = null;
  function scheduleReload(delayMs) {
    if (reloadTimer) { clearTimeout(reloadTimer); }
    reloadTimer = setTimeout(function () { reloadTimer = null; load(); }, delayMs);
  }

  // flashRow shows a short-lived (success) or persistent (failure) inline
  // note in row's own status cell -- see the accounts-table row functions'
  // doc comments for why there is no dedicated "Actions"/save-result column
  // any more.
  function flashRow(row, ok, message) {
    var cell = row.querySelector(".col-status");
    if (!cell) { return; }
    var old = cell.querySelector(".row-flash");
    if (old && old.parentNode) { old.parentNode.removeChild(old); }
    var span = document.createElement("span");
    span.className = "row-flash " + (ok ? "row-flash-ok" : "row-flash-fail");
    span.textContent = message || (ok ? t("ui_set_saved") : t("ui_set_failed"));
    cell.appendChild(document.createTextNode(" "));
    cell.appendChild(span);
    if (ok) {
      setTimeout(function () { if (span.parentNode) { span.parentNode.removeChild(span); } }, 1600);
    }
  }

  // setFileAccount calls the file-mode /set route: only the parameters
  // actually passed (non-null) are written, leaving the others untouched in
  // quota-warmup.yaml. row is dimmed while the request is in flight and
  // given an inline saved/failed note afterward (see flashRow) instead of
  // the page-level error banner an explicit Save button used to rely on.
  function setFileAccount(row, name, enabled, timeVal, model) {
    row.classList.add("row-saving");
    var api = getManagementApi();
    var payload = { auth: name };
    if (enabled !== null && enabled !== undefined) { payload.enabled = enabled; }
    if (timeVal) { payload.time = timeVal; }
    if (model) { payload.model = model; }
    var headers = Object.assign({}, api.headers, { "Content-Type": "application/json" });
    fetch(api.base + "set?lang=" + encodeURIComponent(lang), {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload),
      cache: "no-store"
    })
      .then(function (resp) {
        if (resp.status === 401) {
          promptForManagementKey("未授权 (401)");
          throw new Error("unauthorized");
        }
        return resp.json().then(function (body) { return { ok: resp.ok, body: body }; });
      })
      .then(function (res) {
        row.classList.remove("row-saving");
        if (!res.ok) { flashRow(row, false, (res.body && res.body.error) || t("ui_set_failed")); return; }
        flashRow(row, true, res.body && res.body.warning ? res.body.warning : "");
        scheduleReload(1200);
      })
      .catch(function (err) {
        row.classList.remove("row-saving");
        if (err && err.message === "unauthorized") { return; }
        flashRow(row, false, t("ui_set_failed"));
      });
  }

  // setModel calls the /set route to pin (a real model id) or clear
  // (model="auto") one account's (or, for scope="global", every account's)
  // warmup model. el is whatever input/button triggered the save; when it
  // sits inside an accounts-table row, that row gets the same dim/flash
  // treatment setFileAccount uses; the config section's global-model editor
  // has no such row, so it falls back to the page-level error banner (a
  // global change has no single row to annotate anyway).
  function setModel(el, scope, authName, model) {
    var row = el.closest("tr");
    if (row) { row.classList.add("row-saving"); }
    var api = getManagementApi();
    var payload = { scope: scope, model: model || "auto" };
    if (authName) { payload.auth = authName; }
    var headers = Object.assign({}, api.headers, { "Content-Type": "application/json" });
    fetch(api.base + "set?lang=" + encodeURIComponent(lang), {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload),
      cache: "no-store"
    })
      .then(function (resp) {
        if (resp.status === 401) {
          promptForManagementKey("未授权 (401)");
          throw new Error("unauthorized");
        }
        return resp.json().then(function (body) { return { ok: resp.ok, body: body }; });
      })
      .then(function (res) {
        if (row) { row.classList.remove("row-saving"); }
        if (!res.ok) {
          var message = (res.body && res.body.error) || t("ui_set_failed");
          if (row) { flashRow(row, false, message); } else { showError(message); }
          return;
        }
        var warning = res.body && res.body.warning ? res.body.warning : "";
        if (row) { flashRow(row, true, warning); } else { showError(""); }
        scheduleReload(1200);
      })
      .catch(function (err) {
        if (row) { row.classList.remove("row-saving"); }
        if (err && err.message === "unauthorized") { return; }
        if (row) { flashRow(row, false, t("ui_set_failed")); } else { showError(t("ui_set_failed")); }
      });
  }

  // Auto-save on checkbox toggle / text input change (blur): v0.6.0 removed
  // every explicit "保存"/Save button from the accounts table and the
  // config section's global-model editor. A file-mode row's checkbox
  // change fires immediately; a text input (time/model) commits on blur
  // (native "change" semantics) or Enter (see the keydown listener below,
  // since Enter alone does not fire "change" until focus actually leaves
  // the field).
  document.addEventListener("change", function (event) {
    var target = event.target;
    if (target.classList.contains("row-enabled") || target.classList.contains("row-time") || target.classList.contains("row-model")) {
      var row = target.closest("tr[data-name]");
      if (!row) { return; }
      var name = row.getAttribute("data-name");
      var enabledInput = row.querySelector(".row-enabled");
      var timeInput = row.querySelector(".row-time");
      var modelInput = row.querySelector(".row-model");
      var enabled = enabledInput ? enabledInput.checked : null;
      var timeVal = timeInput ? timeInput.value.trim() : null;
      var modelVal = modelInput ? modelInput.value.trim() : "";
      setFileAccount(row, name, enabled, timeVal, modelVal || "auto");
      return;
    }
    if (target.classList.contains("model-input") && target.hasAttribute("data-scope")) {
      var scope = target.getAttribute("data-scope");
      var authName = target.getAttribute("data-auth");
      var value = target.value.trim();
      if (!value) { return; }
      setModel(target, scope, authName, value);
    }
  });

  document.addEventListener("keydown", function (event) {
    var target = event.target;
    var isModelInput = target.classList && target.classList.contains("model-input");

    // While this input's own combo menu is open, arrow keys move the
    // highlighted option, Enter commits whichever option is highlighted
    // (falling back to the plain blur-triggers-autosave behavior below if
    // none is), and Escape just closes the menu without changing anything.
    if (isModelInput && comboActiveInput === target) {
      if (event.key === "ArrowDown") { event.preventDefault(); comboMoveActive(1); return; }
      if (event.key === "ArrowUp") { event.preventDefault(); comboMoveActive(-1); return; }
      if (event.key === "Escape") { event.preventDefault(); closeCombo(); return; }
      if (event.key === "Enter") {
        event.preventDefault();
        if (comboActiveIndex >= 0 && comboItems[comboActiveIndex]) {
          comboSelect(comboItems[comboActiveIndex].value);
        } else {
          closeCombo();
          target.blur();
        }
        return;
      }
    }

    if (event.key !== "Enter") { return; }
    if (target.classList.contains("row-time") || target.classList.contains("row-model") ||
        (isModelInput && target.hasAttribute("data-scope"))) {
      event.preventDefault();
      target.blur();
    }
  });

  // "自动"/Auto text buttons: a file-mode row's only clears the model field
  // (leaving enabled/time untouched); the inline-mode/global one clears via
  // scope. Event delegation: the config/accounts tables are fully
  // re-rendered on every load(), so listeners are attached once here rather
  // than re-bound per row.
  document.addEventListener("click", function (event) {
    var rowAutoBtn = event.target.closest(".row-model-auto");
    if (rowAutoBtn) {
      var row = rowAutoBtn.closest("tr[data-name]");
      if (row) { setFileAccount(row, row.getAttribute("data-name"), null, null, "auto"); }
      return;
    }
    var inlineAutoBtn = event.target.closest(".model-auto-btn");
    if (inlineAutoBtn) {
      setModel(inlineAutoBtn, inlineAutoBtn.getAttribute("data-scope"), inlineAutoBtn.getAttribute("data-auth"), "auto");
    }
  });

  function coveredGlyphHTML(covered) {
    return covered ? '<span class="covered-yes">✓</span>' : '<span class="covered-no">✗</span>';
  }

  function renderRecent(recent) {
    var rows = recent || [];
    var body = rows.map(function (r) {
      return "<tr><td>" + esc(fmtRecentDateTime(r.date, r.time)) + "</td><td class=\"col-name\">" + nameCellHTML(r.auth) + "</td><td>" +
        esc(r.provider || "") + "</td><td>" + esc(r.model || "") + "</td><td>" + coveredGlyphHTML(r.covered) + "</td><td>" +
        esc(r.rounds) + "</td><td>" + (r.status_code ? esc(r.status_code) : dash()) + "</td><td>" +
        (r.warning ? ('<span class="badge badge-failure">' + esc(r.warning) + '</span>') : dash()) + "</td></tr>";
    }).join("");
    document.querySelector("#recentTable tbody").innerHTML = body || ('<tr><td colspan="8" class="empty">' + dash() + '</td></tr>');
  }

  // configYamlLoaded ensures the editor's own GET .../config-yaml fetch only
  // ever fires once automatically (the first time status reports mode ===
  // "file"), not on every 30s load() poll -- otherwise a background refresh
  // could clobber an edit the operator is still typing. Later refreshes only
  // happen from an explicit "重新载入" click or right after a successful
  // "保存" (see saveConfigYAML).
  var configYamlLoaded = false;

  function load() {
    showError("");
    // Any open combo menu is bound to an <input> that renderConfigChips/
    // renderAccounts below are about to replace (config section) or fully
    // re-render (accounts table); closing it first avoids leaving the menu
    // visibly open over a now-detached input, or a keyboard/click handler
    // reacting against a stale comboActiveInput reference.
    closeCombo();
    var api = getManagementApi();
    fetch(api.base + "status?lang=" + encodeURIComponent(lang), { headers: api.headers, cache: "no-store" })
      .then(function (resp) {
        if (resp.status === 401) {
          showError("未授权 (401)：请在 CPA 管理控制台内打开此页面，或提供管理密钥。");
          if (!api.auth) { promptForManagementKey(); }
          throw new Error("unauthorized");
        }
        return resp.json().then(function (body) { return { ok: resp.ok, body: body }; });
      })
      .then(function (res) {
        if (!res.ok) { showError(t("ui_load_failed")); return; }
        setComboModels(res.body.available_models);
        renderConfigChips(res.body.config, res.body.last_tick, res.body.last_tick_error);
        renderConfigAdvanced(res.body.config);
        var isFileMode = res.body.config && res.body.config.mode === "file";
        var configYamlSection = document.getElementById("configYamlSection");
        if (configYamlSection) { configYamlSection.hidden = !isFileMode; }
        if (isFileMode && !configYamlLoaded) {
          configYamlLoaded = true;
          loadConfigYAML();
        }
        renderAccounts(res.body.auths, isFileMode);
        renderRecent(res.body.recent);
      })
      .catch(function (err) {
        if (err && err.message === "unauthorized") { return; }
        showError(t("ui_load_failed"));
      });
  }

  // configYamlMtime tracks the mtime of whatever quota-warmup.yaml content
  // is currently sitting in the editor, so saveConfigYAML's optimistic-
  // concurrency check has something to send. It is refreshed on every
  // successful load or save; it is deliberately never refreshed by the 30s
  // load()/setInterval poll below, so an in-progress edit is never clobbered
  // by a background refresh -- only an explicit "重新载入" click or a
  // successful "保存" ever touches the editor's contents.
  var configYamlMtime = "";

  function loadConfigYAML() {
    var errBox = document.getElementById("configYamlError");
    var metaBox = document.getElementById("configYamlMeta");
    var editor = document.getElementById("configYamlEditor");
    errBox.hidden = true;
    errBox.textContent = "";
    var api = getManagementApi();
    fetch(api.base + "config-yaml?lang=" + encodeURIComponent(lang), { headers: api.headers, cache: "no-store" })
      .then(function (resp) {
        if (resp.status === 401) {
          promptForManagementKey("未授权 (401)");
          throw new Error("unauthorized");
        }
        return resp.json().then(function (body) { return { ok: resp.ok, body: body }; });
      })
      .then(function (res) {
        if (!res.ok) {
          errBox.hidden = false;
          errBox.textContent = (res.body && res.body.error) || t("ui_config_yaml_load_failed");
          return;
        }
        editor.value = res.body.content || "";
        configYamlMtime = res.body.mtime || "";
        var meta = res.body.path || "";
        if (res.body.error) { meta += "（" + res.body.error + "）"; }
        metaBox.textContent = meta;
      })
      .catch(function (err) {
        if (err && err.message === "unauthorized") { return; }
        errBox.hidden = false;
        errBox.textContent = t("ui_config_yaml_load_failed");
      });
  }

  // toBase64Url mirrors the server's base64.RawURLEncoding exactly: standard
  // base64 (via btoa(unescape(encodeURIComponent(s))), which round-trips any
  // Unicode text through a byte-for-byte "binary string" the way btoa
  // requires), then the RFC 4648 §5 substitution (+ -> -, / -> _) and
  // stripping the "=" padding raw base64url never carries. Kept as its own
  // function so an encoding failure (browsers without atob/btoa Unicode
  // support) can be reported distinctly from a network/validation failure.
  function toBase64Url(s) {
    var std = btoa(unescape(encodeURIComponent(s)));
    return std.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function saveConfigYAML() {
    var errBox = document.getElementById("configYamlError");
    var metaBox = document.getElementById("configYamlMeta");
    var editor = document.getElementById("configYamlEditor");
    errBox.hidden = true;
    errBox.textContent = "";
    var encoded;
    try {
      encoded = toBase64Url(editor.value);
    } catch (err) {
      errBox.hidden = false;
      errBox.textContent = t("ui_config_yaml_encode_error");
      return;
    }
    var api = getManagementApi();
    var payload = {
      content: encoded,
      mtime: configYamlMtime
    };
    var headers = Object.assign({}, api.headers, { "Content-Type": "application/json" });
    fetch(api.base + "config-yaml/save?lang=" + encodeURIComponent(lang), {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload),
      cache: "no-store"
    })
      .then(function (resp) {
        if (resp.status === 401) {
          promptForManagementKey("未授权 (401)");
          throw new Error("unauthorized");
        }
        return resp.json().then(function (body) { return { ok: resp.ok, body: body }; });
      })
      .then(function (res) {
        if (!res.ok) {
          var body = res.body || {};
          errBox.hidden = false;
          if (typeof body.line === "number" && typeof body.column === "number") {
            errBox.textContent = "第 " + body.line + " 行第 " + body.column + " 列：" + body.error;
          } else {
            errBox.textContent = body.error || t("ui_config_yaml_load_failed");
          }
          return;
        }
        configYamlMtime = res.body.mtime || "";
        metaBox.textContent = (res.body.path || "") + "  ·  " + (res.body.message || "");
        load();
      })
      .catch(function (err) {
        if (err && err.message === "unauthorized") { return; }
        errBox.hidden = false;
        errBox.textContent = t("ui_config_yaml_load_failed");
      });
  }

  document.getElementById("configYamlReloadBtn").addEventListener("click", loadConfigYAML);
  document.getElementById("configYamlSaveBtn").addEventListener("click", saveConfigYAML);
  // Tab inserts two spaces instead of moving focus out of the textarea --
  // an editor for a 2-space-indented YAML file should not fight the
  // browser's own default tab-to-next-control behavior.
  document.getElementById("configYamlEditor").addEventListener("keydown", function (event) {
    if (event.key !== "Tab") { return; }
    event.preventDefault();
    var el = event.target;
    var start = el.selectionStart, end = el.selectionEnd;
    el.value = el.value.slice(0, start) + "  " + el.value.slice(end);
    el.selectionStart = el.selectionEnd = start + 2;
  });

  document.getElementById("refreshBtn").addEventListener("click", load);
  document.getElementById("runBtn").addEventListener("click", function () {
    var glob = document.getElementById("authFilter").value.trim();
    var api = getManagementApi();
    var payload = { auth: glob };
    var headers = Object.assign({}, api.headers, { "Content-Type": "application/json" });
    fetch(api.base + "run?lang=" + encodeURIComponent(lang), {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload),
      cache: "no-store"
    }).then(function (resp) {
      if (resp.status === 401) {
        promptForManagementKey("未授权 (401)");
        throw new Error("unauthorized");
      }
      return resp.json();
    }).then(function (body) {
      var box = document.getElementById("runResult");
      box.hidden = false;
      var parts = [];
      parts.push("<p>" + esc(t("ui_run_attempted")) + ": " + (((body.attempted || []).map(esc).join(", ")) || dash()) + "</p>");
      var skippedKeys = body.skipped ? Object.keys(body.skipped) : [];
      if (skippedKeys.length) {
        parts.push("<p>" + esc(t("ui_run_skipped")) + ": " + skippedKeys.map(function (k) {
          return esc(k) + " (" + esc(body.skipped[k]) + ")";
        }).join("; ") + "</p>");
      }
      var outcomeKeys = body.outcomes ? Object.keys(body.outcomes) : [];
      if (outcomeKeys.length) {
        var rows = outcomeKeys.map(function (k) {
          var o = body.outcomes[k] || {};
          var bits = [boolText(o.Covered), t("ui_col_rounds") + "=" + esc(o.Rounds)];
          if (o.StatusCode) { bits.push(t("ui_col_status_code") + "=" + esc(o.StatusCode)); }
          if (o.Warning) { bits.push('<span class="warn-text">' + esc(o.Warning) + '</span>'); }
          return "<tr><th>" + esc(k) + "</th><td>" + bits.join(" · ") + "</td></tr>";
        }).join("");
        parts.push('<div class="scroll"><table class="kv"><tbody>' + rows + "</tbody></table></div>");
      }
      document.getElementById("runResultBody").innerHTML = parts.join("");
      load();
    }).catch(function (err) {
      if (err && err.message === "unauthorized") { return; }
      showError(t("ui_load_failed"));
    });
  });

  load();
  setInterval(load, 30000);
})();
</script>
</body>
</html>
`
