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

	// v0.3.0 additions.
	msgNoAccountsConfigured      msgKey = "no_accounts_configured"
	msgInvalidTimeExpr           msgKey = "invalid_time_expr"
	msgNoCandidateModelAvailable msgKey = "no_candidate_model_available"
	msgSetInvalidScope           msgKey = "set_invalid_scope"
	msgSetAuthRequired           msgKey = "set_auth_required"
	msgSetModelRequired          msgKey = "set_model_required"
	msgSetModelNotAvailable      msgKey = "set_model_not_available"
	msgSetPrecheckFailedWarning  msgKey = "set_precheck_failed_warning"
	msgSetSaved                  msgKey = "set_saved"
	msgSetCleared                msgKey = "set_cleared"
	msgSkippedNotInAccounts      msgKey = "skipped_not_in_accounts"

	// v0.4.0 additions: the externally maintained quota-warmup.yaml (file
	// mode), the inline-to-file migration notice, and overrides.json's
	// one-time migration into it.
	msgWarmupFileError        msgKey = "warmup_file_error"
	msgWarmupFileParseFailed  msgKey = "warmup_file_parse_failed"
	msgOverridesMigrated      msgKey = "overrides_migrated"
	msgOverridesMigrateFailed msgKey = "overrides_migrate_failed"
	msgInlineModeNotice       msgKey = "inline_mode_notice"
	msgSetUnsupportedLegacy   msgKey = "set_unsupported_legacy"
	msgSetNothingToUpdate     msgKey = "set_nothing_to_update"
	msgSetInvalidEnabled      msgKey = "set_invalid_enabled"
	msgSkippedFileDisabled    msgKey = "skipped_file_disabled"

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

	// v0.3.0 additions: minimal-config summary fields, the editable model
	// column, and the global-model editor.
	msgUILabelTime        msgKey = "ui_label_time"
	msgUILabelAccounts    msgKey = "ui_label_accounts"
	msgUILabelLanguage    msgKey = "ui_label_language"
	msgUILabelGlobalModel msgKey = "ui_label_global_model"
	msgUITimezoneAuto     msgKey = "ui_timezone_auto"
	msgUITimezoneManual   msgKey = "ui_timezone_manual"
	msgUINoAccountsHint   msgKey = "ui_no_accounts_hint"
	msgUIModelSave        msgKey = "ui_model_save"
	msgUIModelAuto        msgKey = "ui_model_auto"
	msgUIModelPlaceholder msgKey = "ui_model_placeholder"
	msgUISetSaved         msgKey = "ui_set_saved"
	msgUISetCleared       msgKey = "ui_set_cleared"
	msgUISetFailed        msgKey = "ui_set_failed"

	// v0.4.0 additions: mode/config-file display and the file-mode
	// enabled/time editors.
	msgUILabelMode       msgKey = "ui_label_mode"
	msgUIModeFile        msgKey = "ui_mode_file"
	msgUIModeInline      msgKey = "ui_mode_inline"
	msgUILabelConfigFile msgKey = "ui_label_config_file"
	msgUILabelParseError msgKey = "ui_label_parse_error"
	msgUITimePlaceholder msgKey = "ui_time_placeholder"

	// v0.5.0 additions: the panel's "编辑配置文件" (edit config file) online
	// editor for quota-warmup.yaml, and its backing GET .../config-yaml and
	// GET .../config-yaml/save routes. Per the coordinator's explicit
	// instruction, every string introduced for this feature (server-side
	// error/status text and the editor's own UI labels alike) is Chinese
	// text stored identically in all four language maps below -- not a real
	// per-language translation like every other key in this catalog -- so
	// there is nothing to translate here, only to keep
	// TestMessageCatalogsHaveTheSameKeys passing.
	msgConfigYAMLNotFileMode    msgKey = "config_yaml_not_file_mode"
	msgConfigYAMLReadFailed     msgKey = "config_yaml_read_failed"
	msgConfigYAMLMissingContent msgKey = "config_yaml_missing_content"
	msgConfigYAMLMissingMtime   msgKey = "config_yaml_missing_mtime"
	msgConfigYAMLInvalidBase64  msgKey = "config_yaml_invalid_base64"
	msgConfigYAMLTooLarge       msgKey = "config_yaml_too_large"
	msgConfigYAMLConflict       msgKey = "config_yaml_conflict"
	msgConfigYAMLWriteFailed    msgKey = "config_yaml_write_failed"
	msgConfigYAMLSaved          msgKey = "config_yaml_saved"

	msgUISectionConfigYAML     msgKey = "ui_section_config_yaml"
	msgUIConfigYAMLReload      msgKey = "ui_config_yaml_reload"
	msgUIConfigYAMLSave        msgKey = "ui_config_yaml_save"
	msgUIConfigYAMLLoadFailed  msgKey = "ui_config_yaml_load_failed"
	msgUIConfigYAMLEncodeError msgKey = "ui_config_yaml_encode_error"

	// v0.6.0 additions: the redesigned accounts-table status column (a
	// dedicated "Enabled" state word, distinct from msgUILabelEnabled which
	// labels the column header itself) and the config section's collapsed
	// "Advanced settings" <details>. msgUISetSaved/msgUISetFailed already
	// existed (msgUISetSaved was declared and translated but never actually
	// referenced anywhere in the template; msgUISetFailed was declared but
	// had never been given translations in any of the four maps below --
	// both are now wired up as the redesigned per-row inline
	// saving/saved/failed indicator's text, see panel.go's flashRow).
	msgUIStatusEnabled    msgKey = "ui_status_enabled"
	msgUIAdvancedSettings msgKey = "ui_advanced_settings"

	// v0.6.1 additions: the model field's custom dropdown combobox (native
	// <datalist> only pops up its suggestions when the current input value
	// is a *prefix* match, so it silently showed nothing once a field
	// already held a full model name or "auto" -- see modelComboMenu's own
	// doc comment in panel.go). ui_expand labels the ▾ toggle button
	// (aria-label, not visible text); ui_model_auto_full is the combo
	// menu's pinned-first "auto (...)" entry's descriptive suffix.
	msgUIExpand        msgKey = "ui_expand"
	msgUIModelAutoFull msgKey = "ui_model_auto_full"
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

	msgNoAccountsConfigured:      "no accounts configured, nothing will be warmed up",
	msgInvalidTimeExpr:           "failed to parse time expression, skipped: %s",
	msgNoCandidateModelAvailable: "none of provider=%s's candidate models are exposed by GET /v1/models, skipping",
	msgSetInvalidScope:           "scope must be \"global\" or \"auth\"",
	msgSetAuthRequired:           "auth is required when scope=auth",
	msgSetModelRequired:          "model is required",
	msgSetModelNotAvailable:      "model %s is not listed by GET /v1/models, not saved",
	msgSetPrecheckFailedWarning:  "model precheck failed, saved without validation: %v",
	msgSetSaved:                  "saved",
	msgSetCleared:                "cleared, back to auto-selection",
	msgSkippedNotInAccounts:      "not included in accounts[]",
	msgWarmupFileError:           "failed to maintain %s: %v",
	msgWarmupFileParseFailed:     "%s failed to parse, continuing with the last valid configuration: %v",
	msgOverridesMigrated:         "migrated overrides.json's model overrides into %s (renamed to .migrated)",
	msgOverridesMigrateFailed:    "failed to migrate overrides.json into %s: %v",
	msgInlineModeNotice:          "currently using the legacy inline config (time/model/accounts in config.yaml); you can migrate by removing those top-level keys and keeping just enabled: true, which auto-generates %s",
	msgSetUnsupportedLegacy:      "the legacy (v0.1/v0.2) inline config does not support panel edits; please upgrade the config format",
	msgSetNothingToUpdate:        "nothing to update: pass at least one of enabled/time/model",
	msgSetInvalidEnabled:         "enabled must be true or false",
	msgSkippedFileDisabled:       "disabled",

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

	msgUILabelTime:        "Warmup time",
	msgUILabelAccounts:    "Accounts",
	msgUILabelLanguage:    "Language",
	msgUILabelGlobalModel: "Global model",
	msgUITimezoneAuto:     "Auto (follows the host process)",
	msgUITimezoneManual:   "Manually set",
	msgUINoAccountsHint:   "No accounts configured, nothing will be warmed up",
	msgUIModelSave:        "Save",
	msgUIModelAuto:        "Auto",
	msgUIModelPlaceholder: "Model",
	msgUISetSaved:         "Saved",
	msgUISetCleared:       "Cleared",
	msgUISetFailed:        "Save failed",
	msgUILabelMode:        "Mode",
	msgUIModeFile:         "External file",
	msgUIModeInline:       "Inline (legacy)",
	msgUILabelConfigFile:  "Config file path",
	msgUILabelParseError:  "Parse error",
	msgUITimePlaceholder:  "Time, e.g. 05:30 or a cron expression",

	msgConfigYAMLNotFileMode:    "该功能仅文件模式（quota-warmup.yaml）下可用",
	msgConfigYAMLReadFailed:     "读取配置文件失败：%v",
	msgConfigYAMLMissingContent: "缺少 content 参数",
	msgConfigYAMLMissingMtime:   "缺少 mtime 参数",
	msgConfigYAMLInvalidBase64:  "content 不是合法的 base64url 编码",
	msgConfigYAMLTooLarge:       "内容超过 256 KiB 上限",
	msgConfigYAMLConflict:       "文件已被别处修改，请刷新",
	msgConfigYAMLWriteFailed:    "写入配置文件失败：%v",
	msgConfigYAMLSaved:          "已保存并重新加载",

	msgUISectionConfigYAML:     "编辑配置文件",
	msgUIConfigYAMLReload:      "重新载入",
	msgUIConfigYAMLSave:        "保存",
	msgUIConfigYAMLLoadFailed:  "配置文件加载失败",
	msgUIConfigYAMLEncodeError: "内容编码失败，请检查浏览器兼容性",
	msgUIStatusEnabled:         "Enabled",
	msgUIAdvancedSettings:      "Advanced settings",
	msgUIExpand:                "Expand",
	msgUIModelAutoFull:         "Auto-select",
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

	msgNoAccountsConfigured:      "未配置 accounts，不会预热任何账号",
	msgInvalidTimeExpr:           "时间表达式解析失败，已跳过：%s",
	msgNoCandidateModelAvailable: "provider=%s 的候选模型均未出现在 GET /v1/models 中，跳过",
	msgSetInvalidScope:           "scope 必须是 global 或 auth",
	msgSetAuthRequired:           "scope=auth 时必须提供 auth 参数",
	msgSetModelRequired:          "缺少 model 参数",
	msgSetModelNotAvailable:      "模型 %s 未出现在 GET /v1/models 中，未保存",
	msgSetPrecheckFailedWarning:  "模型预检失败，已直接保存（未校验）：%v",
	msgSetSaved:                  "已保存",
	msgSetCleared:                "已清除，恢复自动选择",
	msgSkippedNotInAccounts:      "未包含在 accounts 列表中",
	msgWarmupFileError:           "维护 %s 失败：%v",
	msgWarmupFileParseFailed:     "%s 解析失败，继续使用上一次有效配置：%v",
	msgOverridesMigrated:         "已将 overrides.json 中的模型覆盖迁移到 %s（原文件已重命名为 .migrated）",
	msgOverridesMigrateFailed:    "迁移 overrides.json 到 %s 失败：%v",
	msgInlineModeNotice:          "当前使用旧版内联配置（time/model/accounts 写在 config.yaml），可删除这些顶层键、只保留 enabled: true 来迁移到独立文件（自动生成 %s）",
	msgSetUnsupportedLegacy:      "旧版内联配置（v0.1/v0.2）不支持面板设置，请升级配置格式",
	msgSetNothingToUpdate:        "没有要更新的内容：enabled/time/model 至少传一个",
	msgSetInvalidEnabled:         "enabled 必须是 true 或 false",
	msgSkippedFileDisabled:       "未启用",

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

	msgUILabelTime:        "预热时间",
	msgUILabelAccounts:    "账号",
	msgUILabelLanguage:    "语言",
	msgUILabelGlobalModel: "全局模型",
	msgUITimezoneAuto:     "自动（跟随宿主进程）",
	msgUITimezoneManual:   "已手动设置",
	msgUINoAccountsHint:   "未配置 accounts，不会预热任何账号",
	msgUIModelSave:        "保存",
	msgUIModelAuto:        "自动",
	msgUIModelPlaceholder: "模型",
	msgUISetSaved:         "已保存",
	msgUISetCleared:       "已清除",
	msgUISetFailed:        "保存失败",
	msgUILabelMode:        "模式",
	msgUIModeFile:         "独立文件",
	msgUIModeInline:       "内联（旧版）",
	msgUILabelConfigFile:  "配置文件路径",
	msgUILabelParseError:  "解析错误",
	msgUITimePlaceholder:  "时间，如 05:30 或 cron 表达式",

	msgConfigYAMLNotFileMode:    "该功能仅文件模式（quota-warmup.yaml）下可用",
	msgConfigYAMLReadFailed:     "读取配置文件失败：%v",
	msgConfigYAMLMissingContent: "缺少 content 参数",
	msgConfigYAMLMissingMtime:   "缺少 mtime 参数",
	msgConfigYAMLInvalidBase64:  "content 不是合法的 base64url 编码",
	msgConfigYAMLTooLarge:       "内容超过 256 KiB 上限",
	msgConfigYAMLConflict:       "文件已被别处修改，请刷新",
	msgConfigYAMLWriteFailed:    "写入配置文件失败：%v",
	msgConfigYAMLSaved:          "已保存并重新加载",

	msgUISectionConfigYAML:     "编辑配置文件",
	msgUIConfigYAMLReload:      "重新载入",
	msgUIConfigYAMLSave:        "保存",
	msgUIConfigYAMLLoadFailed:  "配置文件加载失败",
	msgUIConfigYAMLEncodeError: "内容编码失败，请检查浏览器兼容性",
	msgUIStatusEnabled:         "已启用",
	msgUIAdvancedSettings:      "高级设置",
	msgUIExpand:                "展开",
	msgUIModelAutoFull:         "自动选择",
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

	msgNoAccountsConfigured:      "未設定 accounts，不會預熱任何帳號",
	msgInvalidTimeExpr:           "時間表達式解析失敗，已略過：%s",
	msgNoCandidateModelAvailable: "provider=%s 的候選模型均未出現在 GET /v1/models 中，略過",
	msgSetInvalidScope:           "scope 必須是 global 或 auth",
	msgSetAuthRequired:           "scope=auth 時必須提供 auth 參數",
	msgSetModelRequired:          "缺少 model 參數",
	msgSetModelNotAvailable:      "模型 %s 未出現在 GET /v1/models 中，未儲存",
	msgSetPrecheckFailedWarning:  "模型預檢失敗，已直接儲存（未驗證）：%v",
	msgSetSaved:                  "已儲存",
	msgSetCleared:                "已清除，恢復自動選擇",
	msgSkippedNotInAccounts:      "未包含在 accounts 清單中",
	msgWarmupFileError:           "維護 %s 失敗：%v",
	msgWarmupFileParseFailed:     "%s 解析失敗，繼續使用上一次有效設定：%v",
	msgOverridesMigrated:         "已將 overrides.json 中的模型覆蓋遷移到 %s（原檔案已重新命名為 .migrated）",
	msgOverridesMigrateFailed:    "遷移 overrides.json 到 %s 失敗：%v",
	msgInlineModeNotice:          "目前使用舊版內聯設定（time/model/accounts 寫在 config.yaml），可刪除這些頂層鍵、只保留 enabled: true 來遷移到獨立檔案（自動產生 %s）",
	msgSetUnsupportedLegacy:      "舊版內聯設定（v0.1/v0.2）不支援面板設定，請升級設定格式",
	msgSetNothingToUpdate:        "沒有要更新的內容：enabled/time/model 至少傳一個",
	msgSetInvalidEnabled:         "enabled 必須是 true 或 false",
	msgSkippedFileDisabled:       "未啟用",

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

	msgUILabelTime:        "預熱時間",
	msgUILabelAccounts:    "帳號",
	msgUILabelLanguage:    "語言",
	msgUILabelGlobalModel: "全域模型",
	msgUITimezoneAuto:     "自動（跟隨宿主行程）",
	msgUITimezoneManual:   "已手動設定",
	msgUINoAccountsHint:   "未設定 accounts，不會預熱任何帳號",
	msgUIModelSave:        "儲存",
	msgUIModelAuto:        "自動",
	msgUIModelPlaceholder: "模型",
	msgUISetSaved:         "已儲存",
	msgUISetCleared:       "已清除",
	msgUISetFailed:        "儲存失敗",
	msgUILabelMode:        "模式",
	msgUIModeFile:         "獨立檔案",
	msgUIModeInline:       "內聯（舊版）",
	msgUILabelConfigFile:  "設定檔路徑",
	msgUILabelParseError:  "解析錯誤",
	msgUITimePlaceholder:  "時間，如 05:30 或 cron 表達式",

	msgConfigYAMLNotFileMode:    "该功能仅文件模式（quota-warmup.yaml）下可用",
	msgConfigYAMLReadFailed:     "读取配置文件失败：%v",
	msgConfigYAMLMissingContent: "缺少 content 参数",
	msgConfigYAMLMissingMtime:   "缺少 mtime 参数",
	msgConfigYAMLInvalidBase64:  "content 不是合法的 base64url 编码",
	msgConfigYAMLTooLarge:       "内容超过 256 KiB 上限",
	msgConfigYAMLConflict:       "文件已被别处修改，请刷新",
	msgConfigYAMLWriteFailed:    "写入配置文件失败：%v",
	msgConfigYAMLSaved:          "已保存并重新加载",

	msgUISectionConfigYAML:     "编辑配置文件",
	msgUIConfigYAMLReload:      "重新载入",
	msgUIConfigYAMLSave:        "保存",
	msgUIConfigYAMLLoadFailed:  "配置文件加载失败",
	msgUIConfigYAMLEncodeError: "内容编码失败，请检查浏览器兼容性",
	msgUIStatusEnabled:         "已啟用",
	msgUIAdvancedSettings:      "進階設定",
	msgUIExpand:                "展開",
	msgUIModelAutoFull:         "自動選擇",
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

	msgNoAccountsConfigured:      "аккаунты не настроены (accounts), прогрев не будет выполняться",
	msgInvalidTimeExpr:           "не удалось разобрать выражение времени, пропущено: %s",
	msgNoCandidateModelAvailable: "ни одна из моделей-кандидатов provider=%s не найдена в GET /v1/models, пропускаем",
	msgSetInvalidScope:           "scope должен быть \"global\" или \"auth\"",
	msgSetAuthRequired:           "при scope=auth необходимо указать auth",
	msgSetModelRequired:          "не указан параметр model",
	msgSetModelNotAvailable:      "модель %s отсутствует в GET /v1/models, не сохранено",
	msgSetPrecheckFailedWarning:  "проверка модели не удалась, сохранено без проверки: %v",
	msgSetSaved:                  "сохранено",
	msgSetCleared:                "сброшено, снова автовыбор",
	msgSkippedNotInAccounts:      "не включён в список accounts",
	msgWarmupFileError:           "не удалось поддерживать %s: %v",
	msgWarmupFileParseFailed:     "не удалось разобрать %s, продолжаем с последней рабочей конфигурацией: %v",
	msgOverridesMigrated:         "переопределения моделей из overrides.json перенесены в %s (файл переименован в .migrated)",
	msgOverridesMigrateFailed:    "не удалось перенести overrides.json в %s: %v",
	msgInlineModeNotice:          "сейчас используется устаревшая встроенная конфигурация (time/model/accounts в config.yaml); можно перейти на внешний файл, удалив эти ключи верхнего уровня и оставив только enabled: true (файл %s будет создан автоматически)",
	msgSetUnsupportedLegacy:      "устаревшая встроенная конфигурация (v0.1/v0.2) не поддерживает редактирование через панель, обновите формат конфигурации",
	msgSetNothingToUpdate:        "нечего обновлять: укажите хотя бы один из enabled/time/model",
	msgSetInvalidEnabled:         "enabled должен быть true или false",
	msgSkippedFileDisabled:       "отключён",

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

	msgUILabelTime:        "Время прогрева",
	msgUILabelAccounts:    "Аккаунты",
	msgUILabelLanguage:    "Язык",
	msgUILabelGlobalModel: "Глобальная модель",
	msgUITimezoneAuto:     "Авто (по часовому поясу хоста)",
	msgUITimezoneManual:   "Задан вручную",
	msgUINoAccountsHint:   "Аккаунты не настроены (accounts), прогрев не будет выполняться",
	msgUIModelSave:        "Сохранить",
	msgUIModelAuto:        "Авто",
	msgUIModelPlaceholder: "Модель",
	msgUISetSaved:         "Сохранено",
	msgUISetCleared:       "Сброшено",
	msgUISetFailed:        "Не удалось сохранить",
	msgUILabelMode:        "Режим",
	msgUIModeFile:         "Внешний файл",
	msgUIModeInline:       "Встроенный (устаревший)",
	msgUILabelConfigFile:  "Путь к файлу конфигурации",
	msgUILabelParseError:  "Ошибка разбора",
	msgUITimePlaceholder:  "Время, например 05:30 или cron-выражение",

	msgConfigYAMLNotFileMode:    "该功能仅文件模式（quota-warmup.yaml）下可用",
	msgConfigYAMLReadFailed:     "读取配置文件失败：%v",
	msgConfigYAMLMissingContent: "缺少 content 参数",
	msgConfigYAMLMissingMtime:   "缺少 mtime 参数",
	msgConfigYAMLInvalidBase64:  "content 不是合法的 base64url 编码",
	msgConfigYAMLTooLarge:       "内容超过 256 KiB 上限",
	msgConfigYAMLConflict:       "文件已被别处修改，请刷新",
	msgConfigYAMLWriteFailed:    "写入配置文件失败：%v",
	msgConfigYAMLSaved:          "已保存并重新加载",

	msgUISectionConfigYAML:     "编辑配置文件",
	msgUIConfigYAMLReload:      "重新载入",
	msgUIConfigYAMLSave:        "保存",
	msgUIConfigYAMLLoadFailed:  "配置文件加载失败",
	msgUIConfigYAMLEncodeError: "内容编码失败，请检查浏览器兼容性",
	msgUIStatusEnabled:         "Включено",
	msgUIAdvancedSettings:      "Дополнительные настройки",
	msgUIExpand:                "Развернуть",
	msgUIModelAutoFull:         "Автовыбор",
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
