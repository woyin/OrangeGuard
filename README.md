# OrangeGuard

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（cpa）插件，做两件事：

1. **防降级（Guard）**：检测上游偷偷换模型——请求 `gpt-6-astra`，上游却用 `gpt-5.5-mini` 处理并返回 HTTP 200。检测到后自动重试，仍被换就返回明确的错误，**绝不把被替换的模型输出交给客户端**。
2. **合并模型（Virtual Models）**：把模型 A、B… 合并成一个新名字 C。请求 C 时按策略（`fallback` / `round-robin` / `random` / `weighted`）调度到成员模型；成员额度耗尽、被限流、被降级时进入**冷却期**，冷却期内直接跳过，不再反复请求。C 可以声明自己的能力（上下文长度、最大输出、多模态、thinking 等），会出现在 `/v1/models` 里，Agent 能正常识别。

两个功能共用同一套检测与冷却逻辑：虚拟模型的成员同样可以开启防降级，被降级的成员会被冷却并切到下一个成员。

## 工作原理

插件同时注册四种能力：

| 能力 | 作用 |
|---|---|
| `model_router` | 只认领配置里的虚拟模型和受保护模型（`TargetKind=self`），其它请求原样交还 cpa，零干预 |
| `executor` | 掌控执行循环：通过 `host.model.execute` / `execute_stream` 让 cpa 去请求成员模型（复用 cpa 的凭据、alias、代理、日志、用量统计），检查响应，决定接受 / 重试 / 切换 |
| `model_registrar` | 把虚拟模型及其能力注册进 cpa 模型表，出现在 `/v1/models`（OpenAI / Claude / Gemini 各格式） |
| `management_api` | 提供冷却状态查询与手动清除接口 |

```
客户端 ── model=C ──▶ cpa ──model.route──▶ orangeguard（认领 C）
                                           │  计划：按策略排序成员，跳过冷却中的成员
                                           ├─▶ host.model.execute(A) ─▶ 429 额度用完 → A 冷却 30min → 下一个
                                           ├─▶ host.model.execute(B) ─▶ 200，但 model=b-mini → B 冷却 10min → 下一个
                                           └─▶ host.model.execute(D) ─▶ 200，model=D ✔ 返回给客户端
```

**流式请求**：在判定通过前不向客户端发送任何字节——先缓冲到能读出处理模型的那个事件（OpenAI 的第一个 chunk、Claude 的 `message_start`，通常几百字节），通过才放行；判定失败就关闭上游流、切换或重试，客户端看不到半个错误模型的响应，重试成本也很低。已经开始向客户端输出之后再出错，就无法再切换，只能以错误结束该流。

**递归防护**：cpa 在插件发起的 `host.model.*` 嵌套调用中会跳过发起方插件自己的 router；插件额外带一个 `X-Orangeguard-Bypass` 头并设置并发熔断，作为双保险。

## 安装

需要带插件支持（CGO 构建）的 cpa，验证：

```bash
curl -s -o /dev/null -D - http://127.0.0.1:8317/v0/management/ | grep -i x-cpa-support-plugin
# X-Cpa-Support-Plugin: 1
```

构建并安装（需要 Go 1.26+ 与 C 编译器）：

```bash
make test       # 单元测试
make build      # 产出 dist/<goos>/<goarch>/orangeguard.<so|dylib|dll>
make install    # 复制到 ~/.cli-proxy-api/plugins/<goos>/<goarch>/（用 INSTALL_DIR= 覆盖，须与 plugins.dir 一致）
```

插件 ID 取自文件名，所以动态库必须叫 `orangeguard.so`（或 `.dylib` / `.dll`），配置键为 `plugins.configs.orangeguard`。推送 `v*` tag 会通过 Release workflow 构建各平台的插件商店格式 zip。

## 配置

完整带注释的示例见 [`config.example.yaml`](config.example.yaml)。最小示例：

```yaml
plugins:
  enabled: true
  dir: "~/.cli-proxy-api/plugins"
  configs:
    orangeguard:
      enabled: true
      priority: 50
      guard:
        models:
          - model: "gpt-6-astra"          # 返回的 model 必须是 gpt-6-astra（或其日期快照）
      virtual_models:
        - name: "astra-auto"
          strategy: fallback
          guard: true                      # 所有成员都做防降级检查
          members:
            - model: "gpt-6-astra"
            - model: "claude-opus-5"
          capabilities:
            context_length: 200000
            max_output_tokens: 32000
            vision: true
```

配置热更新：修改 cpa 配置后 cpa 会调用 `plugin.reconfigure`，插件原子替换配置，**冷却状态保留**。无效条目会被丢弃并在 cpa 日志中给出 `orangeguard: config:` 警告，不会让整个插件失效。

### 防降级 `guard`

