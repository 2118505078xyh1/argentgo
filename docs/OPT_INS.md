# Opt-in 开关使用指南

argentgo 默认行为对所有可选特性**一律关闭（fail-closed）**。本文档列出全部受支持的 opt-in 开关，以及独立于注册表之外的门控环境变量。所有开关名、环境变量名均与源码逐字对应（事实来源：`internal/app/optin_registry.go`、`internal/config/optin_config.go`、`internal/cli/optin_display.go` 及各 `*_wire.go`）。

> **默认开的例外**：`ARGENT_BUILTIN_SHELL`、`ARGENT_TOOL_ARG_COERCION`、`ARGENT_PROGRESS_RECOVERY` 三个开关默认开启（均可用 `=0` 紧急关闭）。依据是用户 2026-11-22 的判定标准：对用户体验更好 / 能让 AI 更强就开，不为保守而保守。三者都保留 `=0` 逃生阀，关闭时逐字节不变。

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

- 入口 A：`ARGENT_OPT_INS` 环境变量，可打开集中注册表的全部 15 个键；此外它还能携带**双激活面**独立门的等价键（当前为 `jobs`，见第三节），所以「15 键」只是注册表自身的数量，并非该变量能接受的全部键。
- 入口 B：配置文件 `optins` 块，可打开下面标注「配置可用」的 10 个键。
- `optins` → app 运行时的映射交集（`configOptInKeyMap`，`app_config_optins.go:17-27`）只包含标注「配置可用」的 10 个键；`refusal_reason`、`untrusted_result`、`untrusted_boundary`、`compaction_fidelity`、`auto_verify` 这 5 个键**只能**通过 `ARGENT_OPT_INS` 打开。

### 两入口同时存在时

代码语义是「逻辑或」：任一来源显式开启即视为开启（`mergeOptIns`）。两个入口都未设置时所有键保持默认关闭（fail-closed），逐字节不变。

---

## 二、集中注册表 opt-in 键（ARGENT_OPT_INS / optins 块）

下表给出集中注册表（`internal/app/optin_registry.go`）的 15 个键。默认状态全部为**关闭**；「配置可用」指该键是否落在 `configOptInKeyMap` 的交集内（不在交集的键只能用环境变量打开）。

> 注意：这张表是**注册表**的全集，不是 `ARGENT_OPT_INS` 能接受的全部字符串。独立门 `jobs` 也接受 `ARGENT_OPT_INS=jobs`（双激活面），但它不在 15 键注册表内，故列在第三节。总开关数因此不能用「15」概括。

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
| 15 | `codegraph` | 代码图谱 | 关闭 | 是 | 交互 |

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
- **如何开启**：`export ARGENT_OPT_INS="auto_verify"`。该键**不在** `configOptInKeyMap`（`app_config_optins.go:17-27`）交集内，配置文件无法打开，**只能**通过 `ARGENT_OPT_INS` 开启。另注：doctor 中的 `ARGENT_EDIT_DIAGNOSTICS=1` 是另一条独立的编辑后诊断路径，与本 opt-in 做或运算，但它不属于本 15 键注册表。
- **适用场景**：希望编辑后自动跑一遍验证、把失败信息即时回灌给模型做修正时。
- **逐字节不变承诺**：未显式开启时 `autoDiagnostics` 为零值、`verifyFuncFor` 走原有分支，验证与诊断路径逐字节不变；即便开启，若无 `go.mod` 或未命中合成条件也回落到原有分支。

### 15. `codegraph`（代码图谱）

- **作用**：索引 + 查询工具作为一个整体一起启用——构建本地 Go 代码图谱，并向工具注册表注入只读的 `codegraph_query(name)`，可查询符号定义与引用位置（结果附源码位置与提取规则，便于核对证据链）。装配点为 `internal/app/app_codegraph_adapter.go` 的 `wireCodeGraph` / `toolset.SetCodeGraph`，工具注册在 `wireOptInTools`，系统提示追加在 `app_execute.go`。
- **默认状态**：**关闭**。默认关时不构建索引、不注册 `codegraph_query`、不注入提示，系统提示/工具列表/事件流/JSONL **逐字节不变**。
- **如何开启**：`export ARGENT_OPT_INS="codegraph"`，或在 `optins` 块中设 `"codegraph": true`。这是集中键而非独立环境变量。
- **适用场景**：需要在大型 Go 仓库里定位符号定义与引用、且希望每条关系都带可解释来源（规则、置信度、源码行）时。索引为本地静态分析、只读，不联网。
- **逐字节不变承诺**：未显式开启时零开销——不遍历工作区、不调用 `SetCodeGraph`、不改 `Specs()`。即便开启，若索引构建失败也只在 stderr 写一行 warn 并把工具降级为不可用，绝不崩溃或改变既有运行路径。

---

## 三、独立门控环境变量

以下开关**不属于**集中注册表的 15 个键，而是通过各自独立的环境变量控制。多数只能通过独立 env 打开；`jobs` 是例外——它同时接受 `ARGENT_OPT_INS=jobs`（双激活面）。它们的共同点：默认关闭，且关闭时相关行为逐字节不变。本节先详述常用门控，末尾补齐后续批次加入的门控；其余门控在第四节速查。注意 `ARGENT_BUILTIN_SHELL` 与本节最末的 `ARGENT_TOOL_ARG_COERCION`、`ARGENT_PROGRESS_RECOVERY` 是默认开的例外——它们用 `=0` 关闭，而非用 `=1` 开启。

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

### `ARGENT_BUILTIN_SHELL` —— 自研内建 shell（默认开）

- **作用**：使用纯标准库实现的自研内建 shell 解释器执行命令（输出仍走既有清洗/裁剪管线，非零退出转为含退出码的 error）。不支持的语法由 `Supported()` 在整条命令级回退系统 shell；运行期若冒出 `ErrUnsupported`，`ShouldFallback` 同样用原始命令行整条回退，杜绝重定向/写入等副作用重复。
- **关闭值**：首尾空白修剪后等于 `0` 时紧急关闭（回到系统 shell 路径）。其它值或未设置都视为开启。
- **适用场景**：默认即使用内建解释器；仅在需要完全回到系统 shell 语义（例如依赖内建子集之外的复杂语法）时设 `ARGENT_BUILTIN_SHELL=0`。
- **逐字节不变承诺**：`ARGENT_BUILTIN_SHELL=0` 时命令走系统 shell 路径，与切换前逐字节不变；`0` 之外的默认路径改由内建解释器执行受支持子集内的命令，输出仍走既有的清洗/裁剪管线。

