# OrangeGuard

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（cpa）插件，做两件事：

1. **防降级（Guard）**：检测上游偷偷换模型——请求 `gpt-6-astra`，上游却用 `gpt-5.5-mini` 处理并返回 HTTP 200。检测到后自动重试，仍被换就返回明确的错误，**绝不把被替换的模型输出交给客户端**。
2. **合并模型（Virtual Models）**：把模型 A、B… 合并成一个新名字 C。请求 C 时按策略（`fallback` / `round-robin` / `random` / `weighted`）调度到成员模型；成员额度耗尽、被限流、被降级时进入**冷却期**，冷却期内直接跳过，不再反复请求。C 可以声明自己的能力（上下文长度、最大输出、多模态、thinking 等），会出现在 `/v1/models` 里，Agent 能正常识别。

两个功能共用同一套检测与冷却逻辑：虚拟模型的成员同样可以开启防降级，被降级的成员会被冷却并切到下一个成员。

另外有一个**被动监控**：根据 cpa 的用量记录，统计每个模型实际是由哪个模型处理的、降级率多少，不拦截、不增加延迟，用来决定哪些模型值得加入防降级。所有设置都可以在 cpa 管理中心的 **OrangeGuard** 页面里完成（见「配置界面」）。

## 工作原理

插件同时注册以下能力：

| 能力 | 作用 |
|---|---|
| `model_router` | 只认领配置里的虚拟模型和受保护模型（`TargetKind=self`），其它请求原样交还 cpa，零干预 |
| `executor` | 掌控执行循环：通过 `host.model.execute` / `execute_stream` 让 cpa 去请求成员模型（复用 cpa 的凭据、alias、代理、日志、用量统计），检查响应，决定接受 / 重试 / 切换 |
| `model_registrar` | 把虚拟模型及其能力注册进 cpa 模型表，出现在 `/v1/models`（OpenAI / Claude / Gemini 各格式） |
| `usage_plugin` | 接收 cpa 的用量记录，被动统计请求的模型与实际处理的模型（监控） |
| `management_api` | 配置页面（cpa 管理中心菜单「OrangeGuard」）、状态查询、冷却与监控清除接口 |

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

### 从 cpa 插件商店安装（推荐）

1. 在 cpa 的 `config.yaml` 里加入本仓库的商店源：

   ```yaml
   plugins:
     enabled: true
     store-sources:
       - https://raw.githubusercontent.com/woyin/OrangeGuard/main/registry.json
   ```

2. 在管理中心的插件商店里找到 **OrangeGuard**，点安装。cpa 会从本仓库最新的 GitHub Release 下载对应平台的包、校验 `checksums.txt`、放到 `plugins/<goos>/<goarch>/orangeguard-v<版本>.so`，并在配置里写好 `enabled: true`。
3. 在 `plugins.configs.orangeguard` 下添加规则（见下文「配置」），或在管理中心的插件配置页修改。以后在商店里点"更新"即可升级。

Release 由推送 `v*` tag 触发：Linux 版在 `golang:1.26-bookworm` 中编译，并校验所需 glibc 不高于官方 cpa 镜像的 2.36。

### 手动构建

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

**cpa v8 必须显式启用**：v8 起，`plugins.configs` 里没有 `<id>: {enabled: true}` 的插件不会被加载（文件放进目录也没有任何日志）。

### Docker 部署脚本

[`deploy/install-cpa-plugins.sh`](deploy/install-cpa-plugins.sh) 用于官方 Docker 镜像部署的 cpa，安装或更新 orangeguard。已安装的 key-billing **默认不动**。

```bash
curl -fsSLO https://raw.githubusercontent.com/woyin/OrangeGuard/main/deploy/install-cpa-plugins.sh
sudo CPA_DIR=/opt/cpa bash install-cpa-plugins.sh --dry-run   # 只检查与编译
sudo CPA_DIR=/opt/cpa bash install-cpa-plugins.sh             # 安装 / 更新
sudo bash install-cpa-plugins.sh --rollback /opt/cpa/backups/plugins-<时间>   # 回滚
```

`raw.githubusercontent.com` 会缓存几分钟；刚更新后若拿到旧版，把 URL 里的 `main` 换成提交号。

