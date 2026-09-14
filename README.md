# cpa-quota-warmup

## English summary

`cpa-quota-warmup` is a native plugin (Go, cgo `c-shared`) for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA). For each configured auth file, at a scheduled time of day (default 05:30 Asia/Shanghai, overridable per file), it sends a tiny chat-completion request (`"hi"`, `max_tokens: 16`) using that account's provider's cheapest model, to warm up the account's 5-hour quota window before the first real request of the day hits it cold. It needs no credentials of its own: it authenticates as an ordinary client using CPA's own configured `api-keys`.

### Install

- **From a release**: download `cpa-quota-warmup-v<ver>.so` from this repository's [Releases](https://github.com/szxypi/cpa-quota-warmup/releases) page and place it under `<plugins.dir>/linux/amd64/` in your CPA installation (CPA derives the plugin id from the file name itself, not from any metadata field). Add a `plugins.configs.cpa-quota-warmup` block to CPA's `config.yaml` (minimal example below), then restart CPA.
- **From source**: `CGO_ENABLED=1 scripts/build.sh` (requires Go 1.26+). This plugin is built against `github.com/router-for-me/CLIProxyAPI/v7` SDK `v7.2.158` (see `go.mod`) and targets the CPA `7.2.15x` host line.
- `ops/deploy` and `ops/merge-config.py` are convenience scripts written for the maintainer's own local systemd deployment (they assume a `cli-proxy-api.service` and `/var/lib/cli-proxy-api/...` paths); treat them as examples and adjust the paths for your own setup, or configure/install by hand instead.

Minimal config:

```yaml
plugins:
  configs:
    cpa-quota-warmup:
      enabled: true
      default:
        enabled: true
        times: ["05:30"]
      providers:
        antigravity: { model: "gemini-3.7-flash-high" }
```

