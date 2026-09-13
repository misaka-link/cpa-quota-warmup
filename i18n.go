package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// lang is one of the four languages CPA's own management console supports.
// Values match the exact strings the console persists under the
// "cli-proxy-language" localStorage key (verified against a live
// /var/lib/cli-proxy-api/static/management.html build): i18next resources
// are keyed "zh-CN", "zh-TW", "en", "ru".
type lang string

const (
	langZhCN lang = "zh-CN"
	langZhTW lang = "zh-TW"
	langEN   lang = "en"
	langRU   lang = "ru"
)

// msgKey identifies one entry in the message catalog. Log/warning keys and
// UI-only keys (prefixed "ui_") share the same catalog and the same tr()
// lookup on purpose: the panel HTML embeds this whole catalog as JSON so the
// Go side and the browser side never need two copies of the same strings.
type msgKey string

const (
	msgAuthListFailed        msgKey = "auth_list_failed"
	msgNoModelForProvider    msgKey = "no_model_for_provider"
	msgAPIKeyResolveFailed   msgKey = "api_key_resolve_failed"
	msgStatePersistFailed    msgKey = "state_persist_failed"
	msgModelPrecheckFailed   msgKey = "model_precheck_failed"
	msgModelNotExposed       msgKey = "model_not_exposed"
	msgAuthModelUnavailable  msgKey = "auth_model_unavailable"
	msgWarmupSendFailed      msgKey = "warmup_send_failed"
	msgNotCoveredAfterRounds msgKey = "not_covered_after_rounds"
	msgWarmedAccount         msgKey = "warmed_account"
	msgWarmupDidNotCover     msgKey = "warmup_did_not_cover"
	msgSkippedDisabled       msgKey = "skipped_disabled_or_unavailable"
	msgSkippedNoModel        msgKey = "skipped_not_enabled_no_model"
	msgSkippedCooldown       msgKey = "skipped_cooldown"
	msgEngineNotRunning      msgKey = "engine_not_running"
	msgRunFailed             msgKey = "run_failed"

	msgUIPageTitle             msgKey = "ui_page_title"
	msgUIPageSubtitle          msgKey = "ui_page_subtitle"
	msgUISectionConfig         msgKey = "ui_section_config"
	msgUILabelEnabled          msgKey = "ui_label_enabled"
	msgUILabelTimezone         msgKey = "ui_label_timezone"
	msgUILabelBaseURL          msgKey = "ui_label_base_url"
	msgUILabelMessage          msgKey = "ui_label_message"
	msgUILabelMaxTokens        msgKey = "ui_label_max_tokens"
	msgUILabelMaxRounds        msgKey = "ui_label_max_rounds"
	msgUILabelCatchUp          msgKey = "ui_label_catch_up_minutes"
	msgUILabelLastTick         msgKey = "ui_label_last_tick"
	msgUILabelLastTickError    msgKey = "ui_label_last_tick_error"
	msgUISectionAccounts       msgKey = "ui_section_accounts"
	msgUIColName               msgKey = "ui_col_name"
	msgUIColProvider           msgKey = "ui_col_provider"
	msgUIColModel              msgKey = "ui_col_model"
	msgUIColTimes              msgKey = "ui_col_times"
	msgUIColNextTrigger        msgKey = "ui_col_next_trigger"
	msgUIColStatus             msgKey = "ui_col_status"
	msgUISectionRecent         msgKey = "ui_section_recent"
	msgUIColDate               msgKey = "ui_col_date"
	msgUIColTime               msgKey = "ui_col_time"
	msgUIColAuth               msgKey = "ui_col_auth"
	msgUIColCovered            msgKey = "ui_col_covered"
	msgUIColRounds             msgKey = "ui_col_rounds"
	msgUIColStatusCode         msgKey = "ui_col_status_code"
	msgUIColWarning            msgKey = "ui_col_warning"
	msgUIYes                   msgKey = "ui_yes"
	msgUINo                    msgKey = "ui_no"
	msgUINone                  msgKey = "ui_none"
	msgUIRefresh               msgKey = "ui_refresh"
	msgUIWarmUpNow             msgKey = "ui_warm_up_now"
	msgUIAuthFilterPlaceholder msgKey = "ui_auth_filter_placeholder"
	msgUILoading               msgKey = "ui_loading"
	msgUILoadFailed            msgKey = "ui_load_failed"
	msgUIRunResultTitle        msgKey = "ui_run_result_title"
	msgUIRunAttempted          msgKey = "ui_run_attempted"
	msgUIRunSkipped            msgKey = "ui_run_skipped"
	msgUIRunOutcomes           msgKey = "ui_run_outcomes"
	msgUIFooterNote            msgKey = "ui_footer_note"
)

