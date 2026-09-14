package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "cpa-quota-warmup"
	pluginVersion = "0.2.1"
	logPrefix     = "[cpa-quota-warmup] "
)

func main() { fmt.Println(pluginName, pluginVersion) }

// engineMu guards the single process-wide *engine instance across
// plugin.register / plugin.reconfigure / plugin.shutdown calls.
var engineMu sync.Mutex
var currentEngine *engine

func ensureEngineRunning(cfg pluginConfig) error {
	engineMu.Lock()
	defer engineMu.Unlock()
	if currentEngine != nil {
		currentEngine.settings.Store(&cfg)
		return nil
	}
	path, err := stateFilePath()
	if err != nil {
		return err
	}
	e := newEngine(cfg, newStateStore(path), hostAuthLister{})
	e.start()
	currentEngine = e
	return nil
}

func reconfigureEngine(cfg pluginConfig) error {
	engineMu.Lock()
	e := currentEngine
	engineMu.Unlock()
	if e == nil {
		// No prior register somehow reached us; behave like a first
		// register instead of losing the config.
		return ensureEngineRunning(cfg)
	}
	e.settings.Store(&cfg)
	return nil
}

func shutdownEngine() {
	engineMu.Lock()
	e := currentEngine
	currentEngine = nil
	engineMu.Unlock()
	if e != nil {
		e.stop()
	}
}

func activeEngine() *engine {
	engineMu.Lock()
	defer engineMu.Unlock()
	return currentEngine
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		cfg, err := decodeConfig(raw)
		if err != nil {
			return nil, err
		}
		if err := ensureEngineRunning(cfg); err != nil {
			return nil, err
		}
		return okEnvelope(registrationPayload())
	case pluginabi.MethodPluginReconfigure:
		cfg, err := decodeConfig(raw)
		if err != nil {
			return nil, err
		}
		if err := reconfigureEngine(cfg); err != nil {
			return nil, err
		}
		return okEnvelope(registrationPayload())
	case pluginabi.MethodPluginQuiesce:
		return okEnvelope(struct{}{})
	case pluginabi.MethodPluginShutdown:
		shutdownEngine()
		return okEnvelope(struct{}{})
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &record)
		}
		if e := activeEngine(); e != nil {
			e.ring.record(record)
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagementRequest(raw)
	default:
		return errorEnvelope("unknown_method", "unsupported method"), nil
	}
}