### `ARGENT_APPROVAL_DIFF` —— 审批卡 diff 展示

- **作用**：TUI 审批卡渲染有界的增删行摘要（`+N/-M in path`），并限制行数与路径宽度，避免长路径撑破弹窗；预览按字素边界截断，CJK 文件名不会被切坏。
- **开启值**：首尾空白修剪后等于 `1`（由 `ConfigureApprovalDiff` 注入的 env 判定）。
- **适用场景**：批准文件编辑前希望一眼看到改了哪些文件、增删大致行数时。
- **逐字节不变承诺**：门控关闭时无论审批请求是否带 diff 数据，弹窗帧字节都与接线前逐字节相同；`name` / `args` 始终原样传递。

### `ARGENT_HOOK_VERDICT` —— 钩子权限裁决折叠

- **作用**：让 hook stdout 的 `permissionDecision` 裁决参与工具审批：`deny`（含 Claude 嵌套形态、exit 0）直接拦截工具调用并给出可读理由；`allow` 仅在无 exit 2 拦截时可解除，绝不解除 exit 2 的硬阻断；同时注入审计中间件与 session/cwd 补齐。
- **开启值**：修剪后等于 `1`。
- **适用场景**：使用返回 `{"permissionDecision":"deny"}` 的 Claude 兼容钩子，希望在工具执行前即时拦截时。
- **逐字节不变承诺**：门控关闭时 `permissionDecision` 被完全忽略，分发路径与裁决结果与接线前逐字节相同。

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

### `ARGENT_PERSISTENT_SHELL` —— 持久化 shell 会话

- **作用**：开启后 bash 工具的命令在一条长驻 shell 会话中执行（原语 `internal/bash`），从而跨调用复用 `cd`、`export`、虚拟环境激活等 shell 状态。装配点为 `internal/app/app_shell_wire.go` 的 `wireShellSessions`：读环境变量 → 建立 `bash.Manager` → 经 `SetShellSessions` 注入本次运行的全部工具注册表；退出时 `Close()`（幂等）回收会话，并且按 LIFO 先于后台作业与工具集关闭。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **shell 种类**：POSIX 平台用 `bash`；Windows 优先 `bash`，找不到时退到 `powershell`（持久化内核阶段1不支持 `cmd`）。
- **阶段1限制**（务必知悉）：
  1. **不支持 stdin 消费型命令**：如无参数的 `cat` 会因没有 stdin 输入流而无法正常工作；需要管道输入请改用 `printf ... | cmd` 或写入临时文件。
  2. **只杀直接 shell，不清理后代**：超时或关闭时只终止直接 shell 进程，不杀其派生的子进程；后代进程可能短暂残留并写入已关闭的管道。
  3. **stderr 被丢弃**：持久化会话只把 stdout 作为命令输出返回，stderr 不进入工具结果（避免混入哨兵切分）。
  4. **末行无换行残行随哨兵丢弃**：命令输出若最后一行没有换行符，该残行会与探针哨兵一起被切掉，不会出现在结果中。
  5. 单命令超时（30 秒）或空闲超时（5 分钟）会销毁会话，下条命令自动重建。
- **适用场景**：需要 shell 状态在多次 bash 调用间存活（连续 `cd`、激活 venv、设置环境变量）时。
- **逐字节不变承诺**：默认关闭时不创建 Manager、不调用 `SetShellSessions`、不安装关闭钩子，命令仍走原有系统 shell 路径，系统提示/事件流/JSONL 与工具输出逐字节不变。

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

### `ARGENT_MCP_HEALTH` —— MCP 运行时健康诊断

- **作用**：开启后在 `internal/app/mcp.go` 的 connect 路径给每个 MCP 服务的 `Spec.HealthTracking` 赋值，使 `internal/mcp` 的 `HealthTracker` 在工具发现成功/失败时记录快照；工具集用 `HealthAggregator` 在连接成功时 `Track`、关闭时 `Untrack` 并 `PollReporters` 聚合，最后把报告写入存储目录下的 `mcp-health.json`。下一次 `argentgo doctor` 读取该文件，对每个不健康服务在 `warnings:` 旁路追加一条 `mcpHealth` warn（含服务名、连接状态与 LastError），不新增 check、不计数、不改退出码。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **适用场景**：MCP 服务较多、希望在 doctor 自检里直接看到哪台服务发现失败及其错误文本时。快照只落存储目录（默认 `~/.argentgo`），不污染工作区；错误文本经脱敏后才进入诊断输出。
- **逐字节不变承诺**：关闭时工具集不分配聚合器、不给 Spec 赋值、`mcp` 客户端不挂接 tracker、不写任何文件，doctor 也不读取快照，文本与 JSON golden、退出码均与接线之前逐字节一致。

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

### 交互 / 进程 / 本地化（TODO #133 / #72 / #29 / #52）

- **`ARGENT_ACP_DECODE=1`**（#133）：`internal/proc` 的 stdout/stderr 包一层增量解码器——UTF-8 合法字节直通零开销；遇到完整非法序列时调用 Windows `MultiByteToWideChar` 按当前 ACP 解码，并锁存到 EOF 不再切回；按行判定避免污染相邻行，跨分块不完整 rune 暂存。非 Windows 平台为空实现。默认关闭时不插入任何包装。
- **`ARGENT_HOOK_DAEMON=<受信任文档路径>`**（#72）：`internal/hooks` 挂载常驻外部进程，经 stdio 换行分隔 JSON-RPC 2.0 通信；文档须过与 `LoadDocument` 一致的 SHA-256 信任门，未授信/格式错误/启动失败都告警并降级，不派生后台进程。守护进程的 Outcome 折入既有 `Summarize + applyVerdict` 管线，裁决/附加上下文语义完全复用。app 层在 `newHookBridge` 构造 dispatcher 时挂载、`bridge.end()` 时回收。未设置时零文件 I/O。
- **`ARGENT_PTY_INTERACTIVE=1`**（#29）：注册 `argentgo pty [-C dir] [--cols/--rows/--max-bytes] -- <cmd>` 子命令，在真实 ConPTY/ptmx 中运行程序；读循环串接终端查询序列剥离 → UTF-8 增量解码 → 字节裁剪。子命令在 `init()` 按 env 动态注册，默认不注册，usage/help/completion 逐字节不变。
- **`ARGENT_LOCALE=en-US`（含 `en`/`en_us`/`English` 别名）**（#52）：原生桌面窗口把硬编码中文文案切到英文；共享键的英文值与 `internal/i18n` 目录一致（native 自包含内联，遵守 native 不得 import internal 的分层门禁）。空值、中文及其它值一律保留原中文，逐字节不变。

