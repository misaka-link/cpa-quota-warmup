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
// which sends its OS/CLI default Accept-Language while an operator's
// localStorage says otherwise) would see the server's first guess baked
// into static text forever, since only the *dynamically rendered* rows
// (config table, accounts, recent) were ever re-translated client-side.
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
// no external requests except the plugin's own GET .../status and
// GET .../run (computed client-side, relative to this page's own URL, so it
// keeps working under any reverse-proxy prefix).
//
// The theme-following script block is adapted verbatim from
// cpa-usage-panel/panel.html (same STORAGE_KEY, same zustand-persist-or-bare
// value parsing) so this page matches whichever theme the Management Center
// is set to. The language-following script mirrors the equivalent real
// logic read out of a live /var/lib/cli-proxy-api/static/management.html
// build (localStorage key "cli-proxy-language", zustand-persist-or-bare
// value, region-based zh-TW detection, navigator.language fallback).
const panelHTMLTemplate = `<!doctype html>
<html lang="{{HTML_LANG}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title data-i18n="ui_page_title">{{ui_page_title}}</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ccircle cx='8' cy='8' r='6' fill='none' stroke='%23d97706' stroke-width='2'/%3E%3Cpath d='M8 4v4l3 2' fill='none' stroke='%23d97706' stroke-width='2' stroke-linecap='round'/%3E%3C/svg%3E">
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
  :root {
    color-scheme: light;
    --bg: #ffffff; --panel: #ffffff; --panel-2: #f6f6f6;
    --border: #e5e5e5; --border-strong: #d9d9d9;
    --text: #2d2a26; --muted: #6d6760;
    --accent: #d97706; --green: #0f9d6f; --red: #c65746;
    --radius: 8px; --shadow: 0 1px 2px 0 rgba(0, 0, 0, .08);
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="white"]) {
      color-scheme: dark;
      --bg: #151412; --panel: #1d1b18; --panel-2: #262320;
      --border: #3a3530; --border-strong: #4a453f;
      --text: #f6f4f1; --muted: #9c958d;
      --accent: #f59e0b; --green: #34d399; --red: #cf4b3a;
      --shadow: 0 1px 3px 0 rgba(0, 0, 0, .3);
    }
  }
  :root[data-theme="dark"] {
    color-scheme: dark;
    --bg: #151412; --panel: #1d1b18; --panel-2: #262320;
    --border: #3a3530; --border-strong: #4a453f;
    --text: #f6f4f1; --muted: #9c958d;
    --accent: #f59e0b; --green: #34d399; --red: #cf4b3a;
    --shadow: 0 1px 3px 0 rgba(0, 0, 0, .3);
  }
  :root[data-theme="white"] {
    color-scheme: light;
    --bg: #ffffff; --panel: #ffffff; --panel-2: #f6f6f6;
    --border: #e5e5e5; --border-strong: #d9d9d9;
    --text: #2d2a26; --muted: #6d6760;
    --accent: #d97706; --green: #0f9d6f; --red: #c65746;
    --shadow: 0 1px 2px 0 rgba(0, 0, 0, .08);
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--text);
    font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC",
          "PingFang SC", "Microsoft YaHei", Roboto, Helvetica, Arial, sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  .wrap { max-width: 1120px; margin: 0 auto; padding: 24px 20px 56px; }
  header.top { display: flex; flex-wrap: wrap; gap: 16px; align-items: flex-end; justify-content: space-between; margin-bottom: 20px; }
  .title h1 { margin: 0; font-size: 21px; letter-spacing: -.01em; }
  .title p { margin: 4px 0 0; color: var(--muted); font-size: 13px; }
  .ghost-btn {
    appearance: none; border: 1px solid var(--border); background: var(--panel); color: var(--text);
    font: inherit; font-size: 13px; padding: 0 12px; height: 32px; border-radius: 6px; cursor: pointer;
    display: inline-flex; align-items: center; box-sizing: border-box;
  }
  .ghost-btn:hover { border-color: var(--border-strong); }
  .ghost-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  input[type="text"] {
    font: inherit; font-size: 13px; padding: 0 10px; height: 32px; border-radius: 6px;
    border: 1px solid var(--border); background: var(--panel); color: var(--text); min-width: 220px;
    box-sizing: border-box;
  }
  input[type="checkbox"] { width: 16px; height: 16px; vertical-align: middle; }
  .model-edit { display: inline-flex; gap: 6px; align-items: center; flex-wrap: nowrap; }
  input.model-input { width: 14em; min-width: 0; }
  input.row-time { width: 12em; min-width: 0; }
  .row-actions { display: inline-flex; gap: 6px; align-items: center; white-space: nowrap; }
  section.block { background: var(--panel); border: 1px solid var(--border); border-radius: var(--radius); padding: 18px; margin-bottom: 16px; box-shadow: var(--shadow); }
  .block-head { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; justify-content: space-between; margin-bottom: 14px; }
  .block-head h2 { margin: 0; font-size: 15px; font-weight: 620; }
  .block-head .controls { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
  th { font-size: 11px; letter-spacing: .05em; text-transform: uppercase; color: var(--muted); font-weight: 600; white-space: nowrap; }
  #accountsTable td { vertical-align: middle; }
  #accountsTable th.col-status, #accountsTable td.col-status { min-width: 14em; white-space: normal; }
  tbody tr:last-child td { border-bottom: 0; }
  .scroll { overflow-x: auto; }
  table.kv td:first-child, table.kv th:first-child { width: 220px; color: var(--muted); font-weight: 600; white-space: nowrap; }
  table.kv tr:last-child td, table.kv tr:last-child th { border-bottom: 0; }
  .yes { color: var(--green); }
  .no { color: var(--muted); }
  .warn-text { color: var(--red); }
  .hint { color: var(--muted); font-size: 12px; margin-top: 10px; }
  .empty { color: var(--muted); padding: 20px 8px; text-align: center; }
  .err { background: color-mix(in srgb, var(--red) 12%, transparent); border: 1px solid var(--red); color: var(--red);
         border-radius: var(--radius); padding: 12px 14px; margin-bottom: 16px; font-size: 13px; }
  footer.meta { color: var(--muted); font-size: 12px; }
  #runResult { margin-top: 14px; border-top: 1px solid var(--border); padding-top: 14px; }
  #runResult h3 { margin: 0 0 8px; font-size: 13px; font-weight: 620; }
  .config-yaml-editor {
    width: 100%; min-height: 360px; box-sizing: border-box; resize: vertical;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 12.5px; line-height: 1.5; tab-size: 2;
    padding: 10px 12px; border-radius: 6px; border: 1px solid var(--border);
    background: var(--panel-2); color: var(--text);
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
    <button type="button" class="ghost-btn" id="refreshBtn" data-i18n="ui_refresh">{{ui_refresh}}</button>
  </header>

  <div class="err" id="error" hidden></div>

  <section class="block">
    <div class="block-head"><h2 data-i18n="ui_section_config">{{ui_section_config}}</h2></div>
    <div class="scroll"><table class="kv"><tbody id="configTable"></tbody></table></div>
    <p class="hint" id="lastTick"></p>
  </section>

  <section class="block">
    <div class="block-head">
      <h2 data-i18n="ui_section_accounts">{{ui_section_accounts}}</h2>
      <div class="controls">
        <input type="text" id="authFilter" placeholder="{{ui_auth_filter_placeholder}}" data-i18n-placeholder="ui_auth_filter_placeholder">
        <button type="button" class="ghost-btn" id="runBtn" data-i18n="ui_warm_up_now">{{ui_warm_up_now}}</button>
      </div>
    </div>
    <div class="scroll"><table id="accountsTable">
      <thead><tr>
        <th data-i18n="ui_col_name">{{ui_col_name}}</th><th data-i18n="ui_col_provider">{{ui_col_provider}}</th><th data-i18n="ui_label_enabled">{{ui_label_enabled}}</th><th data-i18n="ui_col_model">{{ui_col_model}}</th>
        <th data-i18n="ui_col_times">{{ui_col_times}}</th><th data-i18n="ui_col_next_trigger">{{ui_col_next_trigger}}</th><th class="col-status" data-i18n="ui_col_status">{{ui_col_status}}</th><th data-i18n="ui_col_actions">{{ui_col_actions}}</th>
      </tr></thead>
      <tbody><tr><td colspan="8" class="empty" data-i18n="ui_loading">{{ui_loading}}</td></tr></tbody>
    </table></div>
    <datalist id="model-options"></datalist>
    <div id="runResult" hidden>
      <h3 data-i18n="ui_run_result_title">{{ui_run_result_title}}</h3>
      <div id="runResultBody"></div>
    </div>
  </section>

  <section class="block" id="configYamlSection" hidden>
    <div class="block-head">
      <h2 data-i18n="ui_section_config_yaml">{{ui_section_config_yaml}}</h2>
      <div class="controls">
        <button type="button" class="ghost-btn" id="configYamlReloadBtn" data-i18n="ui_config_yaml_reload">{{ui_config_yaml_reload}}</button>
        <button type="button" class="ghost-btn" id="configYamlSaveBtn" data-i18n="ui_config_yaml_save">{{ui_config_yaml_save}}</button>
      </div>
    </div>
    <div class="err" id="configYamlError" hidden></div>
    <textarea id="configYamlEditor" class="config-yaml-editor" spellcheck="false"></textarea>
    <p class="hint" id="configYamlMeta"></p>
  </section>

  <section class="block">
    <div class="block-head"><h2 data-i18n="ui_section_recent">{{ui_section_recent}}</h2></div>
    <div class="scroll"><table id="recentTable">
      <thead><tr>
        <th data-i18n="ui_col_date">{{ui_col_date}}</th><th data-i18n="ui_col_time">{{ui_col_time}}</th><th data-i18n="ui_col_auth">{{ui_col_auth}}</th>
        <th data-i18n="ui_col_provider">{{ui_col_provider}}</th><th data-i18n="ui_col_model">{{ui_col_model}}</th><th data-i18n="ui_col_covered">{{ui_col_covered}}</th>
        <th data-i18n="ui_col_rounds">{{ui_col_rounds}}</th><th data-i18n="ui_col_status_code">{{ui_col_status_code}}</th><th data-i18n="ui_col_warning">{{ui_col_warning}}</th>
      </tr></thead>
      <tbody><tr><td colspan="9" class="empty" data-i18n="ui_loading">{{ui_loading}}</td></tr></tbody>
    </table></div>
  </section>

  <footer class="meta" data-i18n="ui_footer_note">{{ui_footer_note}}</footer>
</div>
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
  // Anything rendered later (config/accounts/recent tables, the run result)
  // already goes through t() directly and needs no such pass.
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

  var base = window.location.pathname.replace(/[^/]*$/, "");

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s === null || s === undefined ? "" : String(s);
    return d.innerHTML;
  }
  function boolText(b) { return b ? ('<span class="yes">' + esc(t("ui_yes")) + '</span>') : ('<span class="no">' + esc(t("ui_no")) + '</span>'); }
  function dash() { return esc(t("ui_none")); }

  function showError(msg) {
    var box = document.getElementById("error");
    if (msg) { box.textContent = msg; box.hidden = false; } else { box.hidden = true; }
  }

  // modelEditorHTML builds the "<input list=model-options> + Save + Auto"
  // control used both for the config table's global-model row and for each
  // account row's model cell. currentValue "auto" (or empty) leaves the
  // input blank -- the placeholder communicates that auto-selection is in
  // effect -- so a beginner can never accidentally type the literal word
  // "auto" into a real model name field.
  function modelEditorHTML(scope, authName, currentValue) {
    var attrs = 'data-scope="' + esc(scope) + '" data-auth="' + esc(authName || "") + '"';
    var value = (!currentValue || currentValue === "auto") ? "" : currentValue;
    return '<span class="model-edit">' +
      '<input type="text" list="model-options" class="model-input" ' + attrs +
      ' value="' + esc(value) + '" placeholder="' + esc(t("ui_model_placeholder")) + '">' +
      '<button type="button" class="ghost-btn model-save-btn" ' + attrs + '>' + esc(t("ui_model_save")) + '</button>' +
      '<button type="button" class="ghost-btn model-auto-btn" ' + attrs + '>' + esc(t("ui_model_auto")) + '</button>' +
      '</span>';
  }

  // modelInputOnlyHTML is modelEditorHTML without the inline Save/Auto
  // buttons, used in the accounts table's own "Model" column now that those
  // buttons live in a shared "Actions" column instead (modelActionsHTML).
  // modelSource (a raw source label like "auto"/"account"/"panel") is
  // surfaced as the input's title tooltip rather than a dedicated column.
  function modelInputOnlyHTML(scope, authName, currentValue, modelSource) {
    var attrs = 'data-scope="' + esc(scope) + '" data-auth="' + esc(authName || "") + '"';
    var value = (!currentValue || currentValue === "auto") ? "" : currentValue;
    var title = modelSource ? ' title="' + esc(modelSource) + '"' : "";
    return '<input type="text" list="model-options" class="model-input" ' + attrs +
      ' value="' + esc(value) + '" placeholder="' + esc(t("ui_model_placeholder")) + '"' + title + '>';
  }

  // modelActionsHTML renders the shared "Actions" column's Save/Auto button
  // pair for inline-mode rows (file-mode rows build their own actions cell
  // directly in fileRowHTML, since a file-mode "Save" also covers the
  // enabled/time inputs, not just the model).
  function modelActionsHTML(scope, authName) {
    var attrs = 'data-scope="' + esc(scope) + '" data-auth="' + esc(authName || "") + '"';
    return '<span class="row-actions">' +
      '<button type="button" class="ghost-btn model-save-btn" ' + attrs + '>' + esc(t("ui_model_save")) + '</button>' +
      '<button type="button" class="ghost-btn model-auto-btn" ' + attrs + '>' + esc(t("ui_model_auto")) + '</button>' +
      '</span>';
  }

  // populateModelOptions fills the shared <datalist> from status'
  // available_models, grouped by owned_by so the dropdown at least hints at
  // which provider each id belongs to. A model not in this list can still
  // be typed freely -- the datalist is a convenience, not a whitelist; the
  // server itself validates against the live list when "保存"/Save is
  // clicked.
  function populateModelOptions(models) {
    var list = models || [];
    var groups = {}, order = [];
    list.forEach(function (m) {
      var g = m.owned_by || "";
      if (!groups[g]) { groups[g] = []; order.push(g); }
      groups[g].push(m.id);
    });
    var html = order.map(function (g) {
      var options = groups[g].map(function (id) { return '<option value="' + esc(id) + '">'; }).join("");
      return g ? ('<optgroup label="' + esc(g) + '">' + options + '</optgroup>') : options;
    }).join("");
    var el = document.getElementById("model-options");
    if (el) { el.innerHTML = html; }
  }

  function renderConfig(cfg) {
    cfg = cfg || {};
    var isFile = cfg.mode === "file";
    var isV3Inline = cfg.mode === "inline" && !cfg.legacy_mode;
    var timezoneLine = esc(cfg.timezone) || dash();
    if (cfg.timezone_auto) {
      timezoneLine += " (" + esc(t("ui_timezone_auto")) + ")";
    } else if (!cfg.legacy_mode) {
      timezoneLine += " (" + esc(t("ui_timezone_manual")) + ")";
    }
    var rows = [
      [t("ui_label_enabled"), boolText(cfg.enabled)],
      [t("ui_label_mode"), esc(isFile ? t("ui_mode_file") : t("ui_mode_inline"))]
    ];
    if (isFile) {
      rows.push([t("ui_label_config_file"), esc(cfg.config_file) || dash()]);
      if (cfg.config_file_error) {
        rows.push([t("ui_label_parse_error"), '<span class="warn-text">' + esc(cfg.config_file_error) + '</span>']);
      }
    } else {
      rows.push([t("ui_label_time"), esc((cfg.time || []).join(", ")) || dash()]);
      if (isV3Inline) {
        var accountsValue = (cfg.accounts && cfg.accounts.length)
          ? esc(cfg.accounts.join(", "))
          : ('<span class="warn-text">' + esc(t("ui_no_accounts_hint")) + '</span>');
        rows.push([t("ui_label_accounts"), accountsValue]);
      }
    }
    rows = rows.concat([
      [t("ui_label_timezone"), timezoneLine],
      [t("ui_label_language"), esc(cfg.language) || dash()],
      [t("ui_label_base_url"), esc(cfg.base_url) || dash()],
      [t("ui_label_message"), esc(cfg.message) || dash()],
      [t("ui_label_max_tokens"), esc(cfg.max_tokens)],
      [t("ui_label_max_rounds"), esc(cfg.max_rounds)],
      [t("ui_label_catch_up_minutes"), esc(cfg.catch_up_minutes)]
    ]);
    var html = rows.map(function (r) {
      return "<tr><th>" + esc(r[0]) + "</th><td>" + r[1] + "</td></tr>";
    }).join("");
    if (isV3Inline) {
      html += "<tr><th>" + esc(t("ui_label_global_model")) + "</th><td>" + modelEditorHTML("global", "", cfg.model) + "</td></tr>";
    }
    document.getElementById("configTable").innerHTML = html;
  }

  // statusCellHTML renders the "Status" column for either row type: real
  // problems (a.warning -- an invalid time expression or an unavailable
  // model) stay red, but a plain "not enabled" skip reason is shown muted
  // gray -- it is the normal, most common state for a fresh install with
  // many accounts, not an error, and should not read like one.
  function statusCellHTML(a) {
    var cell = a.skipped ? ('<span class="no">' + esc(a.skipped) + '</span>') : boolText(a.enabled);
    if (a.warning) { cell += '<br><span class="warn-text">' + esc(a.warning) + '</span>'; }
    return cell;
  }

  // fileRowHTML renders one v0.4.0 file-mode account row: an editable
  // enabled checkbox, model input (with an "Auto"/自动 shortcut folded into
  // the shared Actions column), and time input. The Actions column's "Save"
  // button reads all three current input values and writes them back into
  // quota-warmup.yaml at once; "Auto" only clears the model field.
  function fileRowHTML(a) {
    var enabledCell = '<input type="checkbox" class="row-enabled"' + (a.enabled ? " checked" : "") + '>';
    var modelValue = (!a.model || a.model === "auto") ? "" : a.model;
    var modelCell = '<input type="text" list="model-options" class="model-input row-model" value="' + esc(modelValue) +
      '" placeholder="' + esc(t("ui_model_placeholder")) + '"' + (a.model_source ? ' title="' + esc(a.model_source) + '"' : "") + '>';
    var timeCell = '<input type="text" class="row-time" value="' + esc((a.times || []).join(", ")) + '" placeholder="' + esc(t("ui_time_placeholder")) + '">';
    var actionsCell = '<span class="row-actions">' +
      '<button type="button" class="ghost-btn row-save">' + esc(t("ui_model_save")) + '</button>' +
      '<button type="button" class="ghost-btn row-model-auto">' + esc(t("ui_model_auto")) + '</button>' +
      '</span>';
    return "<tr data-name=\"" + esc(a.name) + "\"><td>" + esc(a.name) + "</td><td>" + esc(a.provider || "") + "</td><td>" +
      enabledCell + "</td><td>" + modelCell + "</td><td>" +
      timeCell + "</td><td>" + (esc(a.next_trigger) || dash()) + "</td><td class=\"col-status\">" + statusCellHTML(a) + "</td><td>" + actionsCell + "</td></tr>";
  }

  // inlineRowHTML renders one v0.3.0-style (inline-mode) account row: the
  // enabled/time columns are read-only display, and only the model has the
  // panel/account/global/provider/auto override editor (input in the Model
  // column, Save/Auto buttons in the shared Actions column).
  function inlineRowHTML(a) {
    var modelCell = a.enabled ? modelInputOnlyHTML("auth", a.name, a.model, a.model_source) : (esc(a.model) || dash());
    var actionsCell = a.enabled ? modelActionsHTML("auth", a.name) : "";
    return "<tr><td>" + esc(a.name) + "</td><td>" + esc(a.provider || "") + "</td><td>" + dash() + "</td><td>" +
      modelCell + "</td><td>" + esc((a.times || []).join(", ")) + "</td><td>" +
      (esc(a.next_trigger) || dash()) + "</td><td class=\"col-status\">" + statusCellHTML(a) + "</td><td>" + actionsCell + "</td></tr>";
  }

  function renderAccounts(auths, isFile) {
    var rows = auths || [];
    var body = rows.map(isFile ? fileRowHTML : inlineRowHTML).join("");
    document.querySelector("#accountsTable tbody").innerHTML = body || ('<tr><td colspan="8" class="empty">' + dash() + '</td></tr>');
  }

  // setFileAccount calls the v0.4.0 file-mode /set route: only the
  // parameters actually passed (non-null) are written, leaving the others
  // untouched in quota-warmup.yaml.
  function setFileAccount(name, enabled, timeVal, model) {
    var url = base + "set?lang=" + encodeURIComponent(lang) + "&auth=" + encodeURIComponent(name);
    if (enabled !== null && enabled !== undefined) { url += "&enabled=" + (enabled ? "true" : "false"); }
    if (timeVal) { url += "&time=" + encodeURIComponent(timeVal); }
    if (model) { url += "&model=" + encodeURIComponent(model); }
    fetch(url, { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
      .then(function (res) {
        if (!res.ok) { showError((res.body && res.body.error) || t("ui_set_failed")); return; }
        showError(res.body && res.body.warning ? res.body.warning : "");
        load();
      })
      .catch(function () { showError(t("ui_set_failed")); });
  }

  // setModel calls the /set route to pin (a real model id) or clear
  // (model="auto") one account's (or, for scope="global", every account's)
  // warmup model, then reloads so the change is reflected immediately.
  function setModel(scope, authName, model) {
    var url = base + "set?lang=" + encodeURIComponent(lang) + "&scope=" + encodeURIComponent(scope) + "&model=" + encodeURIComponent(model || "auto");
    if (authName) { url += "&auth=" + encodeURIComponent(authName); }
    fetch(url, { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
      .then(function (res) {
        if (!res.ok) {
          showError((res.body && res.body.error) || t("ui_set_failed"));
          return;
        }
        showError(res.body && res.body.warning ? res.body.warning : "");
        load();
      })
      .catch(function () { showError(t("ui_set_failed")); });
  }

  // Event delegation: the config/accounts tables are fully re-rendered on
  // every load(), so listeners are attached once here rather than re-bound
  // per row.
  document.addEventListener("click", function (event) {
    var saveBtn = event.target.closest(".model-save-btn");
    var autoBtn = event.target.closest(".model-auto-btn");
    var btn = saveBtn || autoBtn;
    if (!btn) { return; }
    var scope = btn.getAttribute("data-scope");
    var authName = btn.getAttribute("data-auth");
    if (autoBtn) {
      setModel(scope, authName, "auto");
      return;
    }
    // The kv-table's global-model row still wraps its input+buttons in one
    // ".model-edit" span; the accounts table's per-account row now keeps
    // the model input and the Save/Auto buttons in separate <td> cells of
    // the same <tr> (see modelInputOnlyHTML/modelActionsHTML), so fall back
    // to searching the whole row when there is no ".model-edit" ancestor.
    var wrapper = btn.closest(".model-edit") || btn.closest("tr");
    var input = wrapper ? wrapper.querySelector(".model-input") : null;
    var model = input ? input.value.trim() : "";
    if (!model) {
      showError(t("ui_set_failed"));
      return;
    }
    setModel(scope, authName, model);
  });

  // Event delegation for v0.4.0 file-mode account rows (fileRowHTML): one
  // "Save" button per row commits the row's current enabled/time/model
  // inputs together; "自动"/Auto next to the model field clears only the
  // model (leaving enabled/time untouched).
  document.addEventListener("click", function (event) {
    var rowSaveBtn = event.target.closest(".row-save");
    var rowAutoBtn = event.target.closest(".row-model-auto");
    var btn = rowSaveBtn || rowAutoBtn;
    if (!btn) { return; }
    var row = btn.closest("tr[data-name]");
    if (!row) { return; }
    var name = row.getAttribute("data-name");
    if (rowAutoBtn) {
      setFileAccount(name, null, null, "auto");
      return;
    }
    var enabledInput = row.querySelector(".row-enabled");
    var timeInput = row.querySelector(".row-time");
    var modelInput = row.querySelector(".row-model");
    var enabled = enabledInput ? enabledInput.checked : null;
    var timeVal = timeInput ? timeInput.value.trim() : null;
    var modelVal = modelInput ? modelInput.value.trim() : "";
    setFileAccount(name, enabled, timeVal, modelVal || "auto");
  });

  function renderRecent(recent) {
    var rows = recent || [];
    var body = rows.map(function (r) {
      return "<tr><td>" + esc(r.date) + "</td><td>" + esc(r.time) + "</td><td>" + esc(r.auth) + "</td><td>" +
        esc(r.provider || "") + "</td><td>" + esc(r.model || "") + "</td><td>" + boolText(r.covered) + "</td><td>" +
        esc(r.rounds) + "</td><td>" + (r.status_code ? esc(r.status_code) : dash()) + "</td><td>" +
        (r.warning ? ('<span class="warn-text">' + esc(r.warning) + '</span>') : dash()) + "</td></tr>";
    }).join("");
    document.querySelector("#recentTable tbody").innerHTML = body || ('<tr><td colspan="9" class="empty">' + dash() + '</td></tr>');
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
    fetch(base + "status?lang=" + encodeURIComponent(lang), { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
      .then(function (res) {
        if (!res.ok) { showError(t("ui_load_failed")); return; }
        populateModelOptions(res.body.available_models);
        renderConfig(res.body.config);
        var isFileMode = res.body.config && res.body.config.mode === "file";
        var configYamlSection = document.getElementById("configYamlSection");
        if (configYamlSection) { configYamlSection.hidden = !isFileMode; }
        if (isFileMode && !configYamlLoaded) {
          configYamlLoaded = true;
          loadConfigYAML();
        }
        renderAccounts(res.body.auths, isFileMode);
        renderRecent(res.body.recent);
        var tick = res.body.last_tick ? res.body.last_tick : dash();
        var line = t("ui_label_last_tick") + ": " + esc(tick);
        if (res.body.last_tick_error) {
          line += " — " + t("ui_label_last_tick_error") + ": " + esc(res.body.last_tick_error);
        }
        document.getElementById("lastTick").innerHTML = line;
      })
      .catch(function () { showError(t("ui_load_failed")); });
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
    fetch(base + "config-yaml?lang=" + encodeURIComponent(lang), { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
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
      .catch(function () {
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
    var url = base + "config-yaml/save?lang=" + encodeURIComponent(lang) +
      "&mtime=" + encodeURIComponent(configYamlMtime) + "&content=" + encoded;
    fetch(url, { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
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
      .catch(function () {
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
    var url = base + "run?lang=" + encodeURIComponent(lang) + (glob ? "&auth=" + encodeURIComponent(glob) : "");
    fetch(url, { cache: "no-store" }).then(function (resp) { return resp.json(); }).then(function (body) {
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
    }).catch(function () { showError(t("ui_load_failed")); });
  });

  load();
  setInterval(load, 30000);
})();
</script>
</body>
</html>
`
