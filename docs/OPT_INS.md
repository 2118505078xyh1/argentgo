# Opt-in 开关使用指南

argentgo 默认行为对所有可选特性**一律关闭（fail-closed）**。本文档列出全部受支持的 opt-in 开关，以及独立于注册表之外的门控环境变量。所有开关名、环境变量名均与源码逐字对应（事实来源：`internal/app/optin_registry.go`、`internal/config/optin_config.go`、`internal/cli/optin_display.go` 及各 `*_wire.go`）。

> 背景：在 R16 之前，这些开关没有任何文档记载，用户只能「裸写」`export ARGENT_OPT_INS=...`，发现性为零。本文档就是为补齐这一缺口而写。

## 目录

- [一、两条激活入口](#一两条激活入口)
- [二、14 个 opt-in 键（ARGENT_OPT_INS / optins 块）](#二14-个-opt-in-键argent_opt_ins--optins-块)
- [三、独立门控环境变量](#三独立门控环境变量)
- [四、其他独立门控变量速查](#四其他独立门控变量速查)
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
argent "你的任务"
```

bash / Git Bash：

```bash
export ARGENT_OPT_INS="keep_implicit_context,preserve_summary_sections,context_pressure"
argent "你的任务"
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

- 入口 A：`ARGENT_OPT_INS` 环境变量，可打开全部 14 个键。
- 入口 B：配置文件 `optins` 块，可打开下面标注「配置可用」的 9 个键。
- `optins` → app 运行时的映射交集（`configOptInKeyMap`，`app_config_optins.go:17-27`）只包含标注「配置可用」的 9 个键；`refusal_reason`、`untrusted_result`、`untrusted_boundary`、`compaction_fidelity`、`auto_verify` 这 5 个键**只能**通过 `ARGENT_OPT_INS` 打开。

### 两入口同时存在时

代码语义是「逻辑或」：任一来源显式开启即视为开启（`mergeOptIns`）。两个入口都未设置时所有键保持默认关闭（fail-closed），逐字节不变。

---

## 二、14 个 opt-in 键（ARGENT_OPT_INS / optins 块）

下表给出全集。默认状态全部为**关闭**；「配置可用」指该键是否落在 `configOptInKeyMap` 的交集内（不在交集的键只能用环境变量打开）。

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

以下开关**不属于** 14 键注册表，而是通过各自独立的环境变量控制（因此不能写进 `ARGENT_OPT_INS`）。它们的共同点：默认关闭，且关闭时相关行为逐字节不变。

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

- **作用**：`argent --tokens` 报告追加一行 `cache hint: ...`，说明当前端点的提示缓存能力（静态判定，不发起请求）。
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

---

## 四、其他独立门控变量速查

下列开关同样为独立环境变量、默认关闭，列出以增强发现性（开启值除特别说明外均为首尾空白修剪后等于 `1`）：

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
| `ARGENT_SESSIONS_HEAD_TAIL` | 会话列表首尾预览。 |
| `ARGENT_NO_INTENT_SCHEMA` | 意图 schema 相关（工具注册表门控）。 |

> 本表为速查，精确语义以对应源码注释为准；修改任何行为前请核对相关 `*_wire.go` / `*_test.go`。

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
  argent "你的长任务"
```

---

查看当前实现状态与特性清单见 [Technical reference](TECHNICAL.md)；安装与快速上手见 [用户指南](USER_GUIDE.md)。