### `ARGENT_APPROVAL_PERSIST` —— 持久化审批规则

- **作用**：开启后在审批链上叠加 `FileBackedScopedApprover`（TODO #127），把 `always` 授权与粘性 `deny` 规则落盘到 `<storage>/approvals.jsonl`，跨会话复用审批决定。作用域取工具名（接线传 `scopeFor=nil`），**只记录工具名/注入的命令前缀，绝不记录自由文本参数或反馈文字**。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **fail-closed**：规则文件损坏或无法信任时审批器进入失败态——不兑现任何规则、也不再写入，后续审批退回 inner 审批器的正常询问路径。
- **逐字节不变承诺**：关闭时 app 层不构造该审批器，零文件 I/O，审批链行为逐字节不变。

### `ARGENT_EXEC_SESSION` —— 统一 exec 会话

- **作用**：bash 工具新增 `session_id`/`write_stdin`/`yield_time_ms`/TTY 字段（TODO #120，学自 codex），并在 app 层按开关注册 `write_stdin` 工具。`internal/jobs` 的会话注册表持有有界输出环（1 MiB）、stdin 限长（8000 字节）、空闲回收（5 分钟）与按会话精确 kill；TTY 会话走 ConPTY/ptmx。
- **开启值**：仅值为 `1`（允许首尾空白）时生效。
- **逐字节不变承诺**：关闭时 bash 的 schema、参数解析、执行路径与默认工具 Specs() 全部逐字节不变。

### `ARGENT_CONTEXT_CALIBRATION` —— 上下文计量校准

- **作用**：opt-in 地把提供方实测 token 锚点纳入压缩判定（TODO #104，学自 opencode/cline/pi）：`CalibrationFactor(measured, estimated)` 返回低估系数并钳到 `[1,4]`（实测 120k / 估算 60k → 2，严重低估 → 4），`CalibratedEstimate` 放大估算后再做既有 1.3 倍膨胀与阈值比较；`ReservedCeiling` 给出扣余量后的上限（`max(15%, 16000)`），`ShrinkWindow` 提供逐级缩减比例 `[0.85,0.7,0.5,0.35]`。接线取最近助手的原生 `totalTokens` 作实测锚点，无锚点时退回原判定。
- **开启值**：仅值为 `1`（允许首尾空白）时启用；默认关闭时估算、阈值与比较逐字节不变。

### `ARGENT_BACKGROUND_COMPACT` —— 后台预压缩

- **作用**：app 层包装 `agent.Compactor`（TODO #105，学自 opencode）：agent 循环在每次工具批次后经可选 `CompactObserver` 把消息与阈值交给 app；当压力达到阈值的 80% 时，后台 goroutine 提前调用内层 compactor 算出压缩视图并缓存。到阈值时若缓存存在且锚点后新增消息不超过 24 条，直接交付缓存（零阻塞）；tail 过长则丢弃并同步回退；连续失败 2 次后暂停预计算。
- **开启值**：真值（`1`/`true`/`yes`/`on`）；默认关闭时不包装 compactor、不注册 observer，同步压缩路径逐字节不变。
- **一致性**：窗口由 `agent.CompactWindow(threshold)` 统一计算，避免与同步路径公式漂移；agent 包仍不依赖 session。

### `ARGENT_FOVEATED_ARCHIVE` —— foveated 多保真归档（纯文本版）

- **作用**：`Messages()` 的读路径改用多保真视图（TODO #134 第一步，学自 oh-my-pi 的 `planArchive`）：两端保留逐字不变的高保真消息，中段按预算从较新往较旧保留少量稀疏消息（角色 + 单行摘要，保留 ID/ToolCallID/Data），最旧的中段被丢弃并在头段与稀疏段之间插入一个可解析的 `system` gap 标记 `[… N messages omitted …]`（N 为消息条数，非 rune）。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。预算复用 `ARGENT_BUDGET_PROJECTION_BUDGET`（缺省 32000 rune）。
- **配对安全**：稀疏与 gap 的切点都落在 `buildProjectionPlan` 的"配对单元"边界——assistant 的工具调用与其结果同留同弃，绝不把 gap 插在调用与结果之间造成非法消息序列。
- **压缩会话**：接线在 `Messages()` 的压缩分支之前，基于活动路径的**全量消息**重建视图（压缩分支会先把 head 截到 `compaction.Keep`，旧端已丢失），并保留最新压缩摘要作为开头的 `system` 消息。
- **零 wire 变化**：不新增记录种类、不落盘、不改 `llm.Message` 结构；纯文本版不涉及位图帧与读者能力回放（第二步）。
- **逐字节不变承诺**：关闭时本分支完全不进入，`Messages()` 原有的压缩/非压缩路径逐字节不变。

### `ARGENT_IMAGE_INPUT` —— 图像输入

- **作用**：从用户原文（不是注入的 review/context 前缀）扫描 `@图片路径` 引用，读取工作区内合法的图片文件并构造 `llm.ImageInput` 列表（原语 `internal/llm/image_input_wire.go` 的 `ImageInputsFromPrompt(prompt, root, maxBytes)`），经 `agent.Options.InitialImages` 注入首条用户消息。首条消息由 `llm.BuildImageMessage` 构造为带图像 part 的 content 数组；无图时保持裸文本。
- **开启值**：首尾空白修剪后等于 `1`；未设置、空或其它值关闭。
- **配套约束**：只接受相对路径且不得逃出工作区根目录，扩展名限 png/jpg/jpeg/gif/webp，单文件上限 20 MiB（`imageInputMaxBytes`）；坏路径、越界、缺图、超限均被静默跳过，不使运行失败。CJK 路径按 UTF-8 原样处理。
- **适用场景**：需要把本地截图/设计稿直接交给模型辨识时（当前仅 OpenAI chat completions 协议能真正承载图像 part；Anthropic Messages / OpenAI Responses 会退化为 `[image]` 占位）。
- **逐字节不变承诺**：门控关闭时零调用 `ImageInputsFromPrompt`，`InitialImages` 为零值 nil，首条用户消息走原纯文本路径，请求体、事件流与会话 JSONL 逐字节不变。

### `ARGENT_TOOL_ARG_COERCION` —— 工具参数强转（默认开）