| 字段 | 默认 | 说明 |
|---|---|---|
| `max_retries` | `3` | 直接请求受保护模型时，检测到被替换后在同一模型上额外重试的次数 |
| `retry_delay_ms` | `200` | 重试间隔 |
| `on_missing_model` | `accept` | 响应里没有模型字段时：`accept` 放行（fail-open），`reject` 视为被替换 |
| `models[].model` | — | 客户端请求的模型名，支持 `*` `?` 通配 |
| `models[].expect` | 请求的模型名 | 可接受的处理模型（字符串或列表，支持通配）。别名与上游名毫无字面关系时才需要写 |
| `models[].deny` | — | 一律拒绝的处理模型（通配），优先级高于 `expect`，例如 `["*mini*", "*flash*"]` |
| `models[].max_retries` | 全局值 | 覆盖重试次数 |

**匹配规则**（不含通配时；大小写不敏感）：

| 期望 | 实际 | 结果 | 说明 |
|---|---|---|---|
| `gpt-6-astra` | `gpt-6-astra` | ✅ | 相等 |
| `gpt-6-astra` | `gpt-6-astra-2026-08-01` | ✅ | 日期/快照后缀（至少 3 位数字，或 `latest`/`preview` 等） |
| `gpt-6-astra` | `openai/gpt-6-astra` | ✅ | 上游加了厂商前缀 |
| `my-glm-5.2` | `glm-5.2` | ✅ | cpa alias 前缀被剥离 |
| `gpt-6-astra` | `gpt-5.5-mini` | ❌ | 降级 |
| `gpt-6-astra` | `gpt-6-astra-mini` | ❌ | 后缀是档位名而不是版本 |
| `gpt-5` | `gpt-5.5` | ❌ | `.` 引入的是另一个小版本 |
| `claude-opus-4` | `claude-opus-4-1` | ❌ | 短数字后缀视为另一个版本 |
| `glm-5.2` | `glm-5.21` | ❌ | 没有分隔边界 |

处理模型的读取位置：OpenAI 顶层 `model`、Claude `model` / `message_start.message.model`、OpenAI Responses `response.model`、Gemini `modelVersion`。读取的是 cpa 翻译后返回给客户端格式的响应。

**上游真实错误**（4xx/5xx）在直接请求受保护模型时**原样透传**，不消耗重试预算（但会记入冷却表，供虚拟模型使用）。

### 虚拟模型 `virtual_models`

| 字段 | 默认 | 说明 |
|---|---|---|
| `name` | — | 对外的模型名 C |
| `strategy` | `fallback` | `fallback` 按顺序；`round-robin` 轮询起点；`random` 随机顺序；`weighted` 按 `weight` 加权选第一个，其余作为后备 |
| `members[].model` | — | 成员模型（cpa 中任意可用的模型名或 alias）。不支持嵌套其它虚拟模型 |
| `members[].weight` | `1` | `weighted` 策略的权重 |
| `members[].expect` / `deny` | — | 该成员的防降级规则（写了即开启检查） |
| `members[].max_retries` | `0` | 该成员被检测到替换后，切换前额外重试的次数 |
| `guard` | `false` | 对所有成员开启防降级检查（期望值为成员名）。命中 `guard.models` 规则的成员总会被检查 |
| `max_attempts` | 成员总尝试数 | 单个请求最多向上游发起的次数 |
| `when_all_cooling` | `soonest` | 全部成员都在冷却时：`soonest` 按恢复时间先后尝试（降级为可用优先）；`fail` 立即返回 429 |
| `failover_on_client_error` | `false` | 普通 4xx（如 400 上下文过长）也切换成员。默认不切，因为同一个请求在别的模型上通常也会失败 |
| `capabilities` | 文本输入输出 | 见下 |

失败时的切换规则：额度 / 限流 / 鉴权 / 模型不存在 / 5xx / 被降级 → 记冷却并切到下一个成员；普通 4xx → 直接返回给客户端。全部成员失败时返回最后一个错误的状态码（例如都是额度问题就返回 429，客户端会按限流退避）。

### 模型能力 `capabilities`

cpa 不会、也无法从成员推导一个新名字的能力，所以需要在这里声明，建议取成员的**最小公共能力**：

| 字段 | 说明 |
|---|---|
| `display_name` / `description` / `owned_by` | 展示信息 |
| `context_length` | 上下文窗口；`input_token_limit` 未填时同值 |
| `max_output_tokens` | 最大输出 token |
| `input_modalities` / `output_modalities` | 如 `[text, image]`；`vision: true` 是加 `image` 的简写 |
| `supported_parameters` | 如 `[tools, tool_choice, reasoning_effort]` |
| `thinking` | `min` / `max` / `zero_allowed` / `dynamic_allowed` / `levels` |

实测（cpa v7.3.17）：Gemini 格式的模型列表会带出 `inputTokenLimit` / `outputTokenLimit` / `supportedInputModalities`，Claude 格式的列表会带出 `max_input_tokens` / `max_tokens` / `display_name`。

### 冷却 `cooldown`

冷却表按**上游模型名**记录，所有虚拟模型共享（A 在 C1 里额度用完，C2 也会跳过 A）；直接请求某个模型不受冷却限制，但结果会更新冷却表。