Status/config panel (self-contained HTML, no external assets, follows the CPA Management Center's own theme and language): `GET /v0/resource/plugins/cpa-quota-warmup/panel`.

**Limitation, in one sentence**: the plugin cannot pin a request to a specific account (CPA gives plugins no such hook), so it can only fan requests out round-robin and reconcile coverage after the fact via usage records -- see "覆盖策略的局限" below for the full explanation (in Chinese; the rest of this document is Chinese-first).

---

CLIProxyAPI (CPA) 原生插件（Go c-shared 库）。每天在配置的时间点（默认每个认证文件 05:30 Asia/Shanghai，可按认证文件单独覆盖），用该账号所属 provider 里最便宜的模型给它发一条极短消息（默认 `"hi"`，`max_tokens: 16`），预热该账号的 5 小时额度窗口，避免第一次真实请求撞上"冷启动"配额检查。

插件本身**不需要，也没有**任何私有凭据或密钥：它就是本机 CPA 的一个普通客户端，用 `api-keys[0]`（或显式配置的 `api-key`）向自己的 `/v1/chat/completions` 发请求。

## 机制

1. 后台每 30 秒 tick 一次。每次 tick 都重新调用 `host.auth.list` 拿最新认证文件列表（增删账号即时生效），对每个未 disabled/unavailable 的账号解析出"有效配置"（`default:` → `providers.<provider>:` → `auths[]` 按顺序覆盖，见下）。
2. 对每个账号的每个 `HH:MM` 时间点，若 `now` 已经过了这个点、且 `now - 该点 <= catch-up-minutes`、且状态文件里还没有今天这个 `(账号, 日期, HH:MM)` 的记录 → 判定为"到期"。
3. **模型预检**：真正发请求前，用同一 api-key 对本机 `GET /v1/models` 取一次 `data[].id` 集合。到期账号里模型不在这个集合中的，直接跳过（不发请求），记 `Warning: "model X not exposed by this CPA instance (see GET /v1/models)"` 并持久化/`warn` 日志；这次 `GET /v1/models` 本身失败（网络问题等）就跳过预检、照常发送，不能因为预检失败反而拦住本该发出去的请求。
4. 到期且通过预检的账号按 provider 分组。每组内，"顺序"（不是并发）发送 N 条请求（N = 该组待预热账号数），每条请求带一个全新随机的 `X-Session-ID` 头；在最多 3 秒的窗口内持续收集这一轮所有请求标记下的**全部** `usage.handle` 记录（不是只看第一条），直到组内每个目标账号的 `AuthID` 都出现过、或者窗口到期。**同一个 `X-Session-ID` 标记下可能对应不止一条 usage 记录**：宿主可能在同一个客户端请求内部先打到 A 账号 429，再重试打到 B 账号成功，两条记录共享同一个 `SessionID`——只看"这个标记下最新一条"会把 A 账号的 429 直接吞掉。失败（429/其他）的记录照样算"覆盖"，账号的额度检查窗口已经被真实触碰过。仍未覆盖的账号进入下一轮，最多 `max-rounds`（默认 3）轮；轮次用尽仍未覆盖的账号记一条 `warn` 日志，但**这一天这个时间点视为已处理**，不会在同一天的 catch-up 窗口内反复重试。
5. 结果（是否覆盖、用了几轮、状态码、警告信息）持久化到 `<CPA 工作目录>/quota-warmup/state.json`（原子写：临时文件 + `rename`，这是插件自己的文件，不是 `config.yaml`，可以放心用 rename），只保留最近 7 天。

## 为什么不用 `host.model.execute`

`host.model.execute` 回调执行时 `InternalSource=true`，宿主对 `InternalSource=true` 的请求**不生成 usage 记录**（`sdk/api/handlers/handlers_execution.go:205`，已对照 v7.2.158 源码核实）。没有 usage 记录就无法知道这次预热请求到底落在了哪个账号上——而"确认预热覆盖到了目标账号"正是本插件存在的意义。所以插件改为像任何普通客户端一样，用 `net/http` 向本机 CPA 发一个真实的 `POST /v1/chat/completions`，走正常的 usage 上报链路。

## 覆盖策略的局限

**插件无法从 API 层面指定"这次请求必须用哪个账号"**（`pinned_auth_id` 只能通过宿主内部 ctx 注入，插件够不到）。所以覆盖账号的思路是"打散 + 事后核对"：

- 每条请求带一个全新的 `X-Session-ID`，让宿主的会话粘滞（session-affinity）机制把它当成一个全新会话，从而回落到宿主的 round-robin 选择器（对 antigravity 上的 `gemini-*`，本机还装了 `cpa-affinity-router` 插件接管选号，其 tie-break 策略是选最久未被选中的账号，顺序发多条一样能轮到每个账号）。
- 但"回落到 round-robin"不等于"保证轮询到每一个账号"——如果某个账号连续被选中好几次，同一组里的其他账号就要等到下一轮才可能被覆盖。`max-rounds` 就是为这种情况兜底的重试预算，用尽仍未覆盖的账号只能放弃并记警告，**不保证 100% 覆盖**，只是尽力而为。
- 覆盖核对优先按 `UsageRecord.SessionID` 精确匹配我们发的会话标记（见下面"核实结果"里 `header:` 前缀的坑）；如果宿主某个版本不再保留这个值，会退化成"发起时间窗口 ± 2s + 模型名"的宽松匹配。

## 配置

键 `plugins.configs.cpa-quota-warmup`：

```yaml
plugins:
  configs:
    cpa-quota-warmup:
      enabled: true
      priority: 1
      log: true
      timezone: "Asia/Shanghai"
      base-url: "http://127.0.0.1:8317"
      api-key: ""              # 留空时从 cwd 的 config.yaml 读 api-keys[0]
      message: "hi"
      max-tokens: 16
      max-rounds: 3
      catch-up-minutes: 60
      default:
        enabled: true
        times: ["05:30"]
      providers:
        antigravity: { model: "gemini-3.7-flash-high" }
        codex:       { model: "gpt-5.6-luna", reasoning-effort: "low" }
        kimi:        { model: "kimi-k2.8" }
        xai:         { model: "grok-4.6" }
        claude:      { model: "claude-haiku-4-5-20251001" }
        gemini-cli:  { model: "gemini-2.5-flash-lite" }
        aistudio:    { model: "gemini-2.5-flash-lite" }
        vertex:      { model: "gemini-2.5-flash-lite" }
      auths:
        - match: "codex-*-prolite.json"
          enabled: false
        - match: "antigravity-alice@example.com.json"
          times: ["05:30", "10:35"]
          model: "gemini-3.7-flash-high"
```

- `default:` 是应用到每个账号的基线；`providers.<provider>.model/reasoning-effort` 按账号的 provider 给出默认模型；`auths:` 按账号文件名（`host.auth.list` 的 `name` 字段）用 glob（`*`、`?`、`[]`）匹配，**列表里靠后的条目覆盖靠前的**（同一账号被多条命中时）。
- 账号的 provider 不在 `providers:` 里、且该账号自己也没有 `auths[].model` 覆盖 → 跳过并打一条 `warn` 日志。
- **`providers:` 里的模型名必须是这台 CPA 实例 `GET /v1/models` 实际列出的那个**，不是任意合法模型名就行——线上 2026-09-13 实测 antigravity 只暴露 `gemini-3.7-flash-high`/`gemini-3.8-flash-high`（`gemini-3.1-flash-lite` 直接 404 `model_not_found`），kimi 只有 `kimi-k2.8`/`kimi-k2.8-code`/`kimi-k3-256k`，xai 只有 `grok-4.6`。上面这份默认值已按这次实测更新；出问题时先 `curl -s -H "Authorization: Bearer <key>" http://127.0.0.1:8317/v1/models | jq '.data[].id'` 核对一遍，不要假设。插件自己也会在每次 tick/`run` 前做同样的预检（见"机制"第 3 步），配错了不会 404，只会跳过并 warn。
- `catch-up-minutes`：服务重启或该时间点被跳过后，仍在这个窗口内可以补发；超过就当天跳过，等下一次配置的时间点。
- `api-key` 留空时，插件从**当前工作目录**（CPA 的 cwd，即 `/var/lib/cli-proxy-api`）下的 `config.yaml` 里只读 `api-keys` 这一个键的第一个值，不解析其余内容。

## 部署

```bash
cd ~/cpa-plugins/cpa-quota-warmup
scripts/build.sh                 # -> dist/cpa-quota-warmup-v0.2.1.so (+ .sha256)
sudo ops/merge-config.py         # 原地合并默认配置到 /var/lib/cli-proxy-api/config.yaml（保 inode）
                                  # sudo ops/merge-config.py --remove 可移除
sudo ops/deploy                  # 安装 .so 到插件目录并重启 cli-proxy-api.service
```

`ops/merge-config.py` 写入的默认配置就是上面那份（含 `codex-*-prolite.json` 禁用那条）。修改前会先按插件 id 做正则查找替换已有块，重复执行是幂等的。

**`ops/deploy` 与 `ops/merge-config.py` 是针对维护者本机 systemd 部署（`cli-proxy-api.service`、`/var/lib/cli-proxy-api/...`）写的辅助脚本**，不是通用安装程序；换一套部署方式（Docker、不同路径、不同服务名等）时请照着改脚本里的路径/服务名，或者干脆手动完成"放 `.so`、改 `config.yaml`、重启 CPA"这三步。

## 验证方法

```bash
# 1. 单元测试 + go vet
go vet ./... && go test ./...

# 2. 真 ABI 集成测试（不需要真实宿主/网络）
python3 integration_abi_test.py dist/cpa-quota-warmup-v0.2.1.so

# 3. 部署后看宿主日志（host.log 回调，前缀 [cpa-quota-warmup]）
journalctl -u cli-proxy-api | rg 'cpa-quota-warmup'

# 4. 状态页面（HTML，免鉴权，管理中心菜单里也能进），跟随管理中心的语言与主题
open http://127.0.0.1:8317/v0/resource/plugins/cpa-quota-warmup/panel

# 5. 状态 JSON（页面本身用的数据源；?lang= 可强制语言，不传则按 Accept-Language/LANG 协商）
curl -s 'http://127.0.0.1:8317/v0/resource/plugins/cpa-quota-warmup/status?lang=ru' | jq .

# 6. 立即手动触发一轮（GET，见下方"宿主事实核实"里为什么不是 POST）
curl -s 'http://127.0.0.1:8317/v0/resource/plugins/cpa-quota-warmup/run?auth=antigravity-*' | jq .

# 7. cpa-usage-panel 里应该能看到这些请求（provider=对应 provider，model=配置的模型，
#    时间落在计划的 HH:MM 附近），确认预热请求确实打到了上游而不是本地短路
```

## 宿主事实核实结果（与任务原始假设的出入）

对照 CLIProxyAPI v7.2.158 源码（`$(go env GOMODCACHE)/github.com/router-for-me/!c!l!i!proxy!a!p!i/v7@v7.2.158`）核实，以下几点与最初给出的方案假设**不一致**，已按下述结论实现：

1. **`UsageRecord.SessionID` 不等于我们发的 `X-Session-ID` 原始值，而是带 `"header:"` 前缀。** `sdk/cliproxy/session/info.go` 的 `ExtractSessionInfo`（"5. OpenCode / Pi Slot / Task / Generic Headers"分支）执行 `info.SessionID = "header:" + sid`；这条路径经 `internal/pluginhost/adapters_usage_translation.go` 的 `usageAdapter.HandleUsage` 原样传给插件的 `UsageRecord.SessionID`。插件里所有按会话标记做覆盖核对的地方都已按 `"header:" + tag` 匹配（见 `usage.go` 的 `sessionHeaderRecordPrefix`），不是原方案假设的原始值直接相等。
2. **CPA 资源路由（`/v0/resource/plugins/<id>/...`）只接受 GET，POST 在宿主层就被拒绝。** `internal/pluginhost/management.go` 的 `ServeResourceHTTP` 开头即 `if !strings.EqualFold(r.Method, http.MethodGet) { return false }`——POST 请求根本不会构造 `ManagementRequest` 转发给插件，直接在 gin 路由层 404。要注册真正的 POST 路由，只能用 `Routes: []ManagementRoute{...}`（`/v0/management/` 前缀），但那一族路由**是要管理鉴权的**，与任务里"两个路由都免鉴权"的要求冲突。按最小合理假设，`run` 触发端点改成了 **GET**（`?auth=<glob>` 走 query string），继续挂在免鉴权的 resource 路由下；README 与代码注释（`management.go`）都记录了这个取舍。
3. **`host.model.execute` 确实不产生 usage 记录**，与任务给出的判断一致（`sdk/api/handlers/handlers_execution.go:205`，`InternalSource=true` 时跳过 usage 上报），已按此确认改用普通 HTTP 客户端。
4. **`POST /v1/chat/completions` 的路径、`max_tokens`、`reasoning_effort` 字段均按任务描述核实无误。** `internal/api/server_routes.go:66` 注册路由；`sdk/api/handlers/openai/openai_handlers.go:211-212` 显式读取并转发 `max_tokens`；`reasoning_effort` 由通用的 `internal/thinking/apply.go` 的 `extractOpenAIConfig` 解析（"OpenAI Chat Completions format" 的合法输入，`none/low/medium/high` 离散档位），不是本插件凭空加的字段，会被正常应用到匹配的模型/provider 上。实际是否被下游 codex 执行器采纳成 upstream 请求未做端到端验证（没有真实 codex 账号可测）。
5. **模型名的 provider 前缀语法（`provider/model` 或 `provider:model`）在 `/v1/chat/completions` 这条路径上不存在。** `ForcedProvider`（`sdk/api/handlers/model_execution.go`）只能通过插件内部的 `ProtocolExecutionRequest`/Gemini Interactions 的 `agent` 参数设置，普通客户端请求体的 `model` 字段没有任何解析出 provider 前缀的逻辑（`sdk/api/handlers/handlers_routing.go` 的 `providersForExecution` 只在 `execOptions.ForcedProvider` 已经非空时才用它，而这个值不会从请求体里派生）。因此本插件严格依赖"每个 provider 配置一个在该 provider 唯一存在的模型名"这一假设，与任务描述的兜底方案一致，未发现更好的替代方案。
6. **`pluginapi.ManagementRequest` 确实带 `Query`（`url.Values`）与 `Headers`（`http.Header`）字段**，且 `internal/pluginhost/management.go` 的 `ServeResourceHTTP` 在构造它时会把真实请求的 query string 和请求头原样克隆进去（`cloneHeader(r.Header)` / `cloneValues(r.URL.Query())`）。所以 `status`/`run`/`panel` 三个路由都能直接读到 `?lang=` 和 `Accept-Language`，不需要"只能退化到路径/查询串"那种更弱的方案。
7. **`cli-proxy-language` 的实际存储格式**：对照一份真实的 `/var/lib/cli-proxy-api/static/management.html` 构建反查得到的函数（对应变量名 `ll`/`ul`/`dl`/`cl`/`sl`/`ol`），确认它是 zustand `persist` 中间件写的，取值可能是 `{"state":{"language":"zh-CN"},...}` 这种信封、也可能是裸 JSON 字符串 `"zh-CN"`、极端情况下甚至是完全不带引号的裸字符串——三种都要兼容解析（本插件页面里的 `parseStored()` 照此实现）；`navigator.language` 兜底规则是 `zh-tw`/`zh-hk`/`zh-mo`/`zh-hant` 前缀 → `zh-TW`，`zh*` → `zh-CN`，`ru*` → `ru`，其余 → `en`，与任务描述完全一致，已按真实源码核实（不是假设）。

## 国际化（i18n）

插件跟随 CPA 管理中心（Management Center）的语言，覆盖 host.log 全部文本、`status`/`run` JSON 里的人类可读字段（`Skipped` 原因、`Warning`）、以及状态页面本身，支持 `zh-CN`（简体）、`zh-TW`（繁体）、`en`、`ru` 四种，与管理中心 i18next 支持的语言完全一致（`Vc="cli-proxy-language"`、`Hc=["zh-CN","zh-TW","en","ru"]`，对照真实 `management.html` 构建核实）。

- **消息目录**：`i18n.go` 里 `messagesEN/messagesZhCN/messagesZhTW/messagesRU` 四张表，key 用 `msgKey` 常量化；`tr(lang, key, args...)` 查表格式化，缺失语言/缺失 key 都回退英文，实在没有则回退裸 key 字符串（不会 panic）。四张表的 key 集合一致性有单测保证（`TestMessageCatalogsHaveTheSameKeys`）。ru 是认真写的技术俄语，不是机翻占位（含一处刻意简化：轮次计数的复数变格统一用 "раундов"，不做俄语 one/few/many 全套规则）。
- **服务端语言选择**：配置新增 `language: "auto" | "zh-CN" | "zh-TW" | "en" | "ru"`（默认 `auto`）。
  - `status`/`run`（有请求上下文）优先级：`?lang=` 查询参数 > `language` 配置（非 `auto` 时）> `Accept-Language` 头（取权重最高的一个 tag）> 环境变量 `LC_ALL`/`LANG`（`zh_CN.UTF-8` → `zh-CN`，`C`/`POSIX`/裸 `C.UTF-8` 视为未设置）> 兜底 `zh-CN`。**`?lang=` 优先级高于 `language` 配置是本插件自己的取舍**：任务原文只给出了 "auto 规则" 的链条，没说清楚 pin 死的 `language` 配置与单次请求的 `?lang=` 谁优先；这里选择让 `?lang=` 总是赢，因为状态页面自己每次都会带着访客浏览器实际检测到的语言发 `?lang=`，如果配置更优先，管理员一旦全局 pin 了语言，页面就没法再跟着访客自己的语言走了。
  - `host.log`（没有请求上下文）：`language` 配置（非 `auto` 时）> `LC_ALL`/`LANG` > 兜底 `zh-CN`。
  - `status`/`run` 的 JSON 响应都带 `"lang"` 字段说明实际用的是哪种。
- **状态页面**：`GET /v0/resource/plugins/cpa-quota-warmup/panel`（管理中心菜单会显示"配额预热 / Quota Warmup"），自包含 HTML（内联 CSS/JS，无外链），登记簿/表格风格（配置摘要用两列表格，账号列表与最近记录都是纯表格，没有卡片套卡片、没有渐变、没有 emoji），字体用系统字体栈，深浅色主题都跟随管理中心的 `cli-proxy-theme`（做法照抄 `cpa-usage-panel/panel.html`）。页面 JS 读 `localStorage['cli-proxy-language']`（同样支持 zustand-persist 的 JSON 信封或裸字符串两种存法），缺失时按 `navigator.language`（`zh-tw`/`zh-hk`/`zh-mo`/`zh-hant` 前缀 → `zh-TW`，`zh*` → `zh-CN`，`ru*` → `ru`，其余 → `en`）——这套判定逻辑对照真实 `management.html` 构建里的 `ol/sl/cl/ll/ul/dl` 几个函数核实过，不是拍脑袋写的。页面自己的文案（标题、表头、按钮等）复用 Go 端同一份消息目录，整份序列化成 JSON 注入页面（`window` 里一个 `<script type="application/json">`），不维护第二份翻译。
- `?auth=<glob>` 的"立即预热"按钮直接调 `/run`，结果就地渲染在按钮下方，不跳转页面。

## 已知限制

- 覆盖不保证 100%：round-robin 打散是"尽力而为"，`max-rounds` 用尽后放弃并只记警告，不会无限重试。
- `run` 只能是 GET（见上）。
- 手动触发（`run`）不写入按时间点的 `state.json`（不影响当天定时槽位的判定），只受每账号 60 秒节流保护，避免连点。
- 模型预检只确认 `GET /v1/models` 列出了这个名字，不代表这个模型这个账号一定能用（例如账号自身权限/额度问题仍可能在实际发送时报错）；预检本身失败（网络问题等）会被当作"跳过预检，照常发送"处理，不会阻塞正常预热。
- 状态页面首次渲染用服务端猜的语言（`?lang=`/`Accept-Language`/`LANG` 那条链，没有访问 localStorage 的能力），JS 加载后立即按 `cli-proxy-language`/`navigator.language` 校正；两者通常一致（浏览器语言与 `Accept-Language` 头本来就同源），但理论上 JS 执行前有极短暂的窗口可能显示了另一种语言的静态文案。
- `language` 配置项写了非法值（既不是 `auto` 也不是四种语言之一）会静默回退成 `auto`，不会让 `plugin.register`/`reconfigure` 失败；没有额外的 `warn` 日志（这一点与其他配置校验不完全一致，属于本次改动里对"不要因为一个拼写错误就整个不生效"的取舍）。

## v0.1.1（线上手动触发实测后的修复）

1. **同一会话标签的多条 `usage.handle` 记录不再被遮蔽。** 线上实测：一条 codex 预热请求先在其中一个 team 账号上得到 429 `usage_limit_reached`，宿主随即在同一请求内重试到另一个 team 账号成功——两条 `usage.handle` 记录共享同一个 `SessionID`。旧实现 `bySessionTag` 只返回最新一条、`waitForSessionCoverage` 找到一条就把该标记标记为"已处理"，于是 429 那条被吞掉，对应账号被误报"not covered"。现在 `usageRing.allBySessionTag` 返回该标记下的**全部**记录，`waitForSessionCoverage` 在整个窗口内持续收集、直到目标账号集合全部命中或超时才返回；失败记录一样算覆盖。见 `usage_test.go`/`runner_test.go` 里复现该场景的单测。
2. **默认模型名改成本机 `GET /v1/models` 实际暴露的那些**（antigravity `gemini-3.7-flash-high`、kimi `kimi-k2.8`、xai `grok-4.6`；codex/claude/gemini-cli/aistudio/vertex 不变），避免像 `gemini-3.1-flash-lite` 那样直接 404。
3. **加了模型预检**（见"机制"第 3 步）：配置漂移（模型改名/下线）时只会跳过并 warn，不会对着一个不存在的模型反复发请求。
4. `run` 手动触发现在也会像后台 tick 一样，给每个账号打一行 `host.log`（含预检跳过的情况），方便对着 journal 排查。

## v0.2.0（i18n：跟随 CPA 语言）

1. 新增消息目录 `i18n.go`（`zh-CN`/`zh-TW`/`en`/`ru`），覆盖所有 `hostLog` 文本、`roundOutcome.Warning`、`manualRunResult.Skipped` 的原因、`status`/`run` JSON 里的人类可读字段；`status`/`run` 响应新增 `"lang"` 字段。
2. 新增配置项 `language`（默认 `auto`），语言协商规则见上面"国际化（i18n）"一节。
3. 新增状态页面 `GET /v0/resource/plugins/cpa-quota-warmup/panel`（自包含 HTML，登记簿/表格风格，跟随管理中心主题与语言），管理中心菜单条目挪到这个页面上（`status`/`run` 不再带菜单标签，纯数据/动作端点）。
4. `ConfigFields` 的 `Description` 从纯英文改成"中文 / English"双语一句话（注册时是静态字符串，无法跟随访客浏览器语言）。
5. **实现过程中用真实单元测试挖出一个真 bug并修复**：本机 `LANG=C.UTF-8`，`languageFromEnv` 最初把编码后缀（`.UTF-8`）剥离的顺序放在了"是不是 `C`/`POSIX`"判断之后，导致 `C.UTF-8` 被误判成英语而不是"未设置"。已把剥离顺序调整到判断之前，并补了 `TestLanguageFromEnv` 里 `C.UTF-8`/`POSIX.UTF-8` 两个用例锁定这个修复。
6. 单元测试新增：语言协商全链路（query/Accept-Language/LANG/兜底，含优先级）、`tr()` 缺失语言/缺失 key 回退、四语言消息表 key 集合一致性、`status`/`run` 响应的 `lang` 字段与 `?lang=` 覆盖、`language` 配置项的规范化、面板 HTML 渲染（无残留占位符、各语言标题正确、未知语言回退英文）。ABI 集成测试新增：`management.register` 现在有 3 个资源路由（`panel`/`status`/`run`，只有 `panel` 带菜单）、`GET .../panel` 返回 `text/html` 且包含 `cli-proxy-language`/`cli-proxy-theme`、`status`/`run` 的 `?lang=` 生效。

## v0.2.1（修复：状态页静态文案不跟随 cli-proxy-language）

**线上实测发现的真 bug**：用 agent-browser 打开 `/panel`、在 `localStorage` 设好 `cli-proxy-language=zh-CN` 并刷新后，`document.documentElement.lang`、配置表行标签、账号状态列都已经是中文（这些走的是页面 JS 里动态渲染路径，本来就调用 `t()`），但标题、副标题、"Configuration"/"Refresh"/"Accounts" 等区块标题、表头（NAME/PROVIDER/MODEL/...）、输入框占位符、页脚这些**静态**文案仍然是英文。

**根因**：这些 `{{ui_*}}` 只在服务端首屏渲染时按 `Accept-Language`（无头浏览器发的是它自己的 OS/CLI 默认值，不是 `cli-proxy-language` 里存的那个）替换过一次；页面自己的 JS 算出真实语言后，只把 `t()` 用在了"以后动态渲染"的节点（配置表、账号表、最近记录、手动执行结果），从没回头重刷这批已经写死在 HTML 里的静态文案。

**修法**：模板里每一处渲染 `{{ui_*}}` 的元素都补上 `data-i18n="<key>"`（`<input>` 的 `placeholder` 用 `data-i18n-placeholder="<key>"`，`<title>` 同样处理）；页面 JS 在算出真实 `lang`/`dict` 之后，紧接着遍历 `[data-i18n]`/`[data-i18n-placeholder]` 用 `t(key)` 覆盖 `textContent`/`placeholder`，覆盖掉服务端首屏猜错的语言。服务端首屏替换本身保留（避免真的猜对时出现一次可见的重排/闪烁）。

新增单测 `TestPanelTemplateStaticTextHasDataI18nAttributes`：对模板里每一个 `{{ui_*}}` 占位符出现的位置（不是去重后的 key 集合——`ui_page_title`/`ui_col_provider`/`ui_col_model`/`ui_loading` 等 key 在模板里各出现不止一次，分别在 `<title>`/`<h1>`、两张表各自的表头等不同元素上），定位其所在的最近外层标签，断言标签内必须有匹配的 `data-i18n`/`data-i18n-placeholder` 属性；用"故意去掉 `<h1>` 上的属性、保留 `<title>` 上的"的方式手动验证过这条测试确实会 fail（而不是一个形同虚设的集合子集检查）。ABI 集成测试补了两条：面板 HTML 里 `data-i18n="ui_page_title"` 与 `data-i18n-placeholder=` 都存在。