- **作用**：模型偶尔把数字或布尔写成字符串（如 `{"count":"3"}`）。开启后由 `agent.effectiveToolArgs` 做保守强转，且**审批摘要与工具执行使用同一份强转后的参数字节**，消除"批准 A、执行 B"的不一致。语义合法的参数不受影响。
- **关闭值**：首尾空白修剪后等于 `0` 时关闭（审批与执行都回到模型产出的原始字节）；其它值或未设置都视为开启。
- **适用场景**：希望工具调用对模型常见的字符串化数字/布尔更鲁棒、同时保证所见即所批时。
- **逐字节不变承诺**：`=0` 时强转分支整体短路，审批摘要与执行参数均为原始字节，与接线之前逐字节不变；审批/执行一致性回归测试同时守护开启路径。

### `ARGENT_PROGRESS_RECOVERY` —— 轮次内进度恢复（默认开）

- **作用**：①写侧：流式文本每累积 1 KiB、每次工具调用时，由 app 注入的 `ProgressSink` 把轮次内进度（轮次号、未完成输出、进行中工具）持久化为 `progress` 记录（fsync）；②读侧：恢复路径调用 `session.RecoverProgress`（两道过期检查），把有效进度作为用户可见的 `EventNotice` 公告发布。**进度文本绝不进入模型历史，也绝不触碰 `NeedsConfirmation`**（安全语义仍由恢复分类唯一决定）。
- **关闭值**：首尾空白修剪后等于 `0` 时关闭；其它值或未设置都视为开启。
- **适用场景**：长轮次运行被强杀（`kill -9`，走不到任何失败边界）后，重启时仍能看到上次中断处的未完成输出与进行中的工具。纯进度崩溃若被分类为干净会话（`Notice==""`），也会单独发一条进度公告。
- **逐字节不变承诺**：`=0` 时不注入 `ProgressSink`、不调用 `RecoverProgress`，请求体/事件流/JSONL 逐字节不变；开启时进度也只走 `EventNotice`，不注入 `SeedContext`、不改写消息序列与确认门。

---

## 四、其它独立门控速查

下列开关同样为独立环境变量、默认关闭，列出以增强发现性（开启值除特别说明外均为首尾空白修剪后等于 `1`）。完整登记以 `internal/cli/optin_metadata.go` 的 `IndependentOptIns` 表为准：