// messagesEN is the source-of-truth key set: every other language map is
// checked against exactly these keys (see TestMessageCatalogsHaveSameKeys).
var messagesEN = map[msgKey]string{
	msgAuthListFailed:        "host.auth.list failed, skipping this tick: %v",
	msgNoModelForProvider:    "auth %s has no configured model for its provider, skipping",
	msgAPIKeyResolveFailed:   "cannot resolve api-key, skipping %d due provider group(s): %v",
	msgStatePersistFailed:    "failed to persist state for auth=%s slot=%s: %v",
	msgModelPrecheckFailed:   "model precheck (GET /v1/models) failed, sending warmup requests without it: %v",
	msgModelNotExposed:       "model %s not exposed by this CPA instance (see GET /v1/models)",
	msgAuthModelUnavailable:  "auth=%s provider=%s model=%s slot=%s: %s",
	msgWarmupSendFailed:      "warmup request for provider=%s model=%s failed to send: %v",
	msgNotCoveredAfterRounds: "not covered by any usage record after %d round(s)",
	msgWarmedAccount:         "warmed auth=%s provider=%s model=%s slot=%s status=%d rounds=%d",
	msgWarmupDidNotCover:     "warmup did not cover auth=%s provider=%s model=%s slot=%s: %s",
	msgSkippedDisabled:       "disabled or unavailable",
	msgSkippedNoModel:        "not enabled or no resolvable model for its provider",
	msgSkippedCooldown:       "manual trigger cooldown (60s) not elapsed",
	msgEngineNotRunning:      "engine not running",
	msgRunFailed:             "run failed: %s",

	msgUIPageTitle:             "Quota Warmup",
	msgUIPageSubtitle:          "Per-account 5-hour quota warmup schedule",
	msgUISectionConfig:         "Configuration",
	msgUILabelEnabled:          "Enabled",
	msgUILabelTimezone:         "Timezone",
	msgUILabelBaseURL:          "Base URL",
	msgUILabelMessage:          "Message",
	msgUILabelMaxTokens:        "Max tokens",
	msgUILabelMaxRounds:        "Max rounds",
	msgUILabelCatchUp:          "Catch-up window (minutes)",
	msgUILabelLastTick:         "Last tick",
	msgUILabelLastTickError:    "Last tick error",
	msgUISectionAccounts:       "Accounts",
	msgUIColName:               "Name",
	msgUIColProvider:           "Provider",
	msgUIColModel:              "Model",
	msgUIColTimes:              "Times",
	msgUIColNextTrigger:        "Next trigger",
	msgUIColStatus:             "Status",
	msgUISectionRecent:         "Recent results",
	msgUIColDate:               "Date",
	msgUIColTime:               "Time",
	msgUIColAuth:               "Account",
	msgUIColCovered:            "Covered",
	msgUIColRounds:             "Rounds",
	msgUIColStatusCode:         "Status code",
	msgUIColWarning:            "Warning",
	msgUIYes:                   "Yes",
	msgUINo:                    "No",
	msgUINone:                  "—",
	msgUIRefresh:               "Refresh",
	msgUIWarmUpNow:             "Warm up now",
	msgUIAuthFilterPlaceholder: "Auth filter (glob, optional)",
	msgUILoading:               "Loading…",
	msgUILoadFailed:            "Failed to load status",
	msgUIRunResultTitle:        "Manual run result",
	msgUIRunAttempted:          "Attempted",
	msgUIRunSkipped:            "Skipped",
	msgUIRunOutcomes:           "Outcomes",
	msgUIFooterNote:            "Data comes from GET /status; this page never writes to CPA's own config.yaml.",
}