| 失败类型 | 判定 | 默认冷却 |
|---|---|---|
| `quota` | 402；429/403/5xx 且包含 quota / insufficient_quota / usage limit / billing / 额度 / 余额 等 | 1800s |
| `rate_limit` | 其它 429 | 60s |
| `auth` | 401；不含额度字样的 403 | 600s |
| `not_found` | 404；model_not_found；cpa 找不到可用凭据 | 3600s |
| `server_error` | 5xx、超时、连接错误；连续达到 `server_error_threshold`（默认 2）次才冷却 | 30s |
| `model_mismatch` | 成员返回了被替换的模型 | 600s |
| `client_error` | 其它 4xx | 不冷却 |

- **指数退避**：同一类型连续失败，冷却时长按 `backoff_multiplier`（默认 2）翻倍，上限 `max_seconds`（默认 6 小时）；任意一次成功即清零。
- **尊重上游提示**（`honor_retry_after`，默认开）：额度/限流错误里若带有 `resets_in_seconds`、`retry_after`、Gemini `retryDelay`、`resets_at`，或 "try again in 20m" 之类文字，则以其为准。cpa 自己生成的 `model_cooldown` 错误里的 `reset_seconds` 只是 cpa 凭据层的退避时间，只会用来**延长**、不会缩短插件的冷却。
- 长冷却不会被短冷却覆盖（额度冷却期间再来一次 429 不会把它缩短成 60s）。
- 冷却状态仅在内存中，cpa 重启后清空（每个模型重新获得一次机会，代价很小）。

### 管理接口

需要 cpa 管理密钥：

```bash
# 查看受保护模型、虚拟模型成员可用性、冷却表
curl -H "Authorization: Bearer <management-key>" \
  http://127.0.0.1:8317/v0/management/plugins/orangeguard/status

# 清除某个模型的冷却（body 为空则清除全部）
curl -X POST -H "Authorization: Bearer <management-key>" \
  -d '{"model":"gpt-6-astra"}' \
  http://127.0.0.1:8317/v0/management/plugins/orangeguard/cooldown/reset
```

### 日志

所有事件写入 cpa 日志，可在管理中心按 `orangeguard` 过滤：

```
[warn ] orangeguard: model mismatch detected | requested=gpt-6-astra upstream=gpt-6-astra served=gpt-5.5-mini attempt=1/3 transport=non-stream
[error] orangeguard: model mismatch blocked | requested=gpt-6-astra upstream=gpt-6-astra served=gpt-5.5-mini cooldown=10m0s
[warn ] orangeguard: upstream attempt failed | requested=smart upstream=quota-model status=429 kind=quota cooldown=30m0s attempt=1 error="..."
[info ] orangeguard: request recovered | requested=smart served_by=good-model attempts=2 transport=stream
[debug] orangeguard: skipping cooling members | requested=smart skipped=quota-model,gpt-6-astra
```

## 已知局限

- **非流式重试会烧 token**：要读完整个响应体才知道处理模型。流式在第一个事件就能判定，成本很低。`max_retries` 不宜过大。
- **重试只对间歇性降级有效**：上游 100% 降级时，Guard 的价值是把"静默拿到错误模型"变成"明确的失败"；配合虚拟模型则会冷却该成员并切到其它成员。
- **响应里的 `model` 字段是实际成员名**，不是虚拟模型名。cpa 不会转发插件执行器返回的响应头，所以也无法通过响应头告知是哪个成员处理的——请看日志中的 `served_by`。
- **流式错误的状态码由 cpa 决定**（实测 500，错误信息会完整带出）；非流式会返回插件指定的状态码（降级 503、额度 429 等）。
- 被插件认领的模型（虚拟模型与受保护模型）的 `count_tokens` 返回按请求体长度估算的值（cpa 没有可委托的 count-tokens 回调）。
- 对 Agent 场景，`round-robin` / `random` 在不同模型间切换会让提示词缓存失效，通常更推荐 `fallback`。
- cpa 自带的 `openai-compatibility` 同名 alias 模型池也能做轮询+失败切换，但只限该类渠道、没有防降级、也没有按额度的模型级冷却；本插件适用于所有渠道，可以与之并用。

## 开发

```
main.go / plugin.go / host.go   C ABI 胶水、RPC 分发、host 回调适配
internal/config                 配置解析与校验（含通配匹配）
internal/detect                 处理模型提取、匹配规则、错误分类、重置时间解析
internal/cooldown               冷却表（按上游模型，指数退避）
internal/engine                 路由判定、执行计划、非流式/流式执行循环、模型注册、管理接口
test/e2e                        端到端测试：真实 cpa + mock 上游
```

```bash
make test    # go test -race ./...
make e2e     # 按 go.mod 中锁定的 SDK 版本编译真实的 cpa，加载插件，对 mock 上游跑完整场景
```

参考：[CLIProxyAPI 插件文档](https://github.com/router-for-me/CLIProxyAPIDocs)、[cpa-plugin-anti-model-fallback](https://github.com/lkangd/cpa-plugin-anti-model-fallback)（防降级思路来源）。