| 环境变量 | 作用摘要 |
|---|---|
| `ARGENT_INSPECTION` | 安全失败检查层总开关；关闭时安全链裸链运行，Guardian 绝不生效。 |
| `ARGENT_GUARDIAN` | Guardian 自身开关；须与 `ARGENT_INSPECTION=1` 同时开启且次要模型非空才生效（两开关为「与」关系）。 |
| `ARGENT_GUARDRAILS` | 护栏运行时模式（如 `enforce` / `shadow`），由 `normalizeGuardrailMode` 归一化。 |
| `ARGENT_PATCH_POLICY_PATHS` | =1 时从 apply_patch 的 `input` 正文解析真实目标文件，逐条走 denyPaths/allowPaths 等路径策略（拒绝时零写入）；默认关闭时补丁正文不参与路径规则，安全链逐字节不变。 |
| `ARGENT_TOOL_ARG_FEEDBACK` | =1 时，模型发出的工具参数非空却不是合法 JSON 则不执行工具，把原始片段（脱敏限长）作为错误结果回灌，让模型重发；默认关闭时仍回退为 `{}` 后派发，逐字节不变。 |
| `ARGENT_MULTIMODAL_TRIM` | =1 时把带图消息的 `ContentParts`（文本 part + 图像 part 按 detail 取保守 token 上限）计入 token 估算，使上下文压力与裁剪决策准确；默认关闭时带图消息仍只算框架开销，逐字节不变。 |
| `ARGENT_GREP_PER_FILE` | 正整数时限制 grep/grep_ex 单文件匹配数：某文件达上限后跳过其余行但继续遍历其它文件，避免一个热文件吃满全局预算；默认 0 禁用时与旧行为逐字节一致。 |
| `ARGENT_BLOCK_GENERATED_EDITS` | =1 时 edit/write/apply_patch 在写入前按文件名模式（如 `zz_generated.*`、`*.pb.go`、`*.gen.ts`）与头部标记（`@generated`/`Code generated`/`DO NOT EDIT`）拒绝编辑生成物，并指引改源文件后重新生成；默认关闭时编辑行为逐字节不变。 |
| `ARGENT_ELIDE_DATA_URLS` | =1 时在工具结果 head/tail 裁剪之前把整段 base64 data URL 原子替换为保留 MIME 与字符数的占位（并治愈遗留前缀碎片），避免切出无法解码的坏图引用；默认关闭时裁剪逐字节不变。 |
| `ARGENT_HOOK_REWRITE` | =1 时应用 PreToolUse 钩子声明的 `updatedInput`/`updatedOutput`：输入改写与审批、执行使用同一份字节，输出改写替换回灌给模型的正文；默认关闭时工具参数与输出逐字节不变。 |
| `ARGENT_COMPACTION_HOOK` | =1 时压缩前触发 PreCompact（返回 blocked 可阻止压缩并公告原因，零写入）、压缩落盘后触发 PostCompact；默认关闭时压缩路径无任何钩子调用，逐字节不变。 |
| `ARGENT_COMPACTION_TODO` | =1 时压缩把会话 `todos.json` 以 `[x]`/`[>]`/`[ ]` 行写入 `Compaction.Todo` 并镜像进摘要，使恢复后的模型知道任务进度；默认关闭时压缩记录逐字节不变。 |
| `ARGENT_BRANCH_SUMMARY` | =1 时 Checkout 为被弃分支生成确定性摘要并追加到新活动分支，使模型知道放弃路径上做过什么；默认关闭时 checkout 与消息投影逐字节不变。 |
| `ARGENT_READ_BEFORE_WRITE` | =1 时 read 成功后登记文件快照，edit/write 在检测到外部改动（size/modtime 变化或文件消失）时拒绝写入并提示重读；默认关闭时无账本、无检查，工具行为逐字节不变。 |
| `ARGENT_MCP_ANNOTATION_APPROVAL` | =1 时按 MCP annotations 把声明 destructiveHint 且非 readOnly 的工具纳入强制审批（即便策略 Allow 也询问）；默认关闭时仅按工具名白名单决定审批，逐字节不变。 |
| `ARGENT_MCP_GATEWAY` | =1 时只公布一个固定的 `mcp` 工具：`mcp(server=X)` 以文本返回该服务工具清单、`mcp(server=X,tool=Y,arguments=...)` 执行；服务首次使用时才连接，工具列表整会话不变，稳定缓存前缀。执行时派生 `mcp__server__tool` 名做只读沙箱拦截、readOnly/destructive 审批（用 per-call 链副本避免竞态），客户端按 RawName 调用。list_changed 不再重建 spec 列表。默认关闭时全部 MCP 接线逐字节不变。迁移自 killi 决策 11。 |
| `ARGENT_EVENT_LOG` | =1 时把 hub 事件以 JSONL 落盘到存储目录，SSE 支持 `Last-Event-ID` 在超出内存回放窗口后从磁盘续放；默认关闭时纯内存、SSE 帧无 `id:`，逐字节不变。 |
| `ARGENT_FAILURE_SIGNALS` | =1 时从工具结果与消息文本提取失败信号（is_error/非零 exit/timed out/失败词）并入压缩摘要的 Failures；默认关闭时不提取，压缩逐字节不变。 |
| `ARGENT_SUMMARY_EMPTY_RETRY` | =1 时压缩摘要为空则少丢一条消息重算一次摘要（最多一次）；默认关闭时使用原摘要，压缩逐字节不变。 |
| `ARGENT_MCP_INJECTION_MEDIUM` | =1 时 MCP 工具输出的中风险注入命中也加可见警示前缀；默认关闭时仅高风险警示，输出逐字节不变。 |
| `ARGENT_MCP_TOOL_CACHE_TTL` | 正整数秒时为 MCP 工具发现结果启用带 TTL 的缓存；默认 0 禁用时每次重新发现，行为不变。 |
| `ARGENT_PARSE_REGRESSION_GUARD` | =1 时 edit/write/apply_patch 写入前检查 Go 语法：原本合法的文件若改后不合法则拒绝（整批零写入）；默认关闭时不检查，写入逐字节不变。 |
| `ARGENT_APPROVAL_PREVIEW` | =1 时写类工具的审批提示附带 `+N/-M` 行数摘要；不改变审批判定，默认关闭时提示逐字节不变。 |
| `ARGENT_MCP_OUTPUT_LIMIT_BYTES` | 正整数字节时单次 MCP 输出超限保留头尾并附恢复标记；缺省 0 时保持旧行为，逐字节不变。 |
| `ARGENT_VERIFY_WRITE` | =1 时写入后重读文件比对实际字节，漂移时附按落盘字节重算的 hashline 锚点警告；默认关闭时零额外读取，结果逐字节不变。 |
| `ARGENT_LSP_SANITIZE_ENV` / `ARGENT_LSP_KILL_TREE` | 分别启用 LSP 客户端的环境净化与进程树整树终止；默认关闭时按原方式拼环境、只杀直接进程。 |
| `ARGENT_PLUGIN_OUTPUT_SCHEMA` | =1 时声明 OutputSchema 的插件工具输出须为合法 JSON，违反则转模型可读错误；默认关闭时不校验，内置工具不受影响。 |
| `ARGENT_HOOK_LIFECYCLE` | =1 时把 agent 轮次边界/中断/子 agent 结束事件映射为 TurnStart/TurnEnd/Interrupt/StopFailure/SubagentStop hook；默认关闭时无额外 hook 调用。 |
| `ARGENT_AUDIT_SANITIZE` | =1 时审计日志写入前用 `SanitizeForLog` 脱敏 reason 中的 `VAR=值`/口令状串；默认关闭时只走既有 RedactJSON。 |
| `ARGENT_TUI_BADGE_BG` / `_INK` / `_ALT_INK` | 三者同时提供时为徽章渲染提供 hex 配色，由 `LegibleInk` 按对比度选墨色；默认未配置时帧逐字节不变。 |
| `ARGENT_MCP_CALL_PROFILE` | =1 时声明 readOnlyHint 的 MCP 工具按每次调用注入只读 profile（拒绝修改类工具，只收紧不放宽）；默认关闭时行为不变。 |
| `ARGENT_TELEMETRY` | =1 时用统一 Span/Counter/Histogram 契约记录每轮时间；默认使用零分配 NOOP，事件/日志/会话字节不变。 |
| `ARGENT_RETRY_OMIT` | =1 时重试前把失败尝试以 ContextEdit 永久从模型可见上下文省略，并按指数退避等待（可被 ctx 取消）；关闭时逐字节不变。 |
| `ARGENT_LIVE_STEER` | =1 且 Steer 非 nil 时，Responses SSE 事件边界检查排队纠正文本并上报 accepted/deferred；不改线协议字节；关闭时零开销。 |
| `ARGENT_SUSPENDED_JOB` | =1 时 Windows 上以 CREATE_SUSPENDED 启动、先入作业对象再恢复，入作业失败即终止未运行的进程，封住孙进程逃逸窗口；默认关闭时逐字节不变。 |
| `ARGENT_STAGED_APPROVAL` | =1 时用 stdio 交互的 StagedApprover 启用 once/always/reject/esc 状态机与「始终允许」作用域记忆；默认关闭时走原审批路径，逐字节不变。 |
| `ARGENT_AUTO_DECIDE` | 自动决策行为；仅 `=1` 改变行为，默认关闭。 |
| `ARGENT_BOOT_TOPO` | 启动拓扑校验：用依赖图推导构造顺序并与历史顺序比对，漂移则 fail-closed。 |
| `ARGENT_CACHE_WARM` | 提示缓存保活（cache warmer）。 |
| `ARGENT_LOOP_DETECTION` | 真值时按调用签名（name+input+output 的 SHA-256）检测重复工具循环，命中注入一条模型可读的纠正提示（软提示，不硬停）；默认关闭时不收集事件、不调用检测器，消息/事件/会话记录逐字节不变。迁移自 killi repeat breaker。 |
| `ARGENT_REQUEST_SNAPSHOT` | 真值时把实际发送给提供方的线协议请求字节追加一条 `request_snapshot` 会话记录（含 SHA-256 校验），用于复现与审计；该 kind 不进入对话视图，默认关闭时请求字节与记录逐字节不变。注意快照含完整 system prompt 与工具结果，可能含敏感文本，需自行控制会话文件权限。迁移自 killi 决策 21。 |
| `ARGENT_SUBAGENT_SHARED_PROMPT` | 真值时子代理以父级 system prompt 为前缀（首请求复用已缓存字节），角色约束与任务标签追加在末尾，不破坏缓存前缀；默认关闭时子代理沿用原有 subagentTemplate，行为逐字节不变。迁移自 killi 决策 20。 |
| `ARGENT_COMPACT_DRAIN` | 真值时，上下文溢出压缩后若仍超预算，按缓存前缀逐条淘汰最旧的非系统消息（单次最多 50 条，保留 system 与末尾提示）再重放；默认关闭时溢出恢复路径逐字节不变。项目 TODO #126。 |
| `ARGENT_RESUME_BYTE_REPLAY` | 真值且 `ARGENT_REQUEST_SNAPSHOT=1` 时，"中断提示"类 resume 用最后一条请求快照的原始线协议字节重发首请求，命中提供方缓存前缀。快照须在活动路径上、其后无 assistant 回复与压缩、协议/模型与本次一致，否则静默回退语义重建。走三协议可选的 RawChatter.ChatRaw，默认关闭时请求字节逐字节不变。迁移自 killi 决策 21。 |
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
| `ARGENT_MCP_HEALTH` | MCP 运行时健康诊断：连接时挂接 HealthTracker、关闭时快照落盘；doctor 以 `mcpHealth` warn 报出不健康服务名与 LastError；仅 `=1` 开启，关闭时零输出。 |
| `ARGENT_EVAL_LIVE` | 评测 live 门禁；仅 `=1` 允许评测走真实 provider，默认断网脚本化。 |
| `ARGENT_STREAM_RECOVERY` | 流中断后非流式回补：app 层接通 `agent.Options.EnableStreamRecovery`；仅 `=1` 开启。 |
| `ARGENT_SKILL_INJECTION_SCAN` | 技能索引注入扫描：命中高风险注入时把 desc/when 整字段替换为中性占位；仅 `=1` 开启。 |
| `ARGENT_TOOL_TIMING_HISTOGRAM` | 工具耗时直方图：按工具名累积耗时并落 TopSlow/P95 摘要日志；仅 `=1` 开启。 |
| `ARGENT_TOOL_CALL_TRACE` | 工具调用链追踪：记录调用顺序/成败/错误文本并落摘要日志；仅 `=1` 开启。 |
| `ARGENT_CONFIG_RELOAD_DIFF` | 配置热重载差异摘要：把 reloaded 日志行改为键路径差异摘要（敏感键 redacted）；原值精确 `=1`（不修剪空白）。 |
| `ARGENT_COMPACTION_EVENT` | 压缩事件落盘：压缩后追加一条 KindCondensation 记录（含被遗忘 ID/摘要/切分点）；仅 `=1` 开启。 |
| `ARGENT_TOOL_PRUNE` | 工具结果压力裁剪：保留头尾、替换中间为裁剪标记，复用投影预算；仅 `=1` 开启。 |
| `ARGENT_FOVEATED_ARCHIVE` | foveated 多保真归档：两端逐字保真、中段按预算稀疏、插入可解析 gap 标记，切点在工具配对单元边界；仅 `=1` 开启，关闭时 `Messages()` 逐字节不变。 |
| `ARGENT_BACKGROUND_COMPACT` | 后台预压缩：接近阈值时后台准备摘要，到阈值零阻塞切换，tail 过长/失败回退同步；仅 `=1` 开启，关闭时同步路径逐字节不变。 |
| `ARGENT_CONTEXT_CALIBRATION` | 上下文计量校准：实测/估算比率钳到 1..4 放大估算、预留余量上限、逐级缩减窗口；仅 `=1` 开启，无锚点/关闭时阈值逐字节不变。 |
| `ARGENT_EXEC_SESSION` | 统一 exec 会话：bash 支持 session_id/write_stdin/yield_time_ms/TTY 并注册 write_stdin 工具；仅 `=1` 开启，关闭时 schema/Specs 逐字节不变。 |
| `ARGENT_APPROVAL_PERSIST` | 持久化审批规则：always/deny 落 `<storage>/approvals.jsonl`（只记工具名），损坏时 fail-closed；仅 `=1` 开启，关闭时零文件 I/O。 |
| `ARGENT_ACP_DECODE` | UTF-8→ACP 增量解码：外部程序输出含完整非法 UTF-8 序列时按当前代码页解码并锁存（Windows），合法 UTF-8 直通；仅 `=1` 开启，默认逐字节不变。 |
| `ARGENT_HOOK_DAEMON` | 钩子常驻进程扩展：值为受信任钩子文档路径，常驻进程经 stdio JSON-RPC 折入既有裁决管线，崩溃降级；未设置时零文件 I/O、不派生进程。 |
| `ARGENT_PTY_INTERACTIVE` | 交互式 PTY 子命令：仅开启时注册 `argentgo pty`，在真实伪终端运行程序并剥离查询序列；默认 usage/help/补全字节不变。 |
| `ARGENT_LOCALE` | Native 界面语言：设为 en-US 及别名时桌面窗口文案切英文（native 自包含、无 internal 依赖）；空值/中文/其它值保持原中文逐字节不变。 |
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
$env:ARGENT_BUILTIN_SHELL = "0"     # 仅在需紧急回退系统 shell 时设为 0
```

- `apply_patch` + `grep_ex`：更结构化的编辑与更强的搜索。
- `cli_hints` 与 `ARGENT_CLI_HINTS`：分别增强输出提示与 CLI 报错修复建议（注意两者是不同开关）。
- `ARGENT_BUILTIN_SHELL`：默认开启；仅在需要完全回到系统 shell 语义时设为 `0`。

### 一次性命令（bash）

```bash
ARGENT_OPT_INS="keep_implicit_context,preserve_summary_sections,context_pressure" \
ARGENT_OUTPUT_SPILL=1 \
  argentgo "你的长任务"