var messagesZhCN = map[msgKey]string{
	msgAuthListFailed:        "host.auth.list 调用失败，跳过本次 tick：%v",
	msgNoModelForProvider:    "账号 %s 所属 provider 未配置模型，跳过",
	msgAPIKeyResolveFailed:   "无法解析 api-key，跳过本次 %d 个到期的 provider 分组：%v",
	msgStatePersistFailed:    "auth=%s slot=%s 的状态写入失败：%v",
	msgModelPrecheckFailed:   "模型预检（GET /v1/models）失败，跳过预检直接发送预热请求：%v",
	msgModelNotExposed:       "模型 %s 未出现在本实例的 GET /v1/models 列表中",
	msgAuthModelUnavailable:  "auth=%s provider=%s model=%s slot=%s：%s",
	msgWarmupSendFailed:      "provider=%s model=%s 的预热请求发送失败：%v",
	msgNotCoveredAfterRounds: "尝试 %d 轮后仍未被任何 usage 记录覆盖",
	msgWarmedAccount:         "已完成预热 auth=%s provider=%s model=%s slot=%s status=%d rounds=%d",
	msgWarmupDidNotCover:     "预热未能覆盖 auth=%s provider=%s model=%s slot=%s：%s",
	msgSkippedDisabled:       "已禁用或不可用",
	msgSkippedNoModel:        "未启用，或其 provider 没有可解析的模型",
	msgSkippedCooldown:       "手动触发冷却时间（60 秒）尚未过去",
	msgEngineNotRunning:      "引擎未运行",
	msgRunFailed:             "执行失败：%s",

	msgUIPageTitle:             "配额预热",
	msgUIPageSubtitle:          "各账号 5 小时额度窗口的预热计划",
	msgUISectionConfig:         "配置",
	msgUILabelEnabled:          "启用",
	msgUILabelTimezone:         "时区",
	msgUILabelBaseURL:          "基础地址",
	msgUILabelMessage:          "预热消息",
	msgUILabelMaxTokens:        "最大 token 数",
	msgUILabelMaxRounds:        "最大轮次",
	msgUILabelCatchUp:          "补发窗口（分钟）",
	msgUILabelLastTick:         "最近一次 tick",
	msgUILabelLastTickError:    "最近一次 tick 错误",
	msgUISectionAccounts:       "账号",
	msgUIColName:               "名称",
	msgUIColProvider:           "Provider",
	msgUIColModel:              "模型",
	msgUIColTimes:              "时间点",
	msgUIColNextTrigger:        "下次触发",
	msgUIColStatus:             "状态",
	msgUISectionRecent:         "最近记录",
	msgUIColDate:               "日期",
	msgUIColTime:               "时刻",
	msgUIColAuth:               "账号",
	msgUIColCovered:            "已覆盖",
	msgUIColRounds:             "轮次",
	msgUIColStatusCode:         "状态码",
	msgUIColWarning:            "警告",
	msgUIYes:                   "是",
	msgUINo:                    "否",
	msgUINone:                  "—",
	msgUIRefresh:               "刷新",
	msgUIWarmUpNow:             "立即预热",
	msgUIAuthFilterPlaceholder: "账号过滤（glob，可留空）",
	msgUILoading:               "加载中…",
	msgUILoadFailed:            "状态加载失败",
	msgUIRunResultTitle:        "手动执行结果",
	msgUIRunAttempted:          "已尝试",
	msgUIRunSkipped:            "已跳过",
	msgUIRunOutcomes:           "结果",
	msgUIFooterNote:            "数据来自 GET /status；本页面不会写入 CPA 自身的 config.yaml。",
}

