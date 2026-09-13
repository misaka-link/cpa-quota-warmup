# cpa-quota-warmup

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
        - match: "antigravity-szxypy@gmail.com.json"
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
scripts/build.sh                 # -> dist/cpa-quota-warmup-v0.1.1.so (+ .sha256)
sudo ops/merge-config.py         # 原地合并默认配置到 /var/lib/cli-proxy-api/config.yaml（保 inode）
                                  # sudo ops/merge-config.py --remove 可移除
sudo ops/deploy                  # 安装 .so 到插件目录并重启 cli-proxy-api.service
```

`ops/merge-config.py` 写入的默认配置就是上面那份（含 `codex-*-prolite.json` 禁用那条）。修改前会先按插件 id 做正则查找替换已有块，重复执行是幂等的。

## 验证方法

```bash
# 1. 单元测试 + go vet
go vet ./... && go test ./...

# 2. 真 ABI 集成测试（不需要真实宿主/网络）
python3 integration_abi_test.py dist/cpa-quota-warmup-v0.1.1.so

# 3. 部署后看宿主日志（host.log 回调，前缀 [cpa-quota-warmup]）
journalctl -u cli-proxy-api | rg 'cpa-quota-warmup'

# 4. 管理面板状态路由（免鉴权）——各账号计划时间、下次触发、最近结果
curl -s http://127.0.0.1:8317/v0/resource/plugins/cpa-quota-warmup/status | jq .

# 5. 立即手动触发一轮（GET，见下方"宿主事实核实"里为什么不是 POST）
curl -s 'http://127.0.0.1:8317/v0/resource/plugins/cpa-quota-warmup/run?auth=antigravity-*' | jq .

