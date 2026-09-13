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
    font: inherit; font-size: 13px; padding: 7px 14px; border-radius: 6px; cursor: pointer;
  }
  .ghost-btn:hover { border-color: var(--border-strong); }
  .ghost-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  input[type="text"] {
    font: inherit; font-size: 13px; padding: 7px 10px; border-radius: 6px;
    border: 1px solid var(--border); background: var(--panel); color: var(--text); min-width: 220px;
  }
  section.block { background: var(--panel); border: 1px solid var(--border); border-radius: var(--radius); padding: 18px; margin-bottom: 16px; box-shadow: var(--shadow); }
  .block-head { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; justify-content: space-between; margin-bottom: 14px; }
  .block-head h2 { margin: 0; font-size: 15px; font-weight: 620; }
  .block-head .controls { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: top; }
  th { font-size: 11px; letter-spacing: .05em; text-transform: uppercase; color: var(--muted); font-weight: 600; white-space: nowrap; }
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
        <th data-i18n="ui_col_name">{{ui_col_name}}</th><th data-i18n="ui_col_provider">{{ui_col_provider}}</th><th data-i18n="ui_col_model">{{ui_col_model}}</th>
        <th data-i18n="ui_col_times">{{ui_col_times}}</th><th data-i18n="ui_col_next_trigger">{{ui_col_next_trigger}}</th><th data-i18n="ui_col_status">{{ui_col_status}}</th>
      </tr></thead>
      <tbody><tr><td colspan="6" class="empty" data-i18n="ui_loading">{{ui_loading}}</td></tr></tbody>
    </table></div>
    <div id="runResult" hidden>
      <h3 data-i18n="ui_run_result_title">{{ui_run_result_title}}</h3>
      <div id="runResultBody"></div>
    </div>
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

  function renderConfig(cfg) {
    cfg = cfg || {};
    var rows = [
      [t("ui_label_enabled"), boolText(cfg.enabled)],
      [t("ui_label_timezone"), esc(cfg.timezone) || dash()],
      [t("ui_label_base_url"), esc(cfg.base_url) || dash()],
      [t("ui_label_message"), esc(cfg.message) || dash()],
      [t("ui_label_max_tokens"), esc(cfg.max_tokens)],
      [t("ui_label_max_rounds"), esc(cfg.max_rounds)],
      [t("ui_label_catch_up_minutes"), esc(cfg.catch_up_minutes)]
    ];
    document.getElementById("configTable").innerHTML = rows.map(function (r) {
      return "<tr><th>" + esc(r[0]) + "</th><td>" + r[1] + "</td></tr>";
    }).join("");
  }

  function renderAccounts(auths) {
    var rows = auths || [];
    var body = rows.map(function (a) {
      var status = a.skipped ? ('<span class="warn-text">' + esc(a.skipped) + '</span>') : boolText(a.enabled);
      return "<tr><td>" + esc(a.name) + "</td><td>" + esc(a.provider || "") + "</td><td>" +
        (esc(a.model) || dash()) + "</td><td>" + esc((a.times || []).join(", ")) + "</td><td>" +
        (esc(a.next_trigger) || dash()) + "</td><td>" + status + "</td></tr>";
    }).join("");
    document.querySelector("#accountsTable tbody").innerHTML = body || ('<tr><td colspan="6" class="empty">' + dash() + '</td></tr>');
  }

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

  function load() {
    showError("");
    fetch(base + "status?lang=" + encodeURIComponent(lang), { cache: "no-store" })
      .then(function (resp) { return resp.json().then(function (body) { return { ok: resp.ok, body: body }; }); })
      .then(function (res) {
        if (!res.ok) { showError(t("ui_load_failed")); return; }
        renderConfig(res.body.config);
        renderAccounts(res.body.auths);
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