var messagesZhTW = map[msgKey]string{
	msgAuthListFailed:        "host.auth.list 呼叫失敗，略過本次 tick：%v",
	msgNoModelForProvider:    "帳號 %s 所屬 provider 未設定模型，略過",
	msgAPIKeyResolveFailed:   "無法解析 api-key，略過本次 %d 個到期的 provider 分組：%v",
	msgStatePersistFailed:    "auth=%s slot=%s 的狀態寫入失敗：%v",
	msgModelPrecheckFailed:   "模型預檢（GET /v1/models）失敗，略過預檢直接傳送預熱請求：%v",
	msgModelNotExposed:       "模型 %s 未出現在本實例的 GET /v1/models 清單中",
	msgAuthModelUnavailable:  "auth=%s provider=%s model=%s slot=%s：%s",
	msgWarmupSendFailed:      "provider=%s model=%s 的預熱請求傳送失敗：%v",
	msgNotCoveredAfterRounds: "嘗試 %d 輪後仍未被任何 usage 記錄覆蓋",
	msgWarmedAccount:         "已完成預熱 auth=%s provider=%s model=%s slot=%s status=%d rounds=%d",
	msgWarmupDidNotCover:     "預熱未能覆蓋 auth=%s provider=%s model=%s slot=%s：%s",
	msgSkippedDisabled:       "已停用或不可用",
	msgSkippedNoModel:        "未啟用，或其 provider 沒有可解析的模型",
	msgSkippedCooldown:       "手動觸發冷卻時間（60 秒）尚未過去",
	msgEngineNotRunning:      "引擎未執行",
	msgRunFailed:             "執行失敗：%s",

	msgUIPageTitle:             "配額預熱",
	msgUIPageSubtitle:          "各帳號 5 小時額度視窗的預熱計畫",
	msgUISectionConfig:         "設定",
	msgUILabelEnabled:          "啟用",
	msgUILabelTimezone:         "時區",
	msgUILabelBaseURL:          "基礎位址",
	msgUILabelMessage:          "預熱訊息",
	msgUILabelMaxTokens:        "最大 token 數",
	msgUILabelMaxRounds:        "最大輪次",
	msgUILabelCatchUp:          "補發視窗（分鐘）",
	msgUILabelLastTick:         "最近一次 tick",
	msgUILabelLastTickError:    "最近一次 tick 錯誤",
	msgUISectionAccounts:       "帳號",
	msgUIColName:               "名稱",
	msgUIColProvider:           "Provider",
	msgUIColModel:              "模型",
	msgUIColTimes:              "時間點",
	msgUIColNextTrigger:        "下次觸發",
	msgUIColStatus:             "狀態",
	msgUISectionRecent:         "最近記錄",
	msgUIColDate:               "日期",
	msgUIColTime:               "時刻",
	msgUIColAuth:               "帳號",
	msgUIColCovered:            "已覆蓋",
	msgUIColRounds:             "輪次",
	msgUIColStatusCode:         "狀態碼",
	msgUIColWarning:            "警告",
	msgUIYes:                   "是",
	msgUINo:                    "否",
	msgUINone:                  "—",
	msgUIRefresh:               "重新整理",
	msgUIWarmUpNow:             "立即預熱",
	msgUIAuthFilterPlaceholder: "帳號過濾（glob，可留空）",
	msgUILoading:               "載入中…",
	msgUILoadFailed:            "狀態載入失敗",
	msgUIRunResultTitle:        "手動執行結果",
	msgUIRunAttempted:          "已嘗試",
	msgUIRunSkipped:            "已略過",
	msgUIRunOutcomes:           "結果",
	msgUIFooterNote:            "資料來自 GET /status；本頁面不會寫入 CPA 自身的 config.yaml。",
}

// messagesRU. Written as natural technical Russian, not a machine-translated
// placeholder: verb aspect, case agreement and word order were chosen for
// how a Russian-speaking operator would actually phrase these, not word-for-
// word from the English source. The %d round-count message accepts the
// simplified genitive plural ("раундов") for every count rather than doing
// full Russian plural-rule branching (one/few/many), which is a deliberate,
// documented simplification -- not an oversight.
var messagesRU = map[msgKey]string{
	msgAuthListFailed:        "Вызов host.auth.list завершился ошибкой, этот тик пропущен: %v",
	msgNoModelForProvider:    "Для аккаунта %s не задана модель у его провайдера, пропускаем",
	msgAPIKeyResolveFailed:   "Не удалось определить api-key, пропускаем %d готовых к прогреву групп провайдеров: %v",
	msgStatePersistFailed:    "Не удалось сохранить состояние для auth=%s slot=%s: %v",
	msgModelPrecheckFailed:   "Проверка модели (GET /v1/models) не удалась, отправляем прогрев без неё: %v",
	msgModelNotExposed:       "Модель %s отсутствует в списке GET /v1/models этого экземпляра",
	msgAuthModelUnavailable:  "auth=%s provider=%s model=%s slot=%s: %s",
	msgWarmupSendFailed:      "Не удалось отправить прогревочный запрос provider=%s model=%s: %v",
	msgNotCoveredAfterRounds: "не подтверждено ни одной записью usage после %d раундов",
	msgWarmedAccount:         "Прогрет auth=%s provider=%s model=%s slot=%s status=%d rounds=%d",
	msgWarmupDidNotCover:     "Прогрев не затронул auth=%s provider=%s model=%s slot=%s: %s",
	msgSkippedDisabled:       "отключён или недоступен",
	msgSkippedNoModel:        "не включён либо для его провайдера не задана подходящая модель",
	msgSkippedCooldown:       "не истёк период ожидания (60 с) после последнего ручного запуска",
	msgEngineNotRunning:      "движок не запущен",
	msgRunFailed:             "запуск завершился ошибкой: %s",

	msgUIPageTitle:             "Прогрев квоты",
	msgUIPageSubtitle:          "График прогрева 5-часового окна квоты для каждого аккаунта",
	msgUISectionConfig:         "Конфигурация",
	msgUILabelEnabled:          "Включено",
	msgUILabelTimezone:         "Часовой пояс",
	msgUILabelBaseURL:          "Базовый URL",
	msgUILabelMessage:          "Сообщение",
	msgUILabelMaxTokens:        "Макс. токенов",
	msgUILabelMaxRounds:        "Макс. раундов",
	msgUILabelCatchUp:          "Окно доотправки (мин.)",
	msgUILabelLastTick:         "Последний тик",
	msgUILabelLastTickError:    "Ошибка последнего тика",
	msgUISectionAccounts:       "Аккаунты",
	msgUIColName:               "Имя",
	msgUIColProvider:           "Провайдер",
	msgUIColModel:              "Модель",
	msgUIColTimes:              "Время",
	msgUIColNextTrigger:        "Следующий запуск",
	msgUIColStatus:             "Статус",
	msgUISectionRecent:         "Последние результаты",
	msgUIColDate:               "Дата",
	msgUIColTime:               "Время",
	msgUIColAuth:               "Аккаунт",
	msgUIColCovered:            "Охвачен",
	msgUIColRounds:             "Раунды",
	msgUIColStatusCode:         "Код статуса",
	msgUIColWarning:            "Предупреждение",
	msgUIYes:                   "Да",
	msgUINo:                    "Нет",
	msgUINone:                  "—",
	msgUIRefresh:               "Обновить",
	msgUIWarmUpNow:             "Прогреть сейчас",
	msgUIAuthFilterPlaceholder: "Фильтр аккаунтов (glob, необязательно)",
	msgUILoading:               "Загрузка…",
	msgUILoadFailed:            "Не удалось загрузить статус",
	msgUIRunResultTitle:        "Результат ручного запуска",
	msgUIRunAttempted:          "Затронуты",
	msgUIRunSkipped:            "Пропущены",
	msgUIRunOutcomes:           "Итоги",
	msgUIFooterNote:            "Данные берутся из GET /status; эта страница никогда не изменяет config.yaml самого CPA.",
}