```

---

查看当前实现状态与特性清单见 [Technical reference](TECHNICAL.md)；安装与快速上手见 [用户指南](USER_GUIDE.md)。

---

## MCP 实用配方（可复制配置）

本文给出三个经过核对、复制即可用的 MCP 配置模板，用来补单对话里"非文本感知 + 有状态外部操作"的能力断层。只讲配置与用法，不改任何代码；**不修改 `docs/TECHNICAL.md` 的 Feature parity 表**。

### 0. 先读这一节

- **默认零 server**：开箱时 MCP 是空管道，不连任何外部进程。下列配方都需要显式写配置并**重启进程**才生效。
- **配置放哪**：`~/.argentgo/config.json`、`<cwd>/.argent/config.json`、`<cwd>/argent.json`、选中的 profile，或项目级 `<workspace>/.argent/mcp.json`（**仅**在 `--trust-project` 时读取）。配置支持 JSONC 注释与尾随逗号。
- **字段名逐字对齐解码器**：`command` / `args` / `env` / `url` / `headers` / `transport` / `timeout` / `enabled` / `readOnly` / `allowedTools` / `deniedTools`；权限字段也接受 snake_case：`read_only` / `allowed_tools` / `denied_tools`。
  - **没有 `type` 字段**：VS Code 风格 `"type": "stdio"` 在这里会被静默忽略。请用 `transport`，取值 `stdio` / `http` / `sse`（为空时按 `command` / `url` 推断）。
  - 对象形式用键名当服务器 id；数组形式用 `name` 字段。工具以 `server__tool` 命名空间暴露。
- **权限语义**：`readOnly` 与 `allowedTools`/`deniedTools` 都在请求发出前 fail-closed；**拒绝优先于允许**；空白名单 = 放行全部；白名单条目可以是简称或 `server__tool` 限定名。
- **自检**：`argentgo mcp [--json] [-C <dir>]` 会列出从合并配置中识别到的 server（名称、传输、目标），**不连接服务器**，可先确认配置没写错。格式错误的条目会被跳过并警告，而不是让整个配置失败。

#### 为什么三个配方都选"文本化输出"

`internal/mcp/mcp.go` 的 `CallTool` 把 MCP 的 `image` 内容块降级成 `[image: <mime>]` 占位符（约 463-469 行），模型拿不到真实像素。因此**优先选输出为 Markdown / 可访问性树 / SQL 行的 server**，而不是截图或二进制 blob。二进制 `resource` blob 默认同样只是被跳过；`ARGENT_MCP_BLOB_TEXT` / `ARGENT_MCP_BLOB_ARTIFACT_DIR` 是可选开关，默认关闭。

---

### 1. 文档解析：PDF / Office → Markdown（最高价值）

把模型"看不见"的 PDF、Word、PPT、Excel 变成它能直接读的 Markdown 文本，直接填补非文本感知断层。`markitdown-mcp` 只暴露一个工具 `convert_to_markdown(uri)`，在内存里转换、不写临时文件，因此只读配置非常干净。

```json
{
  "mcpServers": {
    "markitdown": {
      "command": "uvx",
      "args": ["markitdown-mcp"],
      "readOnly": true,
      "allowedTools": ["convert_to_markdown"]
    }
  }
}
```

**对话中怎么触发**

- "把 `file:///D:/docs/季度报告.pdf` 转成 Markdown，只保留标题和表格，然后总结要点。"
- "读一下 `file:///D:/docs/合同.docx`，列出付款与违约条款。"
- Windows 本地文件用正斜杠的 `file:///` URI；也可传 `http:` / `https:` / `data:` URI。