- 通过挂载找到 `$CPA_DIR` 对应的容器、`config.yaml` 和插件目录。
- 在 `golang:1.26-bookworm` 里编译，与官方镜像（Debian bookworm，glibc 2.36）及宿主机 CPU 架构一致；在更新的系统上直接编译的 `.so` 可能因 glibc 版本过高而无法加载。
- 停止容器 → 备份 `config.yaml` 和被替换的插件 → 安装（已有的 orangeguard **按原文件名原地替换**：cpa 优先加载 `name-v1.2.3.so` 这类带版本号的文件，插件商店还可能在配置里锁定版本）→ 必要时在 `plugins.configs` 中加入 `orangeguard: {enabled: true}` → 启动容器。
- 从 cpa 日志确认插件已注册（原本装了 key-billing 的，也确认它重新加载成功），失败则自动回滚。只用 `docker stop/start`，不会触发 compose 的 `pull_policy: always` 拉取新镜像。
- 回滚会把 `config.yaml` 恢复到安装前的版本，之后对 orangeguard 规则的修改需要重新加上。
- `--with-key-billing`：同时把已安装的 key-billing 原地替换为 [woyin fork](https://github.com/woyin/cpa-plugin-key-billing)（会备份计费数据库；之后不要在插件商店里点它的"更新"）。

已在 cpa v8.0.15 官方镜像上完整演练：插件商店安装的原版 key-billing（带版本号文件名与版本锁定）+ 只装 orangeguard、热加载规则、防降级、虚拟模型切换与计费、原地升级、回滚。

## 配置界面

安装后，cpa 管理中心的插件菜单里会出现 **OrangeGuard**（地址 `/v0/resource/plugins/orangeguard/ui`）。页面沿用管理中心的登录状态；单独打开时输入管理密钥即可。三个标签页：

- **组合模型**：卡片列出所有组合模型及可用成员数。编辑页可以：
  - 从 cpa 的真实模型列表搜索添加成员（显示渠道、上下文、多模态），拖动或用 ↑↓ 排序；
  - 选择调度策略（按顺序回退 / 轮询 / 随机 / 加权），加权时为每个成员设权重；
  - 查看每个成员的实时冷却状态并一键解除，预览下一个请求的尝试顺序；
  - 「从成员推导」能力：取所有成员最小的上下文和最大输出、所有成员都支持的输入模态，每项都可手动改；声明的能力超过某个成员时给出警告。
- **防降级与监控**：编辑防降级规则；下方的模型监控表列出每个模型的请求数、降级率、实际处理的模型，降级率高的点「保护」即加入规则。
- **冷却与设置**：当前冷却表（可逐个或全部解除）、各类失败的冷却时长、监控开关。

保存时页面通过 cpa 自带的插件配置接口（`PATCH /v0/management/plugins/orangeguard/config`）写回 `config.yaml`，cpa 热加载，不需要重启；OrangeGuard 不另存数据。

模型列表来自 cpa 的 `/v1/models` 与 `/v1beta/models`：cpa 的管理接口没有带元数据的完整模型列表，所以页面会用配置里的第一个客户端 API Key 去读取。没有配置 API Key 时，可以直接输入模型名。

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
      # guard.models 不写时默认保护 OpenAI / DeepSeek / GLM（见下文）
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
| `models` | OpenAI / DeepSeek / GLM | **不写时默认为** `gpt-*`、`chatgpt-*`、`o1*`、`o3*`、`o4*`、`deepseek-*`、`glm-*`，也匹配带渠道前缀的请求；写了就完全替换默认值；写 `[]` 表示不保护任何模型 |
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
| `cline-pass/deepseek-v4.1-flash` | `deepseek/deepseek-v4.1-flash` | ✅ | 双方渠道前缀不同，模型 ID 相同 |
| `deepseek-v4.1-pro` | `deepseek-v4.1-flash` | ❌ | 档位不同 |
| `glm-4.5` | `glm-4.5-air` | ❌ | 档位不同 |
| `my-glm-5.2` | `glm-5.2` | ✅ | cpa alias 前缀被剥离 |
| `gpt-6-astra` | `gpt-5.5-mini` | ❌ | 降级 |
| `gpt-6-astra` | `gpt-6-astra-mini` | ❌ | 后缀是档位名而不是版本 |
| `gpt-5` | `gpt-5.5` | ❌ | `.` 引入的是另一个小版本 |
| `claude-opus-4` | `claude-opus-4-1` | ❌ | 短数字后缀视为另一个版本 |
| `glm-5.2` | `glm-5.21` | ❌ | 没有分隔边界 |

**0.3.0 默认配置**：OpenAI、DeepSeek、GLM 均默认保护；每条规则仍期望请求的具体模型身份，**不是**允许同系列任意模型。全局额外重试 3 次、间隔 200ms；缺失模型字段默认 `accept`（未知，不算已验证），严格部署可设为 `reject`。其它模型仍由监控观察、按需启用：接管请求会影响 `count_tokens`（只能估算），重试也可能计费。

普通名称比较会去掉双方最后一个 `/` 之前的渠道/厂商命名空间，保留版本和档位。不带 `/` 的规则/`expect`/`deny` 通配也匹配去前缀后的 ID；带 `/` 的通配保持字面匹配，便于限定渠道。`deny` 始终优先。任意业务 alias 请显式写 `expect`；仅保留 `my-glm-5.2 → glm-5.2` 这类可识别模型家族的前缀兼容，不把 `mini`、`flash`、`reasoner` 等片段视作模型身份。

**动态别名不要猜**：DeepSeek 官方 `deepseek-chat` / `deepseek-reasoner` 的映射随版本发布变化；它们可能是同一基础模型的不同思考模式。默认只接受原名（含渠道前缀和快照），不硬编码成 V3/R1/V4，也不互相等同。若渠道确实返回另一个名称，按渠道实际映射配置精确 `expect`，不要写 `deepseek-*` 来掩盖换模。`chatgpt-*` 的滚动别名和 GLM 的渠道内部名也同理。

**安全边界**：这是模型身份替换检测，不是模型能力排名；未知换模也会拦截，不能自动认定更高版本就是升级。响应字段只是上游声明，伪造字段或关闭 thinking 无法仅凭 `model` 检出。渠道若让 `/` 后的同名 ID 表示不同模型，应显式设置带渠道的 `expect` 通配以保持字面约束。

### 监控 `monitor`

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | `true` | 根据 cpa 用量记录统计每个上游模型：请求数、失败数、实际处理的模型及次数、降级率、最近一次降级 |

统计只在内存中，cpa 重启后清零，最多记录 1000 个模型，每个模型保留最常见的 8 个实际模型名。监控以 cpa 用量记录的 `Model`（执行模型）聚合，页面同时显示 cpa 提供的 `Alias`（客户端请求名）。例如执行模型 `gpt-6.1-sol`、客户端请求名 `openai/gpt-6.1-sol`、响应声明 `gpt-6.1-sol` 是三个不同角色，不能混为一谈；比较时去命名空间不会改写请求路由或响应。若宿主未提供原始别名，监控无法仅从用量记录还原它。

处理模型的读取位置：OpenAI Chat Completions、DeepSeek、GLM 的 JSON / SSE 顶层 `model`；Claude（含 DeepSeek Anthropic 兼容接口）的 `model` / `message_start.message.model`；OpenAI Responses 的非流式 `model` / 流式 `response.model`；Gemini 的 `modelVersion`（支持 JSON 数组流）。不把生成内容、`system_fingerprint`、`usage` 或任意深层 `model` 当作模型证据。读取的是 cpa 翻译后返回给客户端格式的响应，翻译可能丢失原始信息。

协议与命名依据：[OpenAI Chat](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)、[OpenAI Responses](https://platform.openai.com/docs/api-reference/responses)、[DeepSeek Chat](https://api-docs.deepseek.com/api/create-chat-completion/)、[DeepSeek 模型与动态别名变更](https://api-docs.deepseek.com/updates/)、[GLM 对话补全（含响应与流式字段）](https://docs.bigmodel.cn/api-reference/模型-api/对话补全)。第三方渠道的自定义 ID（如 `deepseek-v4.1-flash`）按完整模型 ID 匹配，不依赖官方型号枚举。

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
# 查看生效配置（含默认值）、虚拟模型成员可用性、冷却表、监控统计
curl -H "Authorization: Bearer <management-key>" \
  http://127.0.0.1:8317/v0/management/plugins/orangeguard/status

# 清空监控统计
curl -X POST -H "Authorization: Bearer <management-key>" \
  http://127.0.0.1:8317/v0/management/plugins/orangeguard/monitor/reset

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

## 与 key-billing 配合使用

可以和原版 [haowang02/cpa-plugin-key-billing](https://github.com/haowang02/cpa-plugin-key-billing)（包括插件商店安装的）一起用，两个插件**分别安装**，不要合并成一个：cpa 在插件发起的嵌套调用中会跳过**发起方插件自己的**拦截器。两者分开时，key-billing 对用户请求做额度、并发、模型权限检查，对 orangeguard 请求的每个成员再检查一次定价。

**原版 key-billing 拒绝没有价格的模型，包括虚拟模型名。** 需要在它的管理页面里：

1. 给每个虚拟模型名（如 `smart`）设一个自定义价格，**设 0 即可**。实际费用按处理请求的成员模型计算，不会为虚拟模型名单独记账。
2. 给每个成员模型定价（自定义价或 models.dev 参考价）。没有价格的成员会被 key-billing 拒绝；orangeguard 把这种拒绝当作"模型不可用"，冷却 1 小时并切到下一个成员。
3. 给 API Key 绑定了模型白名单的，把虚拟模型名加进白名单。成员模型不需要加：嵌套调用只检查定价。

计费行为（cpa v8.0.15 实测）：

- 请求虚拟模型按**实际处理请求的成员模型**的价格记账，不会重复计费。
- **重试和切换产生的每一次上游调用都会计费**，包括被 Guard 判定为降级而丢弃的那次、以及切换前失败的成员。cpa 会把每次上游调用都交给计费插件，插件无法区分。受保护模型的 `max_retries` 越大，被降级时用户付出的越多。
- 被降级的那次调用按**请求的模型**（如 `gpt-6-astra`）计价，而不是实际返回的降级模型。
- 用户自己的额度、并发限制只在外层请求检查一次，不会因为某个用户额度用完而让成员模型对所有人进入冷却。

[woyin fork](https://github.com/woyin/cpa-plugin-key-billing) 额外提供：未定价模型按 0 元放行（`unpriced_models: allow`，虚拟模型名不必定价）、定时刷新 models.dev 参考价、复制自参考价的自定义价随之更新。需要时用部署脚本的 `--with-key-billing` 安装。

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