var messagesByLang = map[lang]map[msgKey]string{
	langEN:   messagesEN,
	langZhCN: messagesZhCN,
	langZhTW: messagesZhTW,
	langRU:   messagesRU,
}

// supportedLanguages lists every lang tr()/the panel page can render, in the
// canonical order CPA's own console declares them.
var supportedLanguages = []lang{langZhCN, langZhTW, langEN, langRU}

// tr renders key in language l, falling back to English if l is unknown or
// the key is missing from l's table, and finally to the bare key string if
// even English has nothing (a programming error, not a translation gap).
func tr(l lang, key msgKey, args ...any) string {
	if table, ok := messagesByLang[l]; ok {
		if format, ok := table[key]; ok {
			return renderMessage(format, args)
		}
	}
	if format, ok := messagesEN[key]; ok {
		return renderMessage(format, args)
	}
	return string(key)
}

func renderMessage(format string, args []any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// normalizeLangTag maps an exact, case-insensitive match against one of the
// four canonical language codes to its canonical form. It does not do
// prefix/fallback matching -- that is what languageFromAcceptHeader,
// languageFromEnv and the browser-side navigator-language logic in the panel
// HTML are for.
func normalizeLangTag(s string) (lang, bool) {
	s = strings.TrimSpace(s)
	for _, l := range supportedLanguages {
		if strings.EqualFold(s, string(l)) {
			return l, true
		}
	}
	return "", false
}

// zhTWRegionPrefixes are the region/script subtags that select Traditional
// Chinese over Simplified, mirroring CPA's own console
// (ol=["zh-tw","zh-hk","zh-mo","zh-hant"] in its bundled JS).
var zhTWRegionPrefixes = []string{"zh-tw", "zh-hk", "zh-mo", "zh-hant"}

// classifyLanguageTag maps an arbitrary BCP-47-ish tag (already lower-cased
// or not) to one of the four supported languages using the same "zh* -> CN
// unless a Taiwan/HK/Macau/Hant region -> TW, ru* -> ru, else en" rule the
// console's navigator-language fallback uses.
func classifyLanguageTag(tag string) lang {
	t := strings.ToLower(strings.TrimSpace(tag))
	for _, prefix := range zhTWRegionPrefixes {
		if strings.HasPrefix(t, prefix) {
			return langZhTW
		}
	}
	switch {
	case strings.HasPrefix(t, "zh"):
		return langZhCN
	case strings.HasPrefix(t, "ru"):
		return langRU
	default:
		return langEN
	}
}

// languageFromAcceptHeader parses an RFC 7231 Accept-Language header value
// and classifies the highest-weighted tag. The first tag encountered wins
// ties (a strictly-greater comparison only updates the best candidate),
// matching the header's own left-to-right preference order. Returns
// ok=false only when raw has no usable tag at all (empty/whitespace).
func languageFromAcceptHeader(raw string) (lang, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	bestTag := ""
	bestQ := -1.0
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag := part
		q := 1.0
		if semi := strings.Index(part, ";"); semi >= 0 {
			tag = strings.TrimSpace(part[:semi])
			for _, param := range strings.Split(part[semi+1:], ";") {
				param = strings.TrimSpace(param)
				rest, ok := strings.CutPrefix(param, "q=")
				if !ok {
					continue
				}
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(rest), 64); err == nil {
					q = parsed
				}
			}
		}
		if tag == "" || tag == "*" {
			continue
		}
		if q > bestQ {
			bestQ = q
			bestTag = tag
		}
	}
	if bestTag == "" {
		return "", false
	}
	return classifyLanguageTag(bestTag), true
}