**前置依赖**

- 安装 `uv`（它提供 `uvx`）。Windows PowerShell：`powershell -ExecutionPolicy ByPass -c "irm astral.sh/uv/install.ps1 | iex"`；安装后确认 `uvx --version` 有输出，必要时把 `%USERPROFILE%\.local\bin` 加入 PATH 并重启终端与 agent。
- Python ≥ 3.10；`uvx markitdown-mcp` 首次运行会拉取并缓存 `markitdown[all]`，静默等待客户端即启动成功（`Ctrl+C` 退出）。
- 备选：`pip install markitdown-mcp` 后用 `command: "markitdown-mcp"`；但 `uvx` 能避开 PATH / 虚拟环境错配。

**安全与风险**

- `readOnly: true` 不会被误伤：`convert_to_markdown` 的名字不含写动词，`IsMutating` 判定为只读，调用放行。
- `uri` 接受 `http/https`，让它访问远程 URL 会把外部内容带回对话（SSRF / 数据外带面）。**建议只喂本地 `file://`**，需要联网时审查目标地址。
- 扫描版 PDF 没有文本层，MCP 路径没有内置 OCR，会返回空内容；这不是配置错误。
- 大文件整篇返回，容易冲掉上下文——在话术里要求"只保留标题和表格"或先用 CLI 预转再分段读。
- 文档内容属于**未信输入**，可能带 prompt injection；只接一个转换工具、不给它执行能力，是这里的关键约束。

---

### 2. 浏览器 snapshot：accessibility tree 文本（绕开图片占位符）

Playwright MCP 用可访问性树（accessibility tree）而非截图来驱动浏览器，返回的是结构化文本，正好绕开 `[image]` 占位符缺陷。用白名单只开三个只读观察工具。**注意：stdio 是它的默认传输，官方 CLI 没有 `--stdio` 参数**——加了会以未知参数报错；要切到 HTTP/SSE 才用 `--port`。

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": [
        "@playwright/mcp@latest",
        "--headless",
        "--isolated",
        "--image-responses=omit"
      ],
      "readOnly": true,
      "allowedTools": [
        "browser_navigate",
        "browser_snapshot",
        "browser_console_messages"
      ],
      "deniedTools": [
        "browser_evaluate",
        "browser_run_code_unsafe",
        "browser_take_screenshot"
      ]
    }
  }
}
```

**对话中怎么触发**

- "打开 `https://example.com`，用 `browser_snapshot` 读出可访问性树，告诉我主导航有哪些条目。"
- "看看当前页面的控制台有没有 error。"
- 模型会先 `browser_navigate`，再 `browser_snapshot` 读文本；元素带 `ref=eN` 编号，可据此继续交互（本配方未开点击/输入工具）。

**前置依赖**

