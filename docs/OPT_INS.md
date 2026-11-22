# Opt-in 开关使用指南

argentgo 默认行为对所有可选特性**一律关闭（fail-closed）**。本文档列出全部受支持的 opt-in 开关，以及独立于注册表之外的门控环境变量。所有开关名、环境变量名均与源码逐字对应（事实来源：`internal/app/optin_registry.go`、`internal/config/optin_config.go`、`internal/cli/optin_display.go` 及各 `*_wire.go`）。

独立门控的单一真源是 `internal/cli/optin_metadata.go` 的 `IndependentOptIns` 表；`internal/cli/optin_docs_guard_test.go` 会持续核对本文档、该表与源码读点三者一致——代码新增门控却漏写文档、或文档出现无源码依据的幽灵条目，测试都会失败。新增独立门控时只需改那张表并同步本文档。

> 背景：在 R16 之前，这些开关没有任何文档记载，用户只能「裸写」`export ARGENT_OPT_INS=...`，发现性为零。本文档就是为补齐这一缺口而写。

## 目录

- [一、两条激活入口](#一两条激活入口)
- [二、集中注册表 opt-in 键（ARGENT_OPT_INS / optins 块）](#二集中注册表-opt-in-键argent_opt_ins--optins-块)
- [三、独立门控环境变量](#三独立门控环境变量)
- [四、其它独立门控速查](#四其它独立门控速查)
- [五、逐字节不变承诺](#五逐字节不变承诺)
- [六、推荐配置组合](#六推荐配置组合)

---

## 一、两条激活入口

### 入口 A：`ARGENT_OPT_INS` 环境变量（当前唯一可靠的激活路径）

`ARGENT_OPT_INS` 是承载 opt-in 键列表的**单个**环境变量。解析规则（`ParseOptInEnv` / `OptInFromEnv`）：

- 值为**逗号分隔的键名**，例如 `refusal_reason,context_pressure,apply_patch`。
- 每个片段两侧空白会被忽略（`" a , b "` 等价 `"a,b"`）。
- **未知键与空片段一律忽略**（fail-closed）；非 ASCII 文本不会命中任何键，解析结果与地区无关。
- 未设置、设置为空、只写分隔符时，所有开关保持关闭。

PowerShell：

```powershell
$env:ARGENT_OPT_INS = "keep_implicit_context,preserve_summary_sections,context_pressure"
argentgo "你的任务"
```

bash / Git Bash：

```bash
export ARGENT_OPT_INS="keep_implicit_context,preserve_summary_sections,context_pressure"
argentgo "你的任务"
```

### 入口 B：配置文件的 `optins` 块（辅助入口，见状态说明）

配置文件位于 `~/.argentgo/config.json`，可写一个布尔类型的 `optins` 子块：

```json
{
  "optins": {
    "cli_hints": true,
    "apply_patch": true,
    "grep_ex": true,
    "context_pressure": true,
    "attempt_isolation": true,
    "keep_implicit_context": true,
    "preserve_summary_sections": true
  }
}
```

解析规则（`config.ParseOptInDisplay`）：

- 只认布尔值 `true`；字符串 `"true"`、数字、`null` 等非布尔值一律视为关闭（fail-closed）。
- 白名单之外的键（含 CJK 键）被忽略；缺键 / 空块 / 整个块缺失时所有开关默认关闭。
- 支持「直接形式」：若没有 `optins` 块，配置映射本身会被当作 opt-in 映射解析（白名单外的值同样被忽略）。
- 输出按 key 升序稳定排序，同一组配置永远得到逐字节一致的展示文本。

**当前状态**：`Execute` 的运行时装配面（`internal/app/app_execute.go`）读取 `ARGENT_OPT_INS` 与配置 `optins` 块的**并集**（`mergeOptIns(ParseOptInEnv(...), configOptIns(...))`）。两个入口都能真正在运行中打开开关：

- 入口 A：`ARGENT_OPT_INS` 环境变量，可打开集中注册表的全部 14 个键；此外它还能携带**双激活面**独立门的等价键（当前为 `jobs`，见第三节），所以「14 键」只是注册表自身的数量，并非该变量能接受的全部键。
- 入口 B：配置文件 `optins` 块，可打开下面标注「配置可用」的 9 个键。
- `optins` → app 运行时的映射交集（`configOptInKeyMap`，`app_config_optins.go:17-27`）只包含标注「配置可用」的 9 个键；`refusal_reason`、`untrusted_result`、`untrusted_boundary`、`compaction_fidelity`、`auto_verify` 这 5 个键**只能**通过 `ARGENT_OPT_INS` 打开。

### 两入口同时存在时

代码语义是「逻辑或」：任一来源显式开启即视为开启（`mergeOptIns`）。两个入口都未设置时所有键保持默认关闭（fail-closed），逐字节不变。

---

## 二、集中注册表 opt-in 键（ARGENT_OPT_INS / optins 块）

下表给出集中注册表（`internal/app/optin_registry.go`）的 14 个键。默认状态全部为**关闭**；「配置可用」指该键是否落在 `configOptInKeyMap` 的交集内（不在交集的键只能用环境变量打开）。

> 注意：这张表是**注册表**的全集，不是 `ARGENT_OPT_INS` 能接受的全部字符串。独立门 `jobs` 也接受 `ARGENT_OPT_INS=jobs`（双激活面），但它不在 14 键注册表内，故列在第三节。总开关数因此不能用「14」概括。

| # | 键名 | 中文标签 | 默认 | 配置可用 | 归类 |
|---:|---|---|---|---|---|
| 1 | `refusal_reason` | 拒绝理由 | 关闭 | 否 | 交互 |
| 2 | `untrusted_result` | 不可信结果 | 关闭 | 否 | 上下文 |
| 3 | `context_pressure` | 上下文压力 | 关闭 | 是 | 上下文 |
| 4 | `untrusted_boundary` | 不可信边界 | 关闭 | 否 | 上下文 |
| 5 | `attempt_isolation` | 失败尝试隔离 | 关闭 | 是 | 上下文 |
| 6 | `compaction_fidelity` | 压缩保真 | 关闭 | 否 | 上下文 |
| 7 | `keep_implicit_context` | 保留隐式上下文 | 关闭 | 是 | 上下文 |
| 8 | `preserve_summary_sections` | 保留摘要章节 | 关闭 | 是 | 上下文 |
| 9 | `apply_patch` | 补丁应用 | 关闭 | 是 | 交互 |
| 10 | `grep_ex` | 增强搜索 | 关闭 | 是 | 交互 |
| 11 | `cli_hints` | CLI 提示 | 关闭 | 是 | 交互 |
| 12 | `tool_error_reflection` | 工具错误反射 | 关闭 | 是 | 交互 |
| 13 | `model_summary` | 模型摘要 | 关闭 | 是 | 上下文 |
| 14 | `auto_verify` | 自动验证 | 关闭 | 否 | 交互 |

### 1. `refusal_reason`（拒绝理由）

- **作用**：控制是否在结果中暴露模型的拒绝原因。装配点为 `agent.SetRefusalReason(...)`。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="refusal_reason"`。该键**不在**配置交集内，配置文件无法打开。
- **适用场景**：需要诊断模型为何拒绝某请求、需要在结果里保留可审计的拒绝理由时。
- **逐字节不变承诺**：未显式开启时 `SetRefusalReason(false)` 与零值一致，结果中不暴露拒绝原因，输出逐字节不变。

### 2. `untrusted_result`（不可信结果）

- **作用**：控制是否把工具结果标记为不可信。装配点为 `agent.SetToolResultWrapping(...)`。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="untrusted_result"`。该键**不在**配置交集内。
- **适用场景**：工具来源不可信、需要让模型把工具输出当作数据而非指令来处理（降低提示注入风险）时。
- **逐字节不变承诺**：未显式开启时不启用工具结果包装，工具结果逐字节不变。

### 3. `context_pressure`（上下文压力）

- **作用**：控制是否暴露上下文压力信号。装配点为 `SetContextPressure(...)` 与轮末的 `replaceContextPressure(ctx, newContextPressureObserver())` 观察器。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="context_pressure"`，或在 `optins` 块中设 `"context_pressure": true`。
- **适用场景**：长会话接近上下文上限，希望模型/调用方感知到压力信号并据此调整策略时。
- **逐字节不变承诺**：未显式开启时观察器为 typed nil，轮末采集与上报都是空操作，系统提示/事件序列逐字节不变（且能避免「上次开、本次关」时过期观察器残留的错误通知）。

### 4. `untrusted_boundary`（不可信边界）

- **作用**：控制是否启用不可信内容边界标记。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="untrusted_boundary"`。该键**不在**配置交集内。
- **适用场景**：需要在提示中显式区分可信/不可信内容边界（例如把外部来源文本包裹起来）时。常与 `untrusted_result` 一起开。
- **逐字节不变承诺**：未显式开启时不启用边界标记，相关输出逐字节不变。

### 5. `attempt_isolation`（失败尝试隔离）

- **作用**：控制是否在验证失败时把失败尝试分叉为旁支会话。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="attempt_isolation"`，或 `optins` 块中 `"attempt_isolation": true`。
- **适用场景**：自动化验证/重试密集的任务，希望失败尝试不污染主会话树、便于回退时。
- **逐字节不变承诺**：未显式开启时验证失败**不触碰会话树**，事件流逐字节不变。

### 6. `compaction_fidelity`（压缩保真）

- **作用**：控制是否在压缩摘要要素缺失时发出质量告警。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="compaction_fidelity"`。该键**不在**配置交集内。
- **适用场景**：依赖自动压缩、关心压缩摘要是否丢失关键要素，希望在缺要素时收到告警时。
- **逐字节不变承诺**：未显式开启时压缩完成点**不挂接任何观察者**，压缩行为逐字节不变。

### 7. `keep_implicit_context`（保留隐式上下文）

- **作用**：控制是否把压缩丢弃的隐式上下文（尾部消息与失败尝试）重新并入摘要。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="keep_implicit_context"`，或 `optins` 块中 `"keep_implicit_context": true`。
- **适用场景**：长任务压缩后需要保留尾部细节与失败尝试的语境，避免「忘记刚才发生过什么」时。
- **逐字节不变承诺**：未显式开启时摘要逐字节不变。

### 8. `preserve_summary_sections`（保留摘要章节）

- **作用**：控制是否在重建压缩摘要时保留原摘要中的关键章节（如目标、工具活动）。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="preserve_summary_sections"`，或 `optins` 块中 `"preserve_summary_sections": true`。
- **适用场景**：多次压缩的长会话，希望目标、工具活动等章节在重建摘要时不被抹掉时。
- **逐字节不变承诺**：未显式开启时重建摘要逐字节不变。

### 9. `apply_patch`（补丁应用）

- **作用**：控制是否启用 `apply_patch` 类补丁工具行为。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="apply_patch"`，或 `optins` 块中 `"apply_patch": true`。
- **适用场景**：希望模型使用结构化补丁而非整文件重写来修改代码时。
- **逐字节不变承诺**：未显式开启时工具行为逐字节不变。

### 10. `grep_ex`（增强搜索）

- **作用**：控制是否启用扩展 grep 搜索行为。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="grep_ex"`，或 `optins` 块中 `"grep_ex": true`。
- **适用场景**：需要更丰富的搜索语义（扩展匹配能力）时。
- **逐字节不变承诺**：未显式开启时搜索结果逐字节不变。

### 11. `cli_hints`（CLI 提示）

- **作用**：控制是否在输出中附带 CLI 提示。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="cli_hints"`，或 `optins` 块中 `"cli_hints": true`。
- **适用场景**：希望输出里附带下一步 CLI 操作提示时。
- **逐字节不变承诺**：未显式开启时输出逐字节不变。
- **注意**：此键与下一节的独立变量 `ARGENT_CLI_HINTS` 现在都能开启 CLI 错误修复建议——`ARGENT_CLI_HINTS=1` 或 `ARGENT_OPT_INS=cli_hints` 任一成立即开启。
- **首次启动引导**：无参数运行 `argentgo`、stdin 为字符设备（终端/控制台）且既无配置/apiKey 也无存储凭据时，追加三行一次性引导（login 示例、tui/GUI 入口、doctor 自检）并在 storage 目录写 `.onboarded` 标记；标记存在即不再引导。关闭时零文件读写、stderr 逐字节不变。

### 12. `tool_error_reflection`（工具错误反射）

- **作用**：工具调用失败时，向模型回灌结构化的错误反射（修复指令行 + `[tool: X] ERROR` 头 + 脱敏有界的错误正文），帮助模型自我修正。装配点为 `agent.SetToolErrorReflection(...)` 与 `dispatch.go` 错误分支的 `ReflectionForDispatch(...)`。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="tool_error_reflection"`，或 `optins` 块中 `"tool_error_reflection": true`。
- **适用场景**：模型反复因可修复错误（路径、参数、命令拼写）失败时，提供足够信息让其自行纠正，而非只看到一行 `error: ...`。
- **逐字节不变承诺**：未显式开启时回灌给模型的错误串逐字节不变；事件与会话记录始终保存原始错误字节。

### 13. `model_summary`（模型摘要）

- **作用**：压缩时用模型生成一段增量叙事摘要，补抽取式摘要在"多行目标 / 散文计划 / 连贯性"上的不足。原语位于 `internal/app/model_summary.go`；模型只补叙事、绝不覆盖 `files`/`failures` 等权威结构化字段，失败一律回落到抽取式。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="model_summary"`，或 `optins` 块中 `"model_summary": true`，并配置 `secondary-model`（为空则不调用，控制成本）。
- **适用场景**：长会话压缩后希望上下文更连贯时。成本提示：压缩时额外发起一次模型调用（3 秒超时，失败回落抽取式）。该开销通过 EventUsage 在事件流/usage 工具/TUI 可见，但**不计入 agent 预算与 session stats**（绕过 agent 循环，不经过 account()）。
- **逐字节不变承诺**：未显式开启或 secondary-model 为空时不发起任何模型调用，压缩摘要逐字节不变；失败一律回落抽取式。

### 14. `auto_verify`（自动验证）

- **作用**：编辑类工具成功后自动派生已有的验证诊断回环（Go 项目在无显式验证命令且工作区含 `go.mod` 时合成 `go test ./...`），把验证结论作为结构化诊断回灌给模型，帮助其自我修正。装配点为 `app_execute.go` 注入的 `hookedTools.autoDiagnostics` 与 `verifyFuncFor(..., auto)`（实现见 `internal/app/verify_wire.go`、`hooks.go`）。
- **默认状态**：关闭。
- **如何开启**：`export ARGENT_OPT_INS="auto_verify"`。该键**不在** `configOptInKeyMap`（`app_config_optins.go:17-27`）交集内，配置文件无法打开，**只能**通过 `ARGENT_OPT_INS` 开启。另注：doctor 中的 `ARGENT_EDIT_DIAGNOSTICS=1` 是另一条独立的编辑后诊断路径，与本 opt-in 做或运算，但它不属于本 14 键注册表。
- **适用场景**：希望编辑后自动跑一遍验证、把失败信息即时回灌给模型做修正时。
- **逐字节不变承诺**：未显式开启时 `autoDiagnostics` 为零值、`verifyFuncFor` 走原有分支，验证与诊断路径逐字节不变；即便开启，若无 `go.mod` 或未命中合成条件也回落到原有分支。

---

## 三、独立门控环境变量

以下开关**不属于**集中注册表的 14 个键，而是通过各自独立的环境变量控制。多数只能通过独立 env 打开；`jobs` 是例外——它同时接受 `ARGENT_OPT_INS=jobs`（双激活面）。它们的共同点：默认关闭，且关闭时相关行为逐字节不变。本节先详述常用门控，末尾补齐后续批次加入的门控；其余门控在第四节速查。

### `ARGENT_CLI_HINTS` —— CLI 错误修复建议

- **作用**：当 CLI 命令发生已知类型的错误（TLS、文件、交互式、凭证四类）时，在既有两行错误输出之后追加一条本地化修复建议。
- **开启值**：首尾空白修剪后等于 `1`，或设置 `ARGENT_OPT_INS=cli_hints`（两者任一成立即开启）。
- **适用场景**：终端用户或 CI 排错时，希望错误信息自带可执行的修复建议。
- **逐字节不变承诺**：门控关闭、`err == nil`、或错误未命中任何已知分支时，输出与 `writeActionableError` 逐字节相同；既有的 `error: ...` 行与分类说明始终不变。

### `ARGENT_APPROVE_DANGER` —— TUI 危险命令原因展示

- **作用**：TUI 审批弹窗中，对变更型命令展示由 `security.DangerReason` 计算的稳定中文原因（走 `ApproveDanger` 弹窗，多一行并**预选拒绝**）。
- **开启值**：首尾空白修剪后等于 `1`（`TrimSpace(env("ARGENT_APPROVE_DANGER")) == "1"`）。
- **适用场景**：交互式使用、希望在批准危险命令前看到明确原因并默认偏向拒绝时。
- **逐字节不变承诺**：门控关闭（env 为 nil、命令无危险）时继续走原 `model.Approve`，与接线之前逐字节相同；`name` / `args` 始终原样传递。

### `ARGENT_CONTINUATION_BRIEF` —— 会话续接摘要

- **作用**：resume / 追问时把上一段会话的目标、改动文件、失败、下一步作为有界摘要注入 system 提示，补齐跨轮历史断裂（单次运行内历史本就完整，这项针对的是跨 `Execute` 的续接）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：常 resume 旧会话、或频繁追问"再跑一次刚才的测试"时，让模型记得上一段上下文。
- **逐字节不变承诺**：关闭或无历史时 system 提示、消息序列、事件流、JSONL 逐字节不变。开启时注入的是有界摘要（字簇安全截断），不是全量历史回放——极老细节仍靠 `history` 工具补。

### `ARGENT_CACHE_VISIBLE` —— 缓存能力提示

- **作用**：`argentgo --tokens` 报告追加一行 `cache hint: ...`，说明当前端点的提示缓存能力（静态判定，不发起请求）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：排查 OpenAI 兼容端点的提示缓存是否可用时。
- **逐字节不变承诺**：门控关闭时报告与接线之前逐字节相同。

### `ARGENT_TUI_USAGE` —— TUI 用量与缓存命中显示

- **作用**：TUI 头部/状态行显示本轮 token 用量与缓存命中数（激活既有的 `SetUsage` / `SetCachedTokens` 显示路径）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：交互式观察 token 消耗与缓存命中时。
- **逐字节不变承诺**：门控关闭时事件桥不转发用量事件，TUI 帧字节与接线之前逐字节相同。注意显示的 cached 取当轮用量；R23 起 `account()` 已累加 cached（`loop_helpers.go:178-179` 累加 `CachedTokens`/`CacheWriteTokens`），但 `model_summary` 的补全调用刻意绕过 agent 循环、不经 `account()`，其用量只以 EventUsage 可见、不计入 agent 预算与 session stats——这是仍需注意的边界。

### `ARGENT_OUTPUT_SPILL` —— 超大工具输出落盘

- **作用**：对超大工具输出选择性启用产物落盘，避免把巨型输出全部塞进上下文。
- **开启值**：真值之一——`1`、`true`、`yes`、`on`（大小写不敏感，首尾空白修剪）。其他值或未设置视为关闭。
- **配套变量**：
  - `ARGENT_OUTPUT_SPILL_DIR`：产物目录；缺省为 `<storage>/artifacts`（位于存储目录而非工作区，不污染仓库）。
  - `ARGENT_OUTPUT_SPILL_THRESHOLD`：字节阈值（正整数）；缺省使用 `tools.DefaultOutputSpillThreshold`。
- **适用场景**：长时间运行、工具会产生大量输出（构建日志、扫描结果等）的任务。
- **逐字节不变承诺**：未启用时 `configureOutputSpill` 原样返回 `ctx`，tools 包走普通 `clipTo` 路径，输出字节完全一致，也**不会创建或扫描任何目录**。启用后会做一次尽力而为的旧产物清理，清理失败被刻意忽略，绝不会中断运行。

### `ARGENT_BUILTIN_SHELL` —— 自研内建 shell 兜底

- **作用**：启用纯标准库实现的自研内建 shell 解释器来执行命令（输出仍走既有清洗/裁剪管线，非零退出转为含退出码的 error）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：需要在缺少系统 shell 的环境下运行命令，或希望使用项目自带的确定性 shell 行为时。
- **逐字节不变承诺**：默认关闭，命令走系统 shell 路径，逐字节不变；只有 `ARGENT_BUILTIN_SHELL=1` 才切换到内建解释器。

### `ARGENT_APPROVAL_DIFF` —— 审批卡 diff 展示

- **作用**：TUI 审批卡渲染有界的增删行摘要（`+N/-M in path`），并限制行数与路径宽度，避免长路径撑破弹窗；预览按字素边界截断，CJK 文件名不会被切坏。
- **开启值**：首尾空白修剪后等于 `1`（由 `ConfigureApprovalDiff` 注入的 env 判定）。
- **适用场景**：批准文件编辑前希望一眼看到改了哪些文件、增删大致行数时。
- **逐字节不变承诺**：门控关闭时无论审批请求是否带 diff 数据，弹窗帧字节都与接线前逐字节相同；`name` / `args` 始终原样传递。

### `ARGENT_RETAIN_GUIDANCE` —— 纠正提示压缩存活

- **作用**：控制是否在压缩时保留纠正 nudge 记录的「压缩存活」标记（`agent.SetRetainGuidance`）。
- **开启值**：`os.Getenv` 原值等于 `1`。
- **适用场景**：长期运行、关心模型自我纠正提示在压缩后是否仍然存在时。
- **逐字节不变承诺**：未显式开启时写入 `false`（与零值一致），nudge 记录形状逐字节不变。

### `ARGENT_GUIDANCE_DEDUPE` —— 失败指引签名去重

- **作用**：opt-in 开启时，在连续失败期间以错误种类为签名去重恢复指引（`agent.SetGuidanceDedupe`），同一种签名只注入一次；工具成功后清空状态，下一段失败可再次注入。
- **开启值**：首尾空白修剪后等于 `1`（由 `Execute` 装配层从注入的 env 读取）。
- **适用场景**：长对话里模型反复失败导致同一句恢复指引堆积、污染上下文时。
- **逐字节不变承诺**：默认关闭时每一轮达到错误上限仍照常注入指引，与历史运行逐字节一致。此开关与 `ARGENT_RETAIN_GUIDANCE` 正交：后者决定指引记录的载荷形状，本开关决定是否写入这条记录。

### `ARGENT_OUTPUT_COLLAPSE` —— bash 输出行折叠

- **作用**：让 `clipBashOutput` 走有界的行折叠输出帧（`internal/textutil` 原语），替代原有的整段裁剪。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：bash 输出行数多、希望折叠重复/超长行以便模型阅读时。
- **逐字节不变承诺**：默认关闭时仍走原有 `clipTo` 路径，输出逐字节不变；只有 `ARGENT_OUTPUT_COLLAPSE=1` 才切换。

### `ARGENT_TODO_STRICT` —— todo 严格状态校验

- **作用**：在每次 todo 写入前调用 `ValidateTodoState` 校验即将写入的状态集，挡住非法状态。
- **开启值**：真值之一——`1`、`true`、`yes`、`on`（大小写不敏感，首尾空白修剪）。空值或假值关闭。
- **适用场景**：希望模型维护的 todo 列表结构始终合法时。
- **逐字节不变承诺**：关闭时写路径逐字节不变；该开关与字符预算安全阀 `ARGENT_TODO_MAX_CHARS` 相互独立。

### `ARGENT_SUMMARY_SCHEMA` —— 压缩摘要固定 schema

- **作用**：在压缩产出摘要后，按固定顺序补齐缺失的 `Goal` / `State` / `Next` / `Files` 小节；`Files` 从已记录的改动文件事件机械生成，并回报摘要是否无实质内容供上层决定是否重试模型摘要。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：依赖抽取式压缩、希望摘要结构稳定不缺章节时。
- **逐字节不变承诺**：默认关闭时 `Summarize` 不触碰摘要，既有压缩记录逐字节不变；开启时也只补缺失章节，绝不覆盖 `files` / `failures` 等权威结构化字段。

### `ARGENT_BUDGET_PROJECTION` —— 消息预算投影

- **作用**：在 `Messages()` 组装前按 rune 预算做投影：保护最新用户输入、保护未配对的活尾工具调用、清除保留消息的思考文本，使「活尾工具调用」真正触发保护。
- **开启值**：首尾空白修剪后等于 `1`。配套 `ARGENT_BUDGET_PROJECTION_BUDGET` 指定投影预算（run E 数，正整数）；非正或不可解析时回退保守默认 `32000`。
- **适用场景**：长会话上下文逼近上限、希望在进入模型前主动压缩消息体时。
- **逐字节不变承诺**：关闭时只多一次环境变量读取，返回值逐字节不变；开启时的预算来源（env 或 default）可被审计与测试断言。

### `ARGENT_JOBS` —— 后台作业注册表

- **作用**：把 bash 工具的 `background_after_ms` 真正接上 `jobs.Registry`，使长命令被提升为可查询的后台作业，退出时回收仍存活的进程。
- **开启值**：首尾空白修剪后等于 `1`，或 `ARGENT_OPT_INS` 列表含 `jobs`（双激活面，任一成立即开启）。
- **适用场景**：需要启动长任务并在后续轮次查询其状态时。
- **逐字节不变承诺**：默认关闭时不创建目录、不调用 `SetJobs`、不安装关闭钩子，注册表仍为 nil，`background_after_ms` 被静默忽略，系统提示/事件流/JSONL 逐字节不变。

### `ARGENT_BASH_USE_PROC` —— 默认 bash 主路径进程硬化

- **作用**：在默认 bash 主路径上启用进程硬化（proc 相关强化）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：希望默认执行路径获得更强的进程行为保证时。
- **逐字节不变承诺**：默认关闭时仍走原路径，逐字节不变；只有显式 `=1` 才切换。

### `ARGENT_PATH_COMPLETION` —— 内联路径补全

- **作用**：TUI 输入行启用内联路径补全；env 由集成方注入，TUI 渲染路径本身不直接读环境变量。
- **开启值**：等于 `1`（无 TrimSpace，`m.env("ARGENT_PATH_COMPLETION") == "1"`）。
- **适用场景**：交互式输入文件路径、希望边打边补全时。
- **逐字节不变承诺**：关闭时 `m.env` 为 nil 或未设置，整段新逻辑不进入，既有 slash 补全与默认帧逐字节不变。

### `ARGENT_VIM_MODE` —— TUI vim 键位

- **作用**：TUI 输入行切换到 vim 键位（由集成方注入的 env 判定）。
- **开启值**：首尾空白修剪后等于 `1`。
- **适用场景**：习惯 vim 键位的交互用户。
- **逐字节不变承诺**：关闭时保持经典输入，帧字节逐字节不变。

### `ARGENT_TUI_PROMPT_HISTORY` —— TUI 输入历史持久化

- **作用**：把 TUI 提交的输入追加到存储目录下的 `prompts.jsonl`（JSONL，单行一条，含次数与时间戳），重启后第一次 `Up` 就能召回上次会话的输入。原语为 `internal/tui/prompt_store.go` 的 `PromptStore`；本开关只负责把它接进 TUI 的输入环：提交时 `Record` 顺带写盘，启动时 `Load` 容错读取自愈后再 `SeedHistory` 灌入内存（只恢复内存、绝不回写）。
- **开启值**：首尾空白修剪后等于 `1`（由集成方 `internal/app/tui.go` 通过 `stdio.Env` 注入判定）。
- **适用场景**：希望「输入历史不再随重启即丢」、经常用 `Up` 复用上一条命令的交互用户。历史写入存储目录而非工作区，不污染仓库；单条上限 4096 rune（兼容 CJK/emoji），磁盘硬上限 1000 行。
- **逐字节不变承诺**：门控关闭（env 为 nil、未设置或非 `1`）时 `frontendOpts.PromptHistory` 保持 nil，编辑器不打开、不读取、不创建任何文件，首帧、历史导航与 `Up` 行为均与接线之前逐字节一致。开启后写盘失败被刻意吞掉，不向屏幕发布通知，也不泄漏提示内容。

### `ARGENT_MCP_IMAGE_ARTIFACT` —— MCP image 内容块落盘

- **作用**：把 MCP `tools/call` 返回的 image 内容块 base64 解码后写入产物目录，并把占位符从 `[image: <mime>]` 增强为 `[image: <mime> → <path>]`，让模型知道可用 `read` 工具读取。落盘复用白名单扩展名、大小上限与防逃逸校验，任何失败都回落旧占位符。
- **开启值**：真值（如 `1`/`true`，大小写不敏感、首尾空白修剪），且需配置产物目录 `ARGENT_MCP_BLOB_ARTIFACT_DIR`。
- **适用场景**：MCP 服务器会返回图片、希望模型能读取图片文件时。
- **逐字节不变承诺**：开关关闭、未配目录、数据不可解码或落盘失败任一成立时，逐字节返回旧占位符，默认输出与历史逐字节一致。

### `ARGENT_MCP_INJECTION_SCAN` —— MCP 工具输出注入扫描

- **作用**：对 MCP `tools/call` 返回的最终文本做提示注入检测（读点 `internal/mcp/mcp.go` 的 `mcpInjectionScanEnv` / `warnOnPromptInjection`，复用 `security.ScanPromptInjection`）。命中**高风险**注入时在原文前加一行可读警示 `[注意：工具输出含疑似指令注入]`；中/低风险是软信号、不触发警示，避免过度敏感。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：MCP 服务器会返回不可信的外部文本、希望在进入模型上下文前对高风险注入给出可见提示时。注意这是**警示而非防御**：是否遵从注入内容仍由模型自行判断。
- **逐字节不变承诺**：只警示、不改写、不过滤原文中的任何字节；开关关闭、未命中高风险注入时 `warnOnPromptInjection` 原样返回文本，工具输出与历史逐字节一致。

### `ARGENT_RULE_DISCOVERY` —— 规则文件发现元数据提示

- **作用**：开启后扫描用户级（home）与项目级（root）的规则文件，把文件名与作用域渲染成一段「只列名、不加载全文」的 system 提示片段（对 legacy 规则追加一次迁移提示）。读点为 `internal/app/app_prompt.go` 的 `ruleDiscoveryEnabled` / `ruleDiscoveryBlock`。
- **开启值**：`1`、`true`、`yes`、`on` 之一（大小写不敏感、首尾空白修剪）；未设置、空值或其它值关闭。
- **适用场景**：项目里有 AGENTS.md / CLAUDE.md 等规则文件、希望模型知道其存在但不加载全文时。
- **逐字节不变承诺**：关闭或不传 env 时 `ruleDiscoveryBlock` 返回空串，不读取任何规则文件、不注入任何文本，system 提示与消息序列逐字节不变。

### `ARGENT_CHECKPOINT_GUARD` —— checkout 还原目标守卫

- **作用**：`sessions checkout` 打开会话时，先让 store 对转录目标做只读校验，拒绝逃逸或已消失的目标（装配点 `internal/cli/sessions.go` 的 `checkoutGuardEnabled` / `sessionsCheckout`，注入 `session.NewCheckoutGuard()`）。
- **开启值**：首尾空白修剪后等于 `1`；`0`、空值或其它值走原路径。
- **适用场景**：需要防止 checkout 指向工作区外或已失效的转录目标时。
- **逐字节不变承诺**：关闭时不安装 `WithCheckoutGuard`，checkout 流程、输出与会话树逐字节不变。

### `ARGENT_REPEAT_PITFALL_TRACKING` —— 跨情节重复踩坑计数

- **作用**：启用跨情节的重复错误签名计数（`internal/agent/mistake_tracker.go` 的 `RecordSignature`），供恢复指引统计「同一签名反复出现」。这是进程级 opt-in：包初始化读取 env，装配层也可用 `agent.SetRepeatPitfallTracking` 显式设置。
- **开启值**：原值精确等于 `1`（不修剪空白、不接受 `true`/`yes` 等其它真值）。
- **适用场景**：关心模型是否反复踩同一类错误、希望据此强化恢复指引时。
- **逐字节不变承诺**：关闭时 `RecordSignature` 立即返回，不分配内存、不触锁，既有 `Record` / `Reset` / `Check` 路径逐字节不变。

### `ARGENT_RECOVERY_METRICS` —— 恢复成效账本

- **作用**：在恢复指引注入处与工具成功处采集「恢复动作之后运行是否真的恢复」的分子/分母（`internal/agent/recovery_metrics.go` + `loop.go` 的 `recordRecoveryAttempt` / `recordRecoverySuccess`），按失败种类统计成功率。每次运行读取一次 env。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：关心失败恢复机制的实际成效、想量化「注入指引后是否真的成功」时。
- **逐字节不变承诺**：关闭时不分配计数器、记录路径为空操作，消息流与事件流逐字节不变。

### `ARGENT_RECOVERY_DETAIL` —— 恢复细节后缀

- **作用**：恢复路径上把 `session.ClassifyRecovery` 一次性分类得到的 `RecoveryPlan` 细节（类别、断链数、孤儿工具调用数、孤儿工具结果数及其实例 ID）作为一段中文文本后缀追加到恢复公告末尾。读点为 `internal/app/app_execute.go` 的 `recoveryDetailEnabled` 与 `internal/app/app_execute_budget.go` 的 `recoveryDetailSuffix`；细节字段由 `recoveryDecisionFor` 从同一份计划一次填充，不做二次分类。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：排查恢复时历史为何被判为降级（断链/孤儿结果）、或确认一次中断的具体类别时，需要在用户可见公告里看到结构化细节。
- **逐字节不变承诺**：关闭时零调用、不追加任何后缀，公告与 `NeedsConfirmation` 逐字节不变；仅在已有恢复公告（Notice 非空）时追加，干净会话不会产生凭空公告。该开关只增信息，绝不改变确认门（安全语义仍由分类唯一决定）。

### `ARGENT_TURN_TIMING` —— agent 轮次逐阶段计时

- **作用**：agent 循环开启逐阶段结构化计时日志（`internal/agent/loop.go` 的 `turnTimingEnabled` / `turnTimingLogger`），输出各阶段耗时。每次运行读取一次 env，不引入需装配层设置的进程级全局状态。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：排查单轮延迟、定位耗时集中在哪个阶段时。
- **逐字节不变承诺**：关闭时逐阶段计时器保持 nil、不输出任何计时日志，日志与事件流逐字节不变。

### `ARGENT_STREAM_RECOVERY` —— 流中断后非流式回补

- **作用**：在 app 层接通 `agent.Options.EnableStreamRecovery`（读点 `internal/app/app_execute.go` 的 `streamRecoveryEnabled`，注入 `loop.Opts`）。提供方在流式答复中途报错（`llm.ErrStreamError`）且已输出部分文本时，用同一 model/messages/tools 再发一次非流式补全；补不到就沿用原失败路径，每轮最多一次。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：不稳定网络或流式端点偶发中断，希望自动以非流式方式取回完整答复时。
- **逐字节不变承诺**：关闭时 `EnableStreamRecovery` 为零值，agent 的恢复分支整体短路，请求体、事件流与会话 JSONL 逐字节不变；开启但未命中流错误、无部分文本或 chatter 不支持非流式时同样走原路径。

### `ARGENT_SKILL_INJECTION_SCAN` —— 技能索引注入扫描

- **作用**：渲染技能索引前对 `desc` / `when` 字段做语义注入检测（读点 `internal/skills/skills.go:313` 的 `skillInjectionScanEnv` / `skillInjectionScanEnabled` / `clipSkillIndexField`，复用 `security.ScanPromptInjection`）。命中时把整个字段替换为中性占位 `[已过滤可疑指令]`，作为「该字段已被过滤」的可见标记。与 MCP 注入扫描只警示不同，这里是**过滤**：命中字段的原文不会进入索引或 `use_skill` 抬头。扫描只作用于索引渲染，不修改原始技能。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：技能来源不完全可信（如第三方 SKILL.md）、担心元数据里的注入指令污染模型时。
- **逐字节不变承诺**：关闭或字段未命中注入时，渲染走同一条脱敏/折叠/裁剪链，输出与 `clipIndexField` 逐字节相同；命中时才以占位替换原文。

### `ARGENT_TOOL_TIMING_HISTOGRAM` —— 工具耗时直方图

- **作用**：开启后按工具名累积每次调用的精确耗时（`internal/agent/loop.go:247` 的 `toolTimingHistogramEnabled`，在 `newRunState` 每次运行读取一次），轮末落一条 `agent_tool_timing_histogram` 结构化日志，含各工具的最慢/P95 摘要与 `tool_top_slow`。与 `ARGENT_TURN_TIMING` 正交，可单独开启。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：排查哪些工具调用最耗时、定位工具侧延迟分布时。
- **逐字节不变承诺**：关闭时 `state.toolTimingHistogram` 为 nil，dispatch 侧的 `Record` 与轮末摘要日志全是空操作，不读时钟、不分配，事件序列与会话记录逐字节不变。

### `ARGENT_TOOL_CALL_TRACE` —— 工具调用链追踪

- **作用**：开启后为每次工具调用开启/结算一个跨度（`internal/agent/loop.go:254` 的 `toolCallTraceEnabled`，在 `newRunState` 每次运行读取一次），轮末落一条 `agent_tool_call_trace` 结构化日志，含调用顺序、成败与失败时的模型可读错误文本。与直方图正交：直方图给分布/TopSlow，trace 给调用顺序与错误文本。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：排查一次运行内工具调用的先后顺序、哪一步失败及失败原因时。
- **逐字节不变承诺**：关闭时 `state.toolCallTrace` 为 nil，`Begin` / `End` 与轮末摘要日志全是空操作，不构造错误对象，事件序列与会话记录逐字节不变。

### `ARGENT_CONFIG_RELOAD_DIFF` —— 配置热重载差异摘要

- **作用**：把配置热重载的单行日志从 `reloaded: <generation/model> -> ...` 改为基于键路径差异的中文摘要（读点 `internal/app/app_config_reload.go:101` 的 `configReloadDiffEnabled`）。摘要只消费 `config.DiffConfigMaps` 产出的键路径，绝不接触配置值；看起来像凭据的键追加 `<redacted>` 占位，提示「值变了但被隐藏」。
- **开启值**：原值精确等于 `1`（**不修剪空白**，与多数其它门控不同）；在调用时读取，便于测试动态调整。
- **适用场景**：需要审计每次热重载「改了哪些配置项」而不打印明文密钥时。
- **逐字节不变承诺**：关闭或配置无差异时返回原有的 `reloaded: ... -> ...` 行，日志逐字节不变；敏感键原文从源头不会进入摘要。

### `ARGENT_COMPACTION_EVENT` —— 压缩事件落盘

- **作用**：压缩时在 `KindCompaction` 摘要记录之后额外追加一条 `KindCondensation` 一等事件记录（读点 `internal/session/compact.go:89` 的 `compactionEventEnv` / `compactionEventEnabled`），载荷为 `CondensationEvent.Marshal()` 的原始字节（保留 CJK 原文与稳定字段顺序），含本轮被遗忘的记录 ID、摘要文本与切分点，使遗忘行为可解释、可复现。该 kind 被 `messageEntries` 忽略，不进入模型可见对话。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。每次调用读取环境，便于测试用 `t.Setenv` 精确控制。
- **适用场景**：需要审计压缩「丢了哪些记录」、复现上下文丢失问题时。
- **逐字节不变承诺**：关闭时整段跳过，压缩只写入原有的 `KindCompaction` 与保留记录，转录字节与旧版逐字一致。

### `ARGENT_TOOL_PRUNE` —— 工具结果压力裁剪

- **作用**：在预算投影之前对超长工具结果做压力裁剪（读点 `internal/session/session_message.go:24` 的 `toolPruneEnv` / `toolPruneEnabled`）：保留头部与尾部上下文，中间替换为 `… [middle pruned, N runes] …`（N 为被裁掉的 rune 数）。仅处理 `Role == "tool"` 的结果，按 rune 切片不切坏多字节字符，且对已裁剪结果幂等。固定顺序 prune → project → ReconcileToolPairs；预算复用 `ARGENT_BUDGET_PROJECTION_BUDGET` 或默认 32000。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：工具会产生超长输出、希望在进入预算投影前先缩短结果时。
- **逐字节不变承诺**：关闭时不执行 `PruneToolResults`，直接走原有的投影/配对路径，消息流逐字节不变。

### `ARGENT_IMAGE_INPUT` —— 图像输入

- **作用**：从用户原文（不是注入的 review/context 前缀）扫描 `@图片路径` 引用，读取工作区内合法的图片文件并构造 `llm.ImageInput` 列表（原语 `internal/llm/image_input_wire.go` 的 `ImageInputsFromPrompt(prompt, root, maxBytes)`），经 `agent.Options.InitialImages` 注入首条用户消息。首条消息由 `llm.BuildImageMessage` 构造为带图像 part 的 content 数组；无图时保持裸文本。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **配套约束**：只接受相对路径且不得逃出工作区根目录，扩展名限 png/jpg/jpeg/gif/webp，单文件上限 20 MiB（`imageInputMaxBytes`）；坏路径、越界、缺图、超限均被静默跳过，不使运行失败。CJK 路径按 UTF-8 原样处理。
- **适用场景**：需要把本地截图/设计稿直接交给模型辨识时（当前仅 OpenAI chat completions 协议能真正承载图像 part；Anthropic Messages / OpenAI Responses 会退化为 `[image]` 占位）。
- **逐字节不变承诺**：门控关闭时零调用 `ImageInputsFromPrompt`，`InitialImages` 为零值 nil，首条用户消息走原纯文本路径，请求体、事件流与会话 JSONL 逐字节不变。

---

## 四、其它独立门控速查

下列开关同样为独立环境变量、默认关闭，列出以增强发现性（开启值除特别说明外均为首尾空白修剪后等于 `1`）。完整登记以 `internal/cli/optin_metadata.go` 的 `IndependentOptIns` 表为准：

| 环境变量 | 作用摘要 |
|---|---|
| `ARGENT_INSPECTION` | 安全失败检查层总开关；关闭时安全链裸链运行，Guardian 绝不生效。 |
| `ARGENT_GUARDIAN` | Guardian 自身开关；须与 `ARGENT_INSPECTION=1` 同时开启且次要模型非空才生效（两开关为「与」关系）。 |
| `ARGENT_GUARDRAILS` | 护栏运行时模式（如 `enforce` / `shadow`），由 `normalizeGuardrailMode` 归一化。 |
| `ARGENT_AUTO_DECIDE` | 自动决策行为；仅 `=1` 改变行为，默认关闭。 |
| `ARGENT_BOOT_TOPO` | 启动拓扑校验：用依赖图推导构造顺序并与历史顺序比对，漂移则 fail-closed。 |
| `ARGENT_CACHE_WARM` | 提示缓存保活（cache warmer）。 |
| `ARGENT_CONFIG_SHADOW_CHECK` | 配置遮蔽来源告警输出。 |
| `ARGENT_EDIT_GUIDE` | 在系统提示中追加编辑指南。 |
| `ARGENT_EDIT_DIAGNOSTICS` | 编辑后诊断相关。 |
| `ARGENT_REPO_MAP_HINT` | 系统提示中追加「文件出现在 repo map 不等于获得编辑授权」提示。 |
| `ARGENT_VERIFY_AUTORUN_HINT` | 系统提示中追加验证自动运行提示。 |
| `ARGENT_POST_EDIT_VERIFY` | 历史遗留/未接线（仅在 `hooks.go:409` 注释中出现，无实际读取）；编辑后验证的真实开关是 opt-in `auto_verify`（`ARGENT_OPT_INS=auto_verify`）与 `ARGENT_EDIT_DIAGNOSTICS=1`。 |
| `ARGENT_APPROVAL_TTL` | 带 TTL 的审批门控；`0` 或空值关闭。 |
| `ARGENT_RECOVERY_REPLAY` | 恢复重放后缀；仅 `=1` 启用。 |
| `ARGENT_RECOVERY_DETAIL` | 恢复细节后缀：把 RecoveryPlan 的类别/断链/孤儿调用/孤儿结果作为文本追加到恢复公告；仅 `=1` 启用，关闭时公告逐字节不变。 |
| `ARGENT_SESSIONS_HEAD_TAIL` | 会话列表首尾预览。 |
| `ARGENT_NO_INTENT_SCHEMA` | 意图 schema 相关（工具注册表门控）。 |
| `ARGENT_PLAN_MODE` | 只规划模式；`=1` 时本次运行只做规划、不改文件。 |
| `ARGENT_CHECKPOINT_WORKSPACE_DIFF` | 工作区差异检查点：把 shell 等间接文件改动纳入检查点，使 `/undo` 可还原。 |
| `ARGENT_TRUST_PROJECT` | 项目级端点改写信任；`true`/`yes`/`on` 放行，默认不信任。 |
| `ARGENT_VERIFY_OUTCOME` | 验证结果分类标记；仅 `=1` 开启。 |
| `ARGENT_MCP_MEDIA_BLOB_EXT` | MCP 扩展媒体类型映射：用更广的白名单 MIME→扩展名映射替代既有映射，需真值开启。 |
| `ARGENT_MCP_BLOB_TEXT` | MCP blob 当文本：把解码后的 blob 按文本渲染，需真值开启。 |
| `ARGENT_MCP_BLOB_ARTIFACT_DIR` | MCP blob/image 产物目录；为空时落盘路径为空操作。 |
| `ARGENT_MCP_CONTENT_FILTER_MIN_PRIORITY` | MCP 内容优先级过滤阈值（正整数）；未设置时不过滤。 |
| `ARGENT_MCP_SPILL_DIR` | MCP 输出溢出目录；配置后 CallTool 的超大输出落盘。 |
| `ARGENT_MCP_SPILL_THRESHOLD` | MCP 输出溢出字节阈值（正整数），覆盖默认值。 |
| `ARGENT_MCP_PROTOCOL_VERSION` | MCP 协议版本回退值；协商失败时作为回退使用。 |
| `ARGENT_MCP_INJECTION_SCAN` | MCP 工具输出注入扫描：命中高风险注入时在原文前加一行可读警示，只警示不改写、不过滤原文；仅 `=1` 开启。 |
| `ARGENT_EVAL_LIVE` | 评测 live 门禁；仅 `=1` 允许评测走真实 provider，默认断网脚本化。 |
| `ARGENT_STREAM_RECOVERY` | 流中断后非流式回补：app 层接通 `agent.Options.EnableStreamRecovery`；仅 `=1` 开启。 |
| `ARGENT_SKILL_INJECTION_SCAN` | 技能索引注入扫描：命中高风险注入时把 desc/when 整字段替换为中性占位；仅 `=1` 开启。 |
| `ARGENT_TOOL_TIMING_HISTOGRAM` | 工具耗时直方图：按工具名累积耗时并落 TopSlow/P95 摘要日志；仅 `=1` 开启。 |
| `ARGENT_TOOL_CALL_TRACE` | 工具调用链追踪：记录调用顺序/成败/错误文本并落摘要日志；仅 `=1` 开启。 |
| `ARGENT_CONFIG_RELOAD_DIFF` | 配置热重载差异摘要：把 reloaded 日志行改为键路径差异摘要（敏感键 redacted）；原值精确 `=1`（不修剪空白）。 |
| `ARGENT_COMPACTION_EVENT` | 压缩事件落盘：压缩后追加一条 KindCondensation 记录（含被遗忘 ID/摘要/切分点）；仅 `=1` 开启。 |
| `ARGENT_TOOL_PRUNE` | 工具结果压力裁剪：保留头尾、替换中间为裁剪标记，复用投影预算；仅 `=1` 开启。 |
| `ARGENT_IMAGE_INPUT` | 图像输入：从用户原文扫描 @图片路径并作为图像 part 注入首条用户消息；仅 `=1` 开启，关闭时逐字节不变。 |

> 本表为速查，精确语义以对应源码注释为准；修改任何行为前请核对相关 `*_wire.go` / `*_test.go`。无论新增或删除独立门控，都需同步 `internal/cli/optin_metadata.go` 与本文件，否则 `TestIndependentOptInsDocumented` / `TestDocsOptInsNoGhost` 会失败。

---

## 五、逐字节不变承诺

所有 opt-in 开关共享同一设计契约：

1. **默认全关**：未显式开启时，每个开关都处于关闭状态；系统提示、事件序列、会话 JSONL、工具输出等与接线之前**逐字节一致**。
2. **fail-closed**：未知键被忽略、非布尔配置值视为关闭、空值/缺失环境按关闭处理；不会「猜」用户意图。
3. **纯解析**：解析函数（`ParseOptInEnv`、`OptInFromEnv`、`ParseOptInDisplay`、`configOptIns`、`mergeOptIns`）只读取入参，不读取进程环境、不修改入参，结果只由入参决定。
4. **稳定展示**：开关列表的展示按 key 升序稳定排序，整串写入，任何 UTF-8 字符都不会在中间被切开，同一组开关永远得到相同文本。
5. **无副作用**：关闭时观察器为 nil / 空操作，不挂接钩子、不创建目录、不改变输出字节。

因此，在不设置任何 opt-in 的情况下运行 argentgo，行为与这些特性引入之前完全相同；打开某个开关只会改变该开关所描述的那一部分行为。

---

## 六、推荐配置组合

### 长任务（多轮、大量工具输出、会触发压缩）

重点解决「上下文丢失」与「输出过大」两个痛点：

```powershell
$env:ARGENT_OPT_INS = "keep_implicit_context,preserve_summary_sections,context_pressure,compaction_fidelity"
$env:ARGENT_OUTPUT_SPILL = "1"
```

- `keep_implicit_context` + `preserve_summary_sections`：压缩后保留尾部细节与关键摘要章节。
- `context_pressure`：让模型/调用方感知上下文压力。
- `compaction_fidelity`：压缩要素缺失时告警，便于及早干预。
- `ARGENT_OUTPUT_SPILL=1`：把超大输出落盘，避免撑爆上下文。
- 可选：验证密集的任务再加 `attempt_isolation`，把失败尝试分叉到旁支。

### 安全/审计优先

```powershell
$env:ARGENT_OPT_INS = "untrusted_result,untrusted_boundary,refusal_reason"
$env:ARGENT_INSPECTION = "1"
$env:ARGENT_GUARDIAN = "1"          # 需同时 ARGENT_INSPECTION=1 且配置了次要模型
$env:ARGENT_APPROVE_DANGER = "1"    # 仅 TUI 有效
```

- `untrusted_result` + `untrusted_boundary`：把工具结果/外部内容当作不可信数据处理。
- `refusal_reason`：保留可审计的拒绝理由。
- `ARGENT_INSPECTION` + `ARGENT_GUARDIAN`：启用安全失败检查层与 Guardian 审阅。
- `ARGENT_APPROVE_DANGER`：TUI 中展示危险命令原因并预选拒绝。

### 交互体验优先

```powershell
$env:ARGENT_OPT_INS = "cli_hints,apply_patch,grep_ex"
$env:ARGENT_CLI_HINTS = "1"
$env:ARGENT_BUILTIN_SHELL = "1"     # 仅在需要内建 shell 时
```

- `apply_patch` + `grep_ex`：更结构化的编辑与更强的搜索。
- `cli_hints` 与 `ARGENT_CLI_HINTS`：分别增强输出提示与 CLI 报错修复建议（注意两者是不同开关）。
- `ARGENT_BUILTIN_SHELL`：仅在环境缺少系统 shell 时开启。

### 一次性命令（bash）

```bash
ARGENT_OPT_INS="keep_implicit_context,preserve_summary_sections,context_pressure" \
ARGENT_OUTPUT_SPILL=1 \
  argentgo "你的长任务"
```

---

查看当前实现状态与特性清单见 [Technical reference](TECHNICAL.md)；安装与快速上手见 [用户指南](USER_GUIDE.md)。
