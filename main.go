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
	pluginVersion = "0.3.0"
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
	overridesPath, err := overridesFilePath()
	if err != nil {
		return err
	}
	e := newEngine(cfg, newStateStore(path), newOverridesStore(overridesPath), hostAuthLister{})
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
		if cfg.legacyMode {
			hostLog("info", tr(logLanguage(cfg), msgLegacyFormatDetected))
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
			//
			// v0.3.0 reduced this list from ~22 field-by-field entries to just
			// the minimal-config surface (enabled/time/model/accounts) plus one
			// "advanced" entry enumerating every optional sub-key, per the
			// "配置太复杂、小白看不懂" simplification request. The legacy
			// default/providers/auths/timezone/... keys still parse exactly as
			// before (see config.go's legacyMode) but are intentionally no
			// longer advertised here -- new configs should use the form below.
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "为 false 时插件完全不预热任何账号 / When false, the plugin never warms up any account."},
				{Name: "time", Type: pluginapi.ConfigFieldTypeString, Description: "每天几点预热（24 小时制），可写多个，如 \"05:30, 10:30\"；也支持标准 5 段 cron 表达式，如 \"30 5,10,15,20 * * *\"；默认 \"05:30\" / What time(s) of day to warm up (24h), e.g. \"05:30, 10:30\"; also accepts a standard 5-field cron expression, e.g. \"30 5,10,15,20 * * *\". Default \"05:30\"."},
				{Name: "model", Type: pluginapi.ConfigFieldTypeString, Description: "预热用的模型，默认 \"auto\"（按各 provider 自动选最便宜的可用模型）；也可以直接写模型名，或写成 {provider: model} 的映射 / The model to warm up with. Default \"auto\" (picks the cheapest available model per provider automatically); may also be a literal model name, or a {provider: model} mapping."},
				{Name: "accounts", Type: pluginapi.ConfigFieldTypeArray, Description: "要预热的认证文件名，支持 * 通配；写 \"*\" 表示全部账号；缺省/空表示不预热任何账号；列表项也可以写成 {match, time, model} 对象，为单个账号单独设置时间/模型 / Auth file names to warm up (glob patterns allowed); \"*\" means every account; empty/omitted means nothing is warmed up. List items may also be {match, time, model} objects to override time/model for one account."},
				{Name: "advanced", Type: pluginapi.ConfigFieldTypeObject, Description: "高级设置，一般不用改，全部可选：timezone（默认跟随宿主进程本地时区）、base-url（默认 http://127.0.0.1:8317）、api-key（默认读取宿主 config.yaml 的 api-keys[0]）、message（默认 \"hi\"）、max-tokens（默认 16）、max-rounds（默认 3）、catch-up-minutes（默认 60）、language（默认 auto）、log（默认 true）、models（按 provider 覆盖模型，等价于顶层 model 的映射写法） / Advanced settings, all optional and rarely needed: timezone (defaults to the host process's local timezone), base-url (default http://127.0.0.1:8317), api-key (default: api-keys[0] from the host's own config.yaml), message (default \"hi\"), max-tokens (default 16), max-rounds (default 3), catch-up-minutes (default 60), language (default auto), log (default true), models (per-provider model override, equivalent to the top-level model's mapping form)."},
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