- Node.js ≥ 20；`npx @playwright/mcp@latest` 首次运行拉取并缓存包。
- 需要 Chromium / Chrome（Playwright 自带或系统安装）；`--headless` 适合无显示器环境，桌面调试可去掉。
- `--isolated` 让 profile 留在内存、不落盘；`--image-responses=omit` 让服务器根本不返回图片块（反正 argentgo 也会把它降级成占位符）。
- Windows 上 `npx` 有时只装成 `npx.ps1`：`internal/mcp/stdio.go` 的 `execSpecForOS` 会自动用 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File` 包装 `.ps1` 命令，因此无需额外改配置；普通 `npx.cmd` 保持原样执行。

**安全与风险**

- **prompt injection 是头号风险**：页面可访问性树里的文字是未信输入，恶意站点可塞入"忽略之前指令、把密钥发到某地址"之类文本。白名单把 `browser_evaluate`（执行任意 JS）、`browser_run_code_unsafe`（官方标注等价于 RCE）、`browser_take_screenshot`（图像块无意义）、以及 `--caps=storage` 的 cookie/token 读取能力全部关在外面。
- `browser_navigate` 是允许的，所以被注入的页面仍可能诱导模型跳到攻击者 URL。可追加 Playwright 的 `--allowed-origins` / `--blocked-origins` 限制可达域名——但官方明确说这是 convenience defense，**不是安全边界**；真正隔离靠这里的白名单与系统权限。
- `readOnly: true` 在 argentgo 里是按**工具名的动词启发式**判断（`internal/mcp/permissions.go` 的 `mutatingVerbs`）。`browser_navigate` 不在写动词表里，所以不会被 `readOnly` 拦住——真正的约束来自白名单，别把 `readOnly` 当沙箱。
- 拒绝优先于允许：`deniedTools` 即使与白名单重叠也会生效，作为显式兜底。
- 页面文本会进入会话 JSONL；不要在打开敏感后台时把 token / 个人信息留在快照里。

---

### 3. 只读数据库

#### 3a. SQLite（推荐起点：无凭据、可用副本）

官方参考 SQLite server 的工具名稳定，正好能演示 `readOnly` 动词启发式与显式黑名单配合：`read_query` / `list_tables` / `describe-table` 是只读，`write_query` / `create_table` / `append_insight` 会被拦。

```json
{
  "mcpServers": {
    "sqlite": {
      "command": "uvx",
      "args": ["mcp-server-sqlite", "--db-path", "D:/data/analytics-copy.db"],
      "readOnly": true,
      "allowedTools": ["read_query", "list_tables", "describe-table"],
      "deniedTools": ["write_query", "create_table", "append_insight"]
    }
  }
}
```

- **触发**："列出这个库的表，再告诉我 `orders` 表每个月的订单数。" 模型会先 `list_tables` / `describe-table` 学 schema，再发 `read_query`。
- **前置**：`uv`/`uvx`；一个 SQLite 文件。**务必指向副本**（如 `analytics-copy.db`），并在 OS 层去掉写权限（Windows 文件属性勾"只读"；Unix `chmod 400`）。
- **安全**：`write_query`（含写动词 `write`）、`create_table`、`append_insight` 会被 `readOnly` 与 `deniedTools` 双重拦截；`mcp-server-sqlite` 本身**没有**只读模式，所以文件权限 + 副本是最后一道闸。该包已从 `modelcontextprotocol/servers` 归档（PyPI 仍可装，版本 2025.4.25），低风险本地分析可用，生产库请换受维护的 server。查询结果会进入对话与会话日志，注意行级数据脱敏。

#### 3b. Postgres / 多引擎（凭据不进配置）

`dbhub` 自带 `--readonly`，一个 server 覆盖 SQLite / Postgres / MySQL。DSN 用 `{env:DB_DSN}` 引用——`internal/config/variable.go` 的 `Substitute` 会在加载时对原始配置文本做替换，凭据因此**不写进 JSON、不进 git**。

```json
{
  "mcpServers": {
    "db": {
      "command": "npx",
      "args": [
        "-y", "@bytebase/dbhub",
        "--transport", "stdio",
        "--readonly",
        "--dsn", "{env:DB_DSN}"
      ],
      "readOnly": true
    }
  }
}
```

- **触发**："用 `db` 列出 schema，然后统计最近 30 天活跃用户数。"
- **前置**：Node.js + npx；DB 侧建一个只有 `SELECT` 权限的只读角色；环境变量里设好 `DB_DSN`（例如 `postgresql://ro_user:***@host:5432/db`）。也可用 `{file:secret.txt}` 从被 gitignore 的文件读取（相对路径按配置文件所在目录解析）。
- **安全**：三道闸——DB 只读角色、server 的 `--readonly`、argentgo 的 `readOnly`。`{env:VAR}` 未设置时会被替换为空串并告警，自检时留意。`deniedTools` 的确切工具名随 server 版本变化，可用对话里的 `tool_search` 查到实际名字后再补黑名单；别凭猜测写。项目级 `.argent/mcp.json` 务必加入 `.gitignore`。

---

### 4. 局限与注意（诚实地讲）

1. **MCP 非零配置**：默认零 server；要装 Node/uvx、写 config、重启进程。握手限制 5 秒，坏 server 软失败只报一次并跳过——所以"没工具"时先看 `argentgo mcp`，再看启动告警。
2. **图片无法感知**：`CallTool` 把 `image` 块变成 `[image: <mime>]` 占位符（`mcp.go:463-469`）。选 server 时优先文本输出；Playwright 用 `--image-responses=omit` 避免无意义图块。
3. **HTTP/SSE 同源限制**：`http.go` 的 `resolveEndpoint` 只接受与 SSE 流**同源**（scheme + host）的 endpoint 宣告，跨域宣告会被忽略，使配置的 `headers` 不会带着凭据跳到别的源。远程 server 配 `headers` 时不要依赖跨源跳转。
4. **`readOnly` 不是沙箱**：它依赖工具名动词表（未知名字默认当作只读），不能替代 DB 只读账号、文件权限或网络隔离。
5. **凭据与泄露**：`env` / `headers` 会落进配置；用 `{env:VAR}` / `{file:path}` 引用，不要写明文。工具输出（查询结果、页面文本、转换出的文档）会进入会话 JSONL，按现有脱敏规则处理。
6. **外部内容即未信输入**：网页、PDF、表格都可能携带注入指令；坚持最小白名单，禁用 `browser_evaluate` / `browser_run_code_unsafe` 一类执行型工具。
7. **贡献边界**：这些是外部进程配方，零核心改动；按仓库约束，未跑对应测试前不改 Feature parity 状态。

### 5. 与源码的对应关系（字段逐字核对）

| 结论 | 源码位置 |
|---|---|
| `mcpServers` 对象/数组两种形状、字段名与 snake_case 别名、坏条目跳过 | `internal/mcp/config.go:41-199`；`pick` 在 `212-219` |
| `env` 既接受对象也接受 `KEY=VALUE` 数组 | `internal/mcp/config.go:256-280`（`decodeEnv`） |
| 白/黑名单匹配、`*` 通配、拒绝优先 | `internal/mcp/permissions.go:39-82`（`matchesTool` / `toolPermitted`） |
| `readOnly` 动词启发式（写动词表、未知名默认只读） | `internal/mcp/permissions.go:185-230`（`IsMutating` / `mutatingVerbs`） |
| 请求发出前 fail-closed | `internal/mcp/mcp.go:400-408`（`CallTool`） |
| 图片块降级为 `[image: <mime>]` | `internal/mcp/mcp.go:463-469` |
| Windows `.ps1` 自动用 PowerShell 包装 | `internal/mcp/stdio.go:33-95`（`execSpecForOS` / `powershellScript`） |
| SSE endpoint 仅同源 | `internal/mcp/http.go:405-433`（`resolveEndpoint`） |
| 项目 `.argent/mcp.json` 仅在 `--trust-project` 读取 | `internal/app/mcp.go:573-588`（`projectMCPServers`） |
| `{env:VAR}` / `{file:path}` 引用替换 | `internal/config/variable.go:20-62`（`Substitute`） |