# 6. cpa-usage-panel 里应该能看到这些请求（provider=对应 provider，model=配置的模型，
#    时间落在计划的 HH:MM 附近），确认预热请求确实打到了上游而不是本地短路
```

## 宿主事实核实结果（与任务原始假设的出入）

对照 CLIProxyAPI v7.2.158 源码（`$(go env GOMODCACHE)/github.com/router-for-me/!c!l!i!proxy!a!p!i/v7@v7.2.158`）核实，以下几点与最初给出的方案假设**不一致**，已按下述结论实现：

1. **`UsageRecord.SessionID` 不等于我们发的 `X-Session-ID` 原始值，而是带 `"header:"` 前缀。** `sdk/cliproxy/session/info.go` 的 `ExtractSessionInfo`（"5. OpenCode / Pi Slot / Task / Generic Headers"分支）执行 `info.SessionID = "header:" + sid`；这条路径经 `internal/pluginhost/adapters_usage_translation.go` 的 `usageAdapter.HandleUsage` 原样传给插件的 `UsageRecord.SessionID`。插件里所有按会话标记做覆盖核对的地方都已按 `"header:" + tag` 匹配（见 `usage.go` 的 `sessionHeaderRecordPrefix`），不是原方案假设的原始值直接相等。
2. **CPA 资源路由（`/v0/resource/plugins/<id>/...`）只接受 GET，POST 在宿主层就被拒绝。** `internal/pluginhost/management.go` 的 `ServeResourceHTTP` 开头即 `if !strings.EqualFold(r.Method, http.MethodGet) { return false }`——POST 请求根本不会构造 `ManagementRequest` 转发给插件，直接在 gin 路由层 404。要注册真正的 POST 路由，只能用 `Routes: []ManagementRoute{...}`（`/v0/management/` 前缀），但那一族路由**是要管理鉴权的**，与任务里"两个路由都免鉴权"的要求冲突。按最小合理假设，`run` 触发端点改成了 **GET**（`?auth=<glob>` 走 query string），继续挂在免鉴权的 resource 路由下；README 与代码注释（`management.go`）都记录了这个取舍。
3. **`host.model.execute` 确实不产生 usage 记录**，与任务给出的判断一致（`sdk/api/handlers/handlers_execution.go:205`，`InternalSource=true` 时跳过 usage 上报），已按此确认改用普通 HTTP 客户端。
4. **`POST /v1/chat/completions` 的路径、`max_tokens`、`reasoning_effort` 字段均按任务描述核实无误。** `internal/api/server_routes.go:66` 注册路由；`sdk/api/handlers/openai/openai_handlers.go:211-212` 显式读取并转发 `max_tokens`；`reasoning_effort` 由通用的 `internal/thinking/apply.go` 的 `extractOpenAIConfig` 解析（"OpenAI Chat Completions format" 的合法输入，`none/low/medium/high` 离散档位），不是本插件凭空加的字段，会被正常应用到匹配的模型/provider 上。实际是否被下游 codex 执行器采纳成 upstream 请求未做端到端验证（没有真实 codex 账号可测）。
5. **模型名的 provider 前缀语法（`provider/model` 或 `provider:model`）在 `/v1/chat/completions` 这条路径上不存在。** `ForcedProvider`（`sdk/api/handlers/model_execution.go`）只能通过插件内部的 `ProtocolExecutionRequest`/Gemini Interactions 的 `agent` 参数设置，普通客户端请求体的 `model` 字段没有任何解析出 provider 前缀的逻辑（`sdk/api/handlers/handlers_routing.go` 的 `providersForExecution` 只在 `execOptions.ForcedProvider` 已经非空时才用它，而这个值不会从请求体里派生）。因此本插件严格依赖"每个 provider 配置一个在该 provider 唯一存在的模型名"这一假设，与任务描述的兜底方案一致，未发现更好的替代方案。

## 已知限制

- 覆盖不保证 100%：round-robin 打散是"尽力而为"，`max-rounds` 用尽后放弃并只记警告，不会无限重试。
- `run` 只能是 GET（见上）。
- 手动触发（`run`）不写入按时间点的 `state.json`（不影响当天定时槽位的判定），只受每账号 60 秒节流保护，避免连点。
- 模型预检只确认 `GET /v1/models` 列出了这个名字，不代表这个模型这个账号一定能用（例如账号自身权限/额度问题仍可能在实际发送时报错）；预检本身失败（网络问题等）会被当作"跳过预检，照常发送"处理，不会阻塞正常预热。

## v0.1.1（线上手动触发实测后的修复）

1. **同一会话标签的多条 `usage.handle` 记录不再被遮蔽。** 线上实测：一条 codex 预热请求先在 `codex-92661722-szxypy@gmail.com-team.json` 上得到 429 `usage_limit_reached`，宿主随即在同一请求内重试到另一个账号成功——两条 `usage.handle` 记录共享同一个 `SessionID`。旧实现 `bySessionTag` 只返回最新一条、`waitForSessionCoverage` 找到一条就把该标记标记为"已处理"，于是 429 那条被吞掉，对应账号被误报"not covered"。现在 `usageRing.allBySessionTag` 返回该标记下的**全部**记录，`waitForSessionCoverage` 在整个窗口内持续收集、直到目标账号集合全部命中或超时才返回；失败记录一样算覆盖。见 `usage_test.go`/`runner_test.go` 里复现该场景的单测。
2. **默认模型名改成本机 `GET /v1/models` 实际暴露的那些**（antigravity `gemini-3.7-flash-high`、kimi `kimi-k2.8`、xai `grok-4.6`；codex/claude/gemini-cli/aistudio/vertex 不变），避免像 `gemini-3.1-flash-lite` 那样直接 404。
3. **加了模型预检**（见"机制"第 3 步）：配置漂移（模型改名/下线）时只会跳过并 warn，不会对着一个不存在的模型反复发请求。
4. `run` 手动触发现在也会像后台 tick 一样，给每个账号打一行 `host.log`（含预检跳过的情况），方便对着 journal 排查。