func registrationPayload() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]bool    `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:    pluginName,
			Version: pluginVersion,
			Author:  "szxypi",
			// Required by CPA. This points to the host SDK, not a published plugin repository.
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			// Descriptions are static strings fixed at plugin.register time, so
			// they cannot follow a visiting browser's language the way host.log
			// lines and the status/run JSON do (see i18n.go). Each is one
			// bilingual sentence (中文 / English) instead.
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "为 false 时，每 30 秒的调度 tick 什么都不做 / When false, the scheduler tick is a no-op every 30s instead of warming anything up."},
				{Name: "priority", Type: pluginapi.ConfigFieldTypeInteger, Description: "插件加载优先级，只影响与其他插件的加载顺序 / Plugin load priority. Only affects load order relative to other plugins."},
				{Name: "log", Type: pluginapi.ConfigFieldTypeBoolean, Description: "每次预热尝试与每个跳过/警告情况都发一行 host.log / Send one host.log line per warmup attempt and per skip/warn condition."},
				{Name: "timezone", Type: pluginapi.ConfigFieldTypeString, Description: "default: 与 auths[].times 里 HH:MM 时间点所用的 IANA 时区，默认 Asia/Shanghai / IANA timezone the default: and auths[].times HH:MM schedules are interpreted in. Defaults to Asia/Shanghai."},
				{Name: "base-url", Type: pluginapi.ConfigFieldTypeString, Description: "本 CPA 实例自身的基础地址，插件用它给自己发预热请求 / This CPA instance's own base URL, used to send warmup chat completions to itself."},
				{Name: "api-key", Type: pluginapi.ConfigFieldTypeString, Description: "用于给插件自身预热请求鉴权的 Bearer key；留空则从当前工作目录的 config.yaml 读 api-keys[0] / Bearer key used to authenticate the plugin's own warmup requests. Empty reads api-keys[0] from the host's own config.yaml in the current working directory."},
				{Name: "message", Type: pluginapi.ConfigFieldTypeString, Description: "每次预热请求发送的用户消息正文，保持简短即可 / The user message body sent in every warmup request. Keep it minimal."},
				{Name: "max-tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "每次预热请求的 max_tokens，默认 16 / max_tokens on every warmup request. Default 16."},
				{Name: "max-rounds", Type: pluginapi.ConfigFieldTypeInteger, Description: "每个到期 provider 分组「发送再核对 usage」的最大轮次，用尽仍未覆盖的账号即放弃，默认 3 / Maximum number of send-then-check-usage rounds per due provider group before giving up on any account not yet covered. Default 3."},
				{Name: "catch-up-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "错过的时间点（例如服务重启后）在这个窗口内仍可补发，超过则当天跳过，默认 60 / How late a missed slot (e.g. after a service restart) may still fire before being skipped for the day. Default 60."},
				{Name: "language", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"auto", "zh-CN", "zh-TW", "en", "ru"}, Description: "host.log 与 status/run JSON 使用的语言；auto 会按请求的 ?lang=、Accept-Language、LANG/LC_ALL 环境变量依次协商，默认 auto / Language for host.log lines and the status/run JSON. \"auto\" (default) negotiates per call from ?lang=, Accept-Language, then LANG/LC_ALL."},
				{Name: "default", Type: pluginapi.ConfigFieldTypeObject, Description: "在 providers[] 与 auths[] 收窄之前，应用到每个账号的基线排程 / Baseline schedule applied to every auth file before providers[] and auths[] narrow it."},
				{Name: "default.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "账号默认是否参与预热，默认 true / Whether an auth file is warmed up at all by default. Default true."},
				{Name: "default.times", Type: pluginapi.ConfigFieldTypeArray, Description: "每个账号默认的 HH:MM 预热时间点列表，默认 [\"05:30\"] / Default list of HH:MM times of day to warm up each auth file at. Default [\"05:30\"]."},
				{Name: "providers", Type: pluginapi.ConfigFieldTypeObject, Description: "provider key（即 host.auth.list 上报的值，如 antigravity、codex、kimi、xai、claude、gemini-cli）到其最便宜预热模型的映射 / Map of provider key (as reported by host.auth.list, e.g. antigravity, codex, kimi, xai, claude, gemini-cli) to its cheapest warmup model."},
				{Name: "providers.<provider>.model", Type: pluginapi.ConfigFieldTypeString, Description: "该 provider 下每个账号预热请求所用的模型名，除非被 auths[] 覆盖 / Model name sent in the warmup request for every auth on this provider, unless an auths[] override replaces it."},
				{Name: "providers.<provider>.reasoning-effort", Type: pluginapi.ConfigFieldTypeString, Description: "可选，加到该 provider 预热请求里的 reasoning_effort（例如 gpt-*/codex 模型用 \"low\"） / Optional reasoning_effort field added to warmup requests for this provider (e.g. \"low\" for gpt-* / codex models)."},
				{Name: "auths", Type: pluginapi.ConfigFieldTypeArray, Description: "按账号文件名的有序覆盖列表；match 是对账号文件名（host.auth.list 的 name 字段）的 glob，列表里靠后的条目覆盖靠前的同名条目 / Ordered per-auth-file overrides. match is a glob against the auth file name (host.auth.list's name field); later entries override earlier ones for the same file."},
				{Name: "auths[].match", Type: pluginapi.ConfigFieldTypeString, Description: "匹配账号文件名的 glob 模式（*、?、[]） / Glob pattern (*, ?, []) matched against the auth file name."},
				{Name: "auths[].enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "覆盖匹配账号的 default.enabled / Overrides default.enabled for matching auth files."},
				{Name: "auths[].times", Type: pluginapi.ConfigFieldTypeArray, Description: "覆盖匹配账号的 default.times / Overrides default.times for matching auth files."},
				{Name: "auths[].model", Type: pluginapi.ConfigFieldTypeString, Description: "覆盖匹配账号所属 provider 的默认模型 / Overrides the provider's default model for matching auth files."},
				{Name: "auths[].reasoning-effort", Type: pluginapi.ConfigFieldTypeString, Description: "覆盖匹配账号所属 provider 的默认 reasoning-effort / Overrides the provider's default reasoning-effort for matching auth files."},
			},
		},
		Capabilities: map[string]bool{"usage_plugin": true, "management_api": true},
	}
}

func okEnvelope(result any) ([]byte, error) {
	return json.Marshal(struct {
		OK     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, result})
}

func errorEnvelope(code, message string) []byte {
	out, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": message}})
	return out
}