// languageFromEnv classifies a POSIX locale value such as "zh_CN.UTF-8",
// "en_US", "ru_RU.KOI8-R", "C" or "POSIX". "C"/"POSIX" and empty values are
// not classifiable (ok=false) -- they say nothing about a human language.
func languageFromEnv(raw string) (lang, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	// Strip the encoding/modifier suffix first ("c.utf-8" -> "c",
	// "zh_cn.gb18030" -> "zh_cn"): the "C"/"POSIX" no-locale sentinel is
	// routinely spelled "C.UTF-8" (this plugin's own build/test environment
	// uses exactly that), and checking for it before stripping would
	// misclassify it as English instead of recognizing it as unset.
	if i := strings.IndexAny(v, ".@"); i >= 0 {
		v = v[:i]
	}
	if v == "" || v == "c" || v == "posix" {
		return "", false
	}
	v = strings.ReplaceAll(v, "_", "-")
	return classifyLanguageTag(v), true
}

// firstNonEmpty returns the first non-blank string, or "" if all are blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// envLocale reads the process's own locale environment, preferring LC_ALL
// over LANG per POSIX precedence (LC_ALL overrides every other locale
// variable).
func envLocale() string {
	return firstNonEmpty(os.Getenv("LC_ALL"), os.Getenv("LANG"))
}

// requestLanguage resolves the language for one management/resource request.
//
// Precedence: an explicit ?lang= query parameter is the most specific,
// deliberate signal available -- the panel page always sends one, computed
// from the visiting browser's own localStorage/navigator language -- so it
// wins even over a pinned `language:` config value. From there: the pinned
// config (when not "auto"), then Accept-Language, then LANG/LC_ALL, then
// zh-CN (the console's own default). This ordering is this plugin's own
// interpretation of an ambiguous spec (the spec's "auto" chain does not say
// where a pinned non-auto config sits relative to a request's own ?lang=);
// treating ?lang= as always-highest keeps the panel page correct for its own
// visitor even when the operator has pinned a different server-wide default.
func requestLanguage(cfg pluginConfig, queryLang, acceptLanguage string) lang {
	if l, ok := normalizeLangTag(queryLang); ok {
		return l
	}
	if l, ok := normalizeLangTag(cfg.Language); ok && !strings.EqualFold(cfg.Language, "auto") {
		return l
	}
	if l, ok := languageFromAcceptHeader(acceptLanguage); ok {
		return l
	}
	if l, ok := languageFromEnv(envLocale()); ok {
		return l
	}
	return langZhCN
}

// logLanguage resolves the language for host.log lines, which have no
// per-request context at all: the pinned config (when not "auto"), then
// LANG/LC_ALL, then zh-CN.
func logLanguage(cfg pluginConfig) lang {
	if l, ok := normalizeLangTag(cfg.Language); ok && !strings.EqualFold(cfg.Language, "auto") {
		return l
	}
	if l, ok := languageFromEnv(envLocale()); ok {
		return l
	}
	return langZhCN
}

// i18nCatalogJSON marshals the full message catalog (log/warning keys and
// UI-only keys alike) for injection into the panel HTML, so the page's own
// JS can look up "ui_*" keys by the same lang/key identifiers the Go side
// uses -- one catalog, not two.
func i18nCatalogJSON() ([]byte, error) {
	return json.Marshal(messagesByLang)
}
