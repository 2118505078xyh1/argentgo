# argentgo 优化 TODO（仅未做项）

> **生成方式**：原 100 条生成于 2026-11-22，由 10 个只读子代理并行深挖 git tracked 源码（HEAD `8881ed9`，round 48），按 9+ 维度取证、交叉去重后按用户优先级重排。
>
> **2026-11-22 增补**：新一轮 6 个只读子代理分别深读 codex / crush / cline / opencode / pi-mono / oh-my-pi 的 `.rs`/`.ts`/`.go` 源码（不看文档做结论），每条发现均双向核对 argentgo 源码后写入新增章节（101–135，已删除落地项）。条目的竞品证据与现状均带 `路径:行号`；所有代理均未改动业务代码。
>
> **证据口径**：每条都附 `路径:行号 / 函数名`，区分「已实现但未接线 / 仅桩 / 边界 bug / 缺测试 / 缺验证」。路径相对项目根 `D:\code\mpi\argentgo\`。
>
> **硬约束**（所有建议必须满足）：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制、自研 shell 不外包；`native/` 是唯一 MyGo 例外；opt-in 项默认关闭且默认路径逐字节不变是项目承诺。
>
> **优先级**：P0 = 实锤 bug / 安全绕过 / 数据丢失 / 发布阻塞；P1 = 已有原语未接线 / 明显体验缺口；P2 = 改进与防御纵深。工作量 S/M/L。

**统计**：仅列出未做项；原编号保留（已落地条目的编号与正文已删除，因此编号有断档）。

---

## 一、上下文管理与可恢复性（1–14）

### 13. 供应商原生 live steering：流中重定向 `[P1/L]`（学自 oh-my-pi/pi-mono）
- **竞品证据**：`oh-my-pi/packages/agent/src/live-steering.ts` 的 `LiveSteeringChannel.claim/wait`、`accepted/deferred`；`agent-loop.ts:812/1968/4203`。
- **现状/缺口**：`internal/agent/steer.go:17,25` 与 `loop_helpers.go:196` 的 `injectSteering` 只在下一轮边界注入；长输出中用户无法即时改向，abort 则丢整轮。
- **建议**：在已有 Responses 协议 `internal/llm/responses.go` 暴露可选 `Steer func() []string`，SSE 循环中按 provider 形式投递；不支持协议回退边界路径；复用同一 `SteerQueue` 记 `accepted/deferred` 事件；opt-in `ARGENT_LIVE_STEER=1`，纯 stdlib。
- **验收**：模拟 Responses 流中注入后后续文本遵循新指令且 transcript 顺序正确；不支持协议请求字节不变；取消时无 goroutine 泄漏。

---

## 二、安全边界（15–28）

### 18. `result_provenance.go` 半完成且与已有原语重复 `[P1/M]`
- **现状**：`internal/tools/result_provenance.go:9-92` 的 `Provenance`/`AnnotateResult`/`Header`/`PrependHeader`，文件头自述「不参与任何输出管线，也没有生产调用方」；`agent/untrusted_result.go:30-64` 提供已接线的同类能力。
- **问题**：两套来源标注格式（`[tool:...]` vs `<tool_result ...>`）并存，维护漂移；默认路径工具结果无任何来源/脱敏/裁剪标记。
- **建议**：二选一并落地——推荐在 `internal/tools/tools_registry.go:Run` 返回处用 `AnnotateResult(name,out,sanitized,clipped).Header()` + `PrependHeader` 加头，复用 agent 的 opt-in 总开关以免默认字节变化；或删掉该文件统一到 `UntrustedResult` 并补 sanitized/clipped/bytes 字段。
- **验收**：`grep -R "AnnotateResult(" --include=*.go internal/ | grep -v _test.go` 至少 1 条；新测试断言开启后结果首行为 `[tool:read sanitized clipped bytes=N]`、关闭时逐字节一致。

### 21. 分阶段审批状态机无生产启用点、无实现者 `[P2/M]`
- **现状**：`chain.go:148-158` 的 `StagedApprover`、`:164-173` 的 `EnableStagedApproval`、`:301-372` 的 `checkStaged`、`approval_stage.go:76-159` 的 `AdvanceStage/ScopeSummary` 生产调用为零，生产代码无类型实现 `AskStage/RememberScope`；对照 `EnablePlanMode` 在 `app_execute.go:376` 有真实接线。
- **问题**：once/always/reject/esc、「始终允许」作用域记忆完全不可用，用户只能逐次批准；32 轮上限（:291）的防御从未在生产路径验证。
- **建议**：在 `app_execute.go:374-376` 旁新增 opt-in 读取调用 `chain.EnableStagedApproval(enabled)`；让 TUI/native 的交互审批器补 `AskStage`/`RememberScope`，作用域存其已有 remember 集合；`chain.go:352` 的二次确认勿绕过。
- **验收**：`grep -R "EnableStagedApproval(true)" --include=*.go internal/app` 至少 1 条；端到端测试走完 always→二次确认→RememberScope，esc 不提交。

### 27. 默认 bash 路径绕过 proc 全部进程硬化 `[P1/M]`
- **现状**：`internal/tools/../shell/tools_builtin_bash.go:153-168` 默认走 `exec.CommandContext(...).CombinedOutput()`；硬化实现 `runBashViaProc`:281-303（`KillTree:true`）只在 `ARGENT_BASH_USE_PROC=1` 启用，因此 `proc.go:174 SanitizeEnv`、:167-168 UTF-8 carry、:162-163 每流 64KB 上限默认不生效。
- **问题**：超时/取消只杀直接 shell，孙进程变孤儿；`CombinedOutput` 无界缓冲，失控命令可撑爆内存；子进程继承宿主私有会话管道变量（嵌套 agent 误认宿主会话）；跨分块中文解出 U+FFFD。
- **建议**：硬化路径设为默认，把 `bashUseProcEnabled()` 语义反转为 opt-out（`ARGENT_BASH_USE_PROC=0` 才回退旧路径）；给 `bashProcCommand` 补 `Env: proc.SanitizedInheritedEnv()`；保留 stdout/stderr 顺序差异测试契约。
- **验收**：`go test ./internal/shell -run 'Bash.*Proc|KillTree|Sanitize' -count=1`；新增集成测试：写中文并派生孙进程，超时后断言孙进程 pid 消失且输出无 U+FFFD。

### 28. 审计日志未脱敏：`SanitizeForLog` 仅活在测试里 `[P2/S/M]`
- **现状**：`internal/shell/command_safety.go:55 ClassifyShellCommand`、`:148 RequireSafeCommand`、`:170 SanitizeForLog` 非测试调用为零（:149 仅内部调用）；`app_execute.go:381` 构造审计链时命令原样流向审计。
- **问题**：`SanitizeForLog` 精心实现 `$VAR`/赋值/口令状串脱敏（:294-411）却从未使用；若审计记录原始 `call.Function.Arguments`，`export TOKEN=abc123`/`--password xxx` 会明文落盘，违背「脱敏发生在写入审计前」不变量。
- **建议**：在 `app_execute.go:542` 审批前或 security 审计适配器调用 `shell.SanitizeForLog(name+" "+args)` 再写审计；在 `tools_registry.go:252 checkGuard` 前按需 `shell.RequireSafeCommand`，把 `ErrUnsupportedCommand` 转模型可读错误（app 可 import shell，无新依赖）。
- **验收**：`internal/app/audit_sanitize_test.go`：执行带 `TOKEN=abc123def` 的 bash，读 audit 日志断言不含明文且含 `<redacted>`。

---

## 三、工具与执行深度（29–38）

### 29. `internal/pty` 包零 importer，交互式能力不可达 `[P1/L]`
- **现状**：`internal/pty/pty.go:81 Start`、`escape_filter.go:26 stripTerminalQueries`/`:159 queryFilter`、平台实现与测试齐备，但全仓（含 native）grep `argentgo/internal/pty` 返回空。
- **问题**：需 TTY 的开发服务器/REPL/监视测试永远跑在管道后，失去颜色/提示/刷新；更严重的是针对 DSR/DA/DECRQSS/OSC 查询的反注入防线 `stripTerminalQueries` 从未运行，将来草率接线 PTY 会把终端回答变成注入通道。
- **建议**：在 `internal/tools` 增加只读 `pty_run` 工具（或 bash 工具加 `tty:true`），经 pluginapi 注入构造子、由 app 负责 `pty.Start`；读循环必须串接 `queryFilter.Write/Flush` 后再进 UTF-8 carry 与 clip，复用 `proc.SanitizeEnv`；`Pty.Close/Wait` 与取消 ctx 绑定。
- **验收**：`internal/app/pty_wire_test.go` 假 Pty 注入，喂 `ESC[6n` 后输出不含该序列，ctx 取消触发 Close；加守护测试 `TestPtyPackageHasProductionImporter`。

### 37. `internal/commands` 补全/诊断纯函数未接线 `[P2/S]`
- **现状**：`internal/commands/complete.go:33` 的 `CompleteCommands`、`complete_argument.go:33`、`complete_coordinator.go`、`diagnostics.go:52` 的 `DiagnoseCommands` 均为纯函数且有测试，但 app 侧无调用证据；CLI 的补全走自成一套的逻辑。
- **问题**：slash 命令的参数补全与诊断能力写好不用，用户在 TUI 输入 `/commit ` 时得不到候选或错误提示。
- **建议**：在 `internal/app/commands_wire.go`（或现有命令装配处）把 `CompleteCommands`/`DiagnoseCommands` 接到 TUI 输入完成回调与 CLI 的 `commands doctor`；保持默认无配置时行为不变。
- **验收**：`go test ./internal/commands ./internal/app -run 'Complete|Diagnose' -count=1`；手工在 TUI 输入 `/` 后 Tab 出现候选。

---

## 四、模型/协议与召回（39–50）

### 49. 模型策略规则化：窗口/限制/怪癖下沉为数据 `[P2/M]`（学自 oh-my-pi catalog）
- **竞品证据**：`oh-my-pi/packages/catalog/src/compat/resolve.ts` 的 `resolveModelPolicy`:1371、`context-window.ts`、`prompt-cache-lookback.ts`；261 条 KDL 规则编译为 rules.json。
- **现状/缺口**：`internal/provider/templates.go`、`context_window.go`、`capability_profile.go`、`thinking.go` 把窗口/能力/推理预算写成 Go 常量；新增长尾模型要改代码重编译，没有「精确 id > 族 > 默认」优先级与冲突检测。
- **建议**：把纯数据抽到 `internal/provider/model_policy.json`（stdlib `encoding/json` + `go:embed`），字段含 id/class/family 选择器、contextWindow、maxOutput、promptCacheLookback、quirks；`provider.Resolve` 按精确 id→族→默认解析，同优先级重叠报明确错误；现有常量保留为默认值。
- **验收**：只改 JSON 即新增模型生效（无需改 .go）；同优先级重叠返回错误；未声明字段的 provider 输出逐字节一致；go vet/gofmt 全绿。

---

## 五、交互界面与可用性（51–61）

### 51. Native 侧边栏 11/12 导航项是静态占位桩 `[P1/M]`
- **现状**：`native/nav_panel.go:17-73` 的 11 个导航项各配 `desc+rows` 纯文本，`:78-83 navPanelFor` 未知项回退「该面板尚未实现。/敬请期待」；`native/main.go:290-314 sidebar` 中 `btn.Clicked()` 仅 `app.nav = name; app.status = "已切换到"+name`；只有「聊天」走真实 `sessionPanel`；测试只验证映射不验证可用性。
- **问题**：点击 Git/文件/用量/任务/终端/工作流得到不可交互静态文字，占导航项 92%，是桌面端最显眼的半成品。
- **建议**：给 `navPanel` 增 `onRow func(*chat,string)`；优先接已有 RPC：「用量」→engine token 统计、「任务」→jobs 列表、「Git/文件」→经 `bridge.call` 调 server 方法；无后端能力的项改只读展示真实文件（AGENTS.md/docs）并注明「只读预览」；去掉「敬请期待」文案改为可诊断错误。
- **验收**：新增 `native/nav_panel_test.go`：每个非聊天项至少有可点击回调或 status 不只是「已切换到」；手工点击 11 项截图无「敬请期待」。

### 52. Native 完全无本地化，硬编码扫描存在「目录+中文」双盲区 `[P1/L]`
- **现状**：`native/` 零 `internal/i18n` import，非测试文件约 158 处中文 UI 字面量；`internal/i18n/i18n.go:1-5` 自称「CLI 和 TUI 的字符串目录」但 TUI 实际用自建 `tui/locale.go:10` 双表，形成两套并行目录；`hardcoded_scan_test.go:31-34 hardcodedScanRoots` 只含 cli/cmd，`:294-300` 对含非 ASCII 字符串直接判非硬编码。
- **问题**：`argentgo gui --locale en-US` 对原生窗口无任何效果（叠加 #60）；英文用户无法用桌面端；守卫无论新增多少中文硬编码都不失败。
- **建议**：① 扩展 `hardcodedScanRoots` 加 native/与 internal/tui/，新增 `isHardcodedChinese` 并建 158 处存量 baseline 做增量守卫；② native 建 locale.go 复用 i18n 的 catalog/effectiveLanguage（增加导出包装），迁移 navItems/status/审批卡/空态；③ `cmd/argent/gui.go:97` 把 ARGENT_LOCALE 传入。
- **验收**：`go test ./internal/i18n -run 'TestCLIHardcodedEnglishDoesNotGrow|TestHardcoded' -count=1`；设 `ARGENT_LOCALE=en-US` 启动 GUI 截图主界面无中文。

### 57. WCAG 对比度原语未接入徽章渲染 `[P2/M]`
- **现状**：`internal/tui/contrast.go:87-138` 的 `contrast`/`ContrastRatio`/`LegibleInk` 完整实现（#rgb/#rrggbb 解析、sRGB 线性化、3:1 阈值、无效输入保守返回 0），注释明确「尚未接入徽章渲染器」并给出方案；仅测试引用；`theme.go:23-39 Palette` 全为 SGR 字符串，无 hex 角色。
- **问题**：新增非内置主题时徽章前景/背景对比度无人校验，低对比组合在深色/高亮终端不可读；成熟辅助函数白写。
- **建议**：`Palette` 加可选 `BadgeBGHex/BadgeInkHex/BadgeAltInkHex`（内置主题留空走原 SGR，逐字节不变）；`render.go attemptLinesLocked` 的「⟳」徽章与 `renderEntryLocked` 通知墨色在非空时调 `LegibleInk`；`SetTheme` 对非内置主题做一次 ContrastRatio 校验并给出可测试错误/降级路径。
- **验收**：`theme_contrast_test.go` 构造低对比自定义主题断言墨色被替换为 alt；内置三主题 Frame 逐字节相同。

---

## 六、生态与扩展（62–73）

### 72. 事件钩子常驻外部进程扩展 `[P2/L]`（学自 pi-mono ExtensionAPI）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/extensions/types.ts`（订阅生命周期事件、注册工具/命令/快捷键、UI 原语）、`runner.ts`、`event-bus.ts`。
- **现状/缺口**：`internal/plugins` 只做清单发现（贡献 skills/commands/MCP）无代码执行；`internal/hooks` 每次事件 fork 一个 shell 取 stdout；`internal/mcp` 只覆盖工具不覆盖生命周期事件决策（压缩前、缓存决策、模型切换）。
- **建议**：复用 `internal/server` 已有的 stdlib JSON-RPC/stdio 新增 `hookServer` 配置：一个常驻外部进程声明订阅事件列表，事件到达发 `event` 通知，可返回 `additionalContext`/`verdict`（沿用 exit-2 fail-closed 或 warn-only）；不引入 Go plugin（避免 cgo），单二进制与零依赖不变。
- **验收**：常驻进程一次会话只启动一次、收到 ≥3 类事件；verdict 能阻断工具且 stdout 不污染协议；进程崩溃后自动降级为无扩展主循环继续；默认无配置时字节不变。

---

## 七、工程纪律 / 测试 / CI / 发布（74–93）

---

## 八、性能 / 资源 / 可观测（94–99）

---

## 九、竞品源码深读新增（101–135）

### 104. 上下文计量：provider 实测锚点 + 文本估算 + 预留余量 + 溢出逐级缩减 `[P1/M]`（opencode + cline + pi）
- **竞品证据**：`opencode/packages/core/src/session/compaction.ts:55-56` 把大小拆成 `measured`（provider 报告的锚点）与 `estimated`（其后新增文本）；`:1120-1144` 用最近同模型、无错的 assistant 的 input+cache+output 作 measured 基线，只估未计量尾部；`:123-136,1151-1159` 的 `RESERVE_SHARE=0.15`/`RESERVE_MIN=16000` 算 ceiling；`:122-124,668-682` 溢出后按 `SHRINK_STEPS=[0.85,0.7,0.5,0.35]` 缩减。`cline/sdk/packages/core/src/extensions/context/compaction.ts:344-368` 用 `underestimateFactor=min(4,max(1,actualPreviousInputTokens/requestInputTokens))` 自适应缩放。`pi-mono/packages/coding-agent/src/core/agent-session.ts:3121-3250` 区分溢出可重试 / 成功但超限 / 阈值三种情形。
- **argentgo 现状**：`internal/agent/trim.go:13-71` 的 `EstimateMessages/EstimateTools` 是纯字符/4 启发式（含缓存保护阈值 `:22-24`）；`internal/app/compact.go:124-132` 有 `usageAnchor` 做原生 totalTokens 前缀锚点，但阈值比较（`:82-88`）仍用固定 `InflateEstimate` 1.3 倍（`internal/agent/budget.go:152-158`），没有「实测/估算」比率自适应；触发后也没有逐级缩减重发。
- **问题**：本地模型/大工具输出场景下估算误差大，可能过早压缩（浪费缓存）或过晚触发（撞硬限制）。
- **建议**：opt-in 地在 `estimateContextTokens` 记录 `requestInputTokens`，用有上限的系数收缩/放宽阈值；把 `RESERVE_SHARE` 风格余量纳入阈值计算；溢出重试复用 `SHRINK_STEPS` 式逐级重发（但受 `MaxCompactRetries` 约束）。默认路径保持 chars/4 × 1.3 不变。
- **验收**：表驱动测试喂「实测 120k / 估算 60k」断言系数钳到 4、阈值按新公式变化；基线用例断言 opt-in 关闭时阈值逐字节不变；`go test ./internal/agent ./internal/app -count=1`。

### 105. 后台预压缩：提前写摘要、到阈值直接切换 `[P1/L]`（opencode + codex）
- **竞品证据**：`opencode/packages/core/src/session/compaction.ts:338-402`：`PRECOMPUTE_LEAD=60_000`，在距上限还有 60k token 时 fork 后台摘要，锚定最近 provider 已计量的 assistant（`:360-366`），受 cache TTL（`:369-386`，过期/换模型/连续失败 2 次则取消）约束；摘要**只持有不立即应用**（`:340-343`，避免改写缓存前缀），`:404-428` 在阈值处校验 tail 后 swap。`codex/codex-rs/core/src/session/async_compaction.rs:34-110` 的 `maybe_start` 有类似的 history_version/模型/auth 一致性检查。
- **argentgo 现状**：`internal/agent/runner.go:111-115` 的 `Compactor` 只有同步 `MaybeCompact`；`internal/agent/loop_helpers.go:15-42` 在阈值处同步调用。`internal/agent/compaction.go:120-168` 的 `SummaryCache` 只缓存相同前缀的摘要结果，避免重复摘要，**不会提前做**。
- **问题**：写摘要要 30–60s，到阈值才开始会让下一次请求阻塞；大文件读取时上下文暴涨来不及。
- **建议**：在 app 层包一层 opt-in 后台 Compactor（沿用 `Compactor` 接口，不改 agent 热路径），在压力接近阈值时异步产出摘要，到阈值时按 anchor/tail 校验后交给现有 `MaybeCompact` 消费；失败/换模型/超 TTL 则丢弃。默认同步路径逐字节不变。
- **验收**：`internal/app/background_compactor_test.go`：注入伪 Chatter 控制延迟，断言后台摘要生成后阈值处零阻塞切换、tail 过长时拒绝切换、失败后回退同步；`go test ./internal/app -count=1`。

### 106. 重试前的持久上下文省略 + 可中止退避 + 摘要调用共享退避 `[P1/M]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/agent-session.ts:1269-1283` 的 `_omitRecoveryAttempt()`，`:1313-1354`/`:3936-3995` 在重试前对失败助手消息及其工具结果写 `appendContextEdit(targetId, null)` **永久省略**；`pi-mono/packages/ai/src/utils/retry.ts:160-164` 的退避可被 `abortRetry` 中断；`:2073-2080,3883-3918` 让压缩/分支摘要的 LLM 调用复用同一 retry 预算与事件。
- **argentgo 现状**：`internal/agent/fallback.go:48-160` 在可重试错误时立即切换模型，无退避、失败消息留在 `s.messages`；`internal/agent/compact_retry.go:44-100` 的压缩重放是 opt-in 且最多一次，同样保留失败消息。`internal/session/context_edit.go:57` 的 `AppendContextEdit` 已有 Text 替换/隐藏语义，但只有 CLI 手动调用。
- **问题**：失败尝试及其工具结果持续占用上下文，重试时重复污染；瞬时限流立即切模型可能不是最优策略。
- **建议**：opt-in 同模型重试策略（`maxRetries`/base/cap），重试前通过 app 回调把失败助手消息写 `ContextEdit{TargetID, Text:""}` 永久省略；退避可被 ctx 取消；复用 `internal/llm/retry.go:416-440` 的分类。默认仍走当前模型回退，事件/字节不变。
- **验收**：`internal/app/retry_omit_test.go`：429 → 重试成功，断言失败轮被 context_edit 省略、退避受 ctx 取消、默认路径无 edit 记录；`go test ./internal/app ./internal/agent -count=1`。

### 111. crush 风格生命周期 hook 事件补齐：TurnStart/End/Interrupt/StopFailure/SubagentStop `[P2/S-M]`（学自 crush）
- **竞品证据**：crush `internal/agent/agent.go:987,1308,1421` 按分类触发 TurnStart/Interrupt/StopFailure/TurnEnd（`turn_hooks.go:25-47`），取消用 `context.WithoutCancel` 保证落盘。
- **argentgo 现状**：常量已声明（`internal/hooks/hooks.go:43-57,98`），但生产只 fire #110 列出的几个；agent 已有完整事件源（`internal/agent/events.go:15-28`），app 的 `Emit` 闭包在 `internal/app/app_execute.go:702`，审批事件在 `dispatch.go:429-434`。
- **问题**：用户订阅不到轮次边界/中断/子 agent 结束等生命周期事件，hooks 声明不完整。
- **建议**：opt-in `ARGENT_HOOK_LIFECYCLE=1`，在 Emit 闭包把 `EventTurnStart/EventTurnEnd/EventStop/EventSubagentEnd` 映射到已有 hook 事件，`EventStop` 按 `result.Stopped` 分类，审批事件接 `bridge.fire`；默认无映射。
- **验收**：`internal/app/hook_lifecycle_test.go`：一轮完整执行后 hook 收到 TurnStart→TurnEnd→Stop 序列；取消时收到 Interrupt；默认无额外 hook 调用；`go test ./internal/app -count=1`。

### 120. 统一 exec 会话：`session_id`/`write_stdin`/`yield_time_ms`/TTY `[P1/L]`（学自 codex）
- **竞品证据**：codex `core/src/tools/handlers/unified_exec.rs:67-91` 定义 `ExecCommandArgs`（`tty`、默认 `yield_time_ms=10s`（`:113`）、`max_output_tokens`、per-call `sandbox_permissions`、`prefix_rule`）；`:30-55` 暴露 `session_id`，`:117` `write_stdin` 默认 250ms；`core/src/unified_exec/mod.rs:75-76` 输出封顶 1MiB/256K token；`process_manager.rs:105` 限制 stdin 写入 8000 字节。
- **argentgo 现状**：`internal/bash/tools_builtin_bash.go:108` 的 bash schema 只有 `command`/`timeout_seconds`/`background_after_ms`；`:129-146` 三条 opt-in 路径；`internal/bash/core.go:96-150` 的持久 shell 用「命令 + nonce 探针」一次性 round trip，明确「管道没有 TTY，超时只能销毁会话」；`internal/jobs/jobs.go:237` 后台作业 `child.Stdin = devNull`；`internal/pty/pty.go` 有 ConPTY/ptmx 零 cgo 实现但**全仓无生产 importer**。
- **问题**：REPL、dev server、watch 模式、交互式登录等长驻进程无法真正交互（现有持久化 shell 只能复用 cwd/env）。
- **建议**：`ARGENT_UNIFIED_EXEC=1` 激活，基于 `internal/pty` + `internal/proc` 新增轻量会话注册表；bash 工具加 `session_id`/`yield_time_ms`/`stdin` 字段并新增 `write_stdin` 工具；stdin 写入限长、输出环形缓冲复用 output_spill；关闭时参数忽略、逐字节不变。注意和旧 TODO #30（持久化 shell 会话）互补：#30 解决状态复用，本条解决交互通道。
- **验收**：`internal/bash/exec_session_test.go`：启动 `python -i` 或简单 REPL，两次 write_stdin 保持进程；TTY 模式下 Ctrl-C 只杀该会话；空闲超时回收无残留进程；`go test ./internal/bash ./internal/pty -count=1`。

### 121. Windows 挂起启动 → Job Object → 恢复，封住孙进程逃逸窗口 `[P1/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/shell/src/sys/spawn/windows.rs:1030` 用 `CREATE_SUSPENDED` 创建进程，`:1052` 分配 Job Object 失败即对**未运行**的子进程 `TerminateProcess`，`:1058` 才 `ResumeThread`。
- **argentgo 现状**：`internal/proc/proc_windows.go:95-110` 在调用方 `cmd.Start()` **之后**才 `createJob()` + `assignJob()`；分配失败只能事后 `taskkill /F /T`（`:126-130`），期间子进程的孙进程可能已经启动并逃逸；`terminate_windows.go:7-9` 优雅终止在 Windows 直接退化为立即 kill。
- **问题**：超时/取消时可能留下孤儿进程；Windows 上无法发 CTRL_BREAK 做优雅退出。
- **建议**：opt-in `SuspendedJob`：沿用 `internal/proc/proc_windows.go:26-33` 的 `syscall.NewLazyDLL` 模式调 `CreateProcessW`/`AssignProcessToJobObject`/`ResumeThread`；入 job 失败即杀掉挂起进程；附带短 grace + `CTRL_BREAK_EVENT`。无 cgo、linux 交叉编译路径不受影响。
- **验收**：`internal/proc/suspended_job_windows_test.go`：启动会 spawn 孙进程的命令，取消后两者均不在；入 job 失败路径子进程从未运行；`GOOS=linux go build ./...` 仍绿。

### 122. 多轮 follow-up 队列：本应停止时再继续 `[P1/S-M]`（学自 pi-mono）
- **竞品证据**：pi `packages/agent/src/agent.ts:197-198` 双队列 `steeringQueue`/`followUpQueue`（各自 one-at-a-time/all 模式，`:255-256`），`:410-412` 外层循环在 agent 停止后 drain follow-up 再跑一轮；`agent-loop.ts:187,310-317` 明确「Agent would stop here. Check for follow-up messages」；`types.ts:233-248` 的 `finishTurn` 返回 `{action:"end"/"continue"}`。
- **argentgo 现状**：`internal/agent/steer.go:12-45` 只有单一 `Steer <-chan string`；`loop_helpers.go:246-270` 在轮次边界 drain；`loop.go:921-940,1140-1146` 无工具回复时只在 `injectSteering()` 命中时多开一轮；无 follow-up 概念。
- **问题**：用户在模型正在回答时追加的消息（TUI/native 场景）只能当 steering 在当前轮插入，无法「答完后作为新轮次继续」。
- **建议**：opt-in `Options.FollowUp <-chan string` + `FollowUpMode`；在 `loop()` 无工具结束分支抽取 follow-up，命中则再跑一轮；不接通道时零分配空操作。
- **验收**：`internal/agent/follow_up_test.go`：无工具回复时通道已有 follow-up → 多跑一轮并包含该文本；通道空 → 原样 StopComplete；`go test ./internal/agent -count=1`。

### 126. 压缩请求溢出时按缓存前缀逐条淘汰 `[P1/S-M]`（学自 codex）
- **竞品证据**：codex `core/src/compact.rs:355-367`：压缩模型请求遇 `ContextWindowExceeded` 时 `history.remove_first_item()` 并重置重试计数，注释明确「从头部裁剪以保留缓存前缀」。
- **argentgo 现状**：`internal/agent/compact_retry.go:59-90` 只在 provider 溢出后调用一次 `MaybeCompact` 且最多重放一次；`internal/app/compact.go:249-285` 的 `compactionWorthwhile` 判定不划算时返回 `(messages,false)`，`compactAndReplay` 的 `!changed` 分支直接放弃重放——首次请求即严重溢出时可能没有有效恢复路径。
- **问题**：超大历史或短上下文模型下，压缩后仍超限时直接失败，而不是继续裁最旧项。
- **建议**：opt-in `ARGENT_COMPACT_DRAIN=1`：摘要后重新估算仍超预算时按前缀逐条移除最旧非系统消息（循环有上限），复用 `resetBudgetReminder()` 与事件通知；仍受 `MaxCompactRetries` 约束。
- **验收**：`internal/agent/compact_drain_test.go`：伪 Chatter 两次报告 context overflow，断言第二次裁掉最旧消息并成功；无上限空转；关闭时行为不变；`go test ./internal/agent -count=1`。

### 127. 可持久化的审批/作用域规则 `[P2/M-L]`（codex + opencode）
- **竞品证据**：codex `protocol/src/protocol.rs:4618-4635` 的 `ReviewDecision` 含 `ApprovedForSession`/`ApprovedExecpolicyAmendment`/`ApprovedMcpPolicyAmendment`/`NetworkPolicyAmendment`；`execpolicy/src/amend.rs:62-78` 用文件锁把前缀规则追加到策略文件，跨会话生效。opencode `core/src/permission.ts:334-395` 拒绝时按 session+messageID 撤销整批 pending，`always` 时持久化 saved rule（`permission/saved.ts:69-84`）。
- **argentgo 现状**：`internal/security/approver.go:232-274` 的 `ScopedApprover` 有 `GrantAlways`/`GrantN`/`Deny`（粘性 deny 更保守，应保留），但只在进程内存且按工具名键控；`chain.go:167-170,415-483` 的 `StagedApprover`/`RememberScope` 无生产实现者；配置侧 `app/policy_config.go:48-70` 无运行时追加。已有 `approver_ttl.go` 的 `TTLCache` 支持持久 grants，但未接到 Chain 主路径。
- **问题**：跨会话重复审批同一前缀规则；拒绝时整批 pending 的撤销策略未落地。
- **建议**：opt-in `ARGENT_APPROVAL_PERSIST=1`：`internal/security` 实现 `FileBackedScopedApprover` 写 `<storage>/approvals.jsonl`（复用 `TTLCache` 持久化与 `PolicyIssues` 校验），只记录命令前缀/工具名，不记录自由文本参数；同时给 RPC `approval.answer` 加可选 feedback 字段并接 `ParseApproverResult`（`approval_feedback_adapter.go:11-58`，目前零生产调用）。默认仍走内存语义。
- **验收**：`internal/security/persistent_approval_test.go`：always 授权重启后仍生效、粘性 deny 优先、文件损坏时 fail-closed；`go test ./internal/security -count=1`。

### 128. 类型化 telemetry 适配层（span/metric schema + NOOP）替代散点 slog `[P2/M-L]`（学自 pi-mono）
- **竞品证据**：pi `packages/telemetry/src/index.ts:1-90` 定义 Span/Metric/Store/Propagation 契约；`metrics.ts:30-120` 的 counter/upDownCounter/histogram 与 schema 绑定；`noop.ts:1-35` 共享 noop context（零行为）；`agent-session.ts:3895-3918` 把 retry 事件结构化发出。
- **argentgo 现状**：`internal/agent/loop.go:276-330,352-477`（TURN_TIMING / TOOL_TIMING_HISTOGRAM / TOOL_CALL_TRACE / CONTEXT_HEALTH_LOG / PRESSURE_LEVEL 多为轮末 slog）、`internal/llm/debug_trace.go`、`internal/cli/trace_sink.go` 观测点丰富但无统一 span/metric 契约，无法跨包关联一次运行。
- **问题**：诊断日志之间无法用统一的 run/span ID 关联；新增观测点只能复制 slog 模式。
- **建议**：新增 stdlib-only 的 `internal/telemetry` 接口包（Context/Span/Counter/Histogram + NOOP 单例），先让现有 opt-in 日志点作为第一个 adapter；默认 NOOP 不分配，事件/记录字节不变。无需新模块。
- **验收**：`internal/telemetry/telemetry_test.go`：noop 实现零分配（benchmark）、span 父子关系与属性 schema 验证；现有观测点开启后输出含统一 run ID；`go test ./internal/telemetry ./internal/agent -count=1`。

### 130. per-call 文件系统/网络权限 profile（不引入 OS 沙箱）`[P2/M]`（学自 codex）
- **竞品证据**：codex `protocol/src/models.rs:99,254,267` 定义 `FileSystemPermissions`/`NetworkPermissions`/`AdditionalPermissionProfile`；`permissions.rs:131-139,152-156,257-275` 定义 Read/Write/Deny 与 special path，deny 优先于 write；unified_exec 可带 per-call `sandbox_permissions`（`handlers/unified_exec.rs:82-83`）。
- **argentgo 现状**：`internal/security/sandbox.go:11-19` 只有三档全局 `SandboxMode`；`policy.go:69-91` 是 denyPaths/denyCommands/writeRoots 粗粒度列表；`sandbox_execution.go:106-140` 已有 enforcement/denial/fail-closed 三维描述但文件头明确「纯数据纯函数，不接入 Chain.Check」，无生产接线。bwrap/seatbelt/landlock 属 OS 沙箱，在「无 cgo / 单二进制」约束下不可照搬。
- **问题**：MCP/插件工具无法声明「此调用只读某目录」之类细粒度权限。
- **建议**：不碰 OS 沙箱，在工具层把 `sandbox_execution.go` 现有描述升级为可传入 per-call profile：`internal/security/policy.go` 加 `CheckWith(ctx, name, args, profile)`，MCP/插件经 hook 注入可选 profile；保持 `Check` 原路径不变。
- **验收**：`internal/security/per_call_profile_test.go`：per-call deny 优先于全局 write、只读 profile 拒绝修改类工具；默认 `Check` 行为不变；`go test ./internal/security -count=1`。

### 133. 每个外部程序独立的 UTF-8→ACP 增量解码 `[P2/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/shell/src/output_decode.rs:89-109` 的 `push` 快路径，`:122-172` 的 `drain_utf8` 线性游标，`:195-220` 的 `external_output_decoding`，`:236-257` 的 `CaptureDecoder` 按行判定（`MAX_HELD_LINE=64*1024`）；遇完整非法序列后该程序剩余输出永久锁 ACP，不再切回。
- **argentgo 现状**：`internal/proc/utf8_writer.go:14-19,67-121` 只做 chunk 边界的 UTF-8 暂存，非法字节原样透传成 U+FFFD；`internal/bash/tools_builtin_bash.go:217-229` 仅在 cmd.exe 分支前置 `chcp 65001`，bash 分支禁止加（`:229`），中文 Windows 上经 Git Bash 启动的原生程序（写 CP936）仍乱码。
- **问题**：中文 Windows 下 shell 捕获的程序输出乱码，影响模型理解错误信息。
- **建议**：opt-in `ByteDecoder` 包住每个 `proc.Run` 的 stdout/stderr：UTF-8 合法直通，遇完整非法序列经 `syscall.NewLazyDLL("kernel32.dll")` 调 `MultiByteToWideChar` 并锁存 ACP 到 EOF；capture 按行判定避免污染相邻行；保留现有 `trimIncompleteRuneTail/Head` 边界语义。
- **验收**：`internal/proc/acp_decode_windows_test.go`：模拟 CP936 字节流解码成正确中文；合法 UTF-8 路径不产生额外开销；`GOOS=linux go build ./...` 仍绿；`go test ./internal/proc -count=1`。

### 134. foveated 多保真压缩归档（纯文本版起步）`[P2/L]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/snapcompact/src/snapcompact.ts:2136-2220` 的 `planArchive`：两端高保真（`:2154-2156`）、中段成框（`:2195-2214`）、超预算丢最旧 dense（`:2207-2212`）；按读者能力回放 `:2258-2298`；三层预算 `:519-544`。
- **argentgo 现状**：已有 opt-in 无损磁盘归档（`internal/session/compact.go:907-988`），但只生成一段「可恢复说明」（`:1085-1097`），不回灌上下文；压缩视图固定为单条 system summary（`session_message.go:131-138`）。
- **问题**：超长会话被压成一段摘要后丢失大量细节；全量归档模型读不动，用户没有分层回放出口。
- **建议**：分两步：第一步只做纯文本版（两端高保真 + 中段稀疏化 + 显式 gap 标记），复用现有 `session.Project`/`PruneToolResults`，零 wire 变化；第二步才用 `image/png` + 内嵌位图字体生成 frame 并按需回放（L 级，可长期排期）。
- **验收**：`internal/session/foveated_archive_test.go`：1000 条消息的归档视图两端逐字保留、中段按预算稀疏、gap 标记可解析；关闭 opt-in 时 `Messages()` 逐字节不变；`go test ./internal/session -count=1`。

---

## 附：优先处理建议

> 2026-11-23 更新：以下顺序综合了新一轮 6 个竞品源码深读的结论；已落地条目（原 #2/#11/#14/#25/#30/#32/#54/#58/#60/#61/#100/#136，以及 #1/#15/#39/#74~#77/#81/#82/#84~#87/#90/#92/#94/#101~#103/#107~#110/#113/#116~#118/#123~#125）已从本文档删除。P0、hook、小项与发版门禁批次均已落地并全绿。

1. **第一优先（真 bug / 安全绕过）**：已全部完成——#101（apply_patch 策略绕过）、#102（畸形参数以 `{}` 执行）、#103（带图消息 token 估算为 0）连同原清单 P0（#1/#15/#39/#74/#75/#76/#77/#94）均已落地，默认路径逐字节不变。
2. **第二优先（hook 表达力与压缩事件接线）**：#108/#109/#110/#112/#123 已全部落地（allow 预审批章、updatedInput/Output 改写、PreCompact/PostCompact 事件、事件日志/SSE 续传、todo 入摘要）。
3. **第三优先（小而实的体验/正确性）**：#107/#113/#116/#117/#118/#124/#125 已全部落地（patch context 锚定、生成文件保护、data URL 裁剪、grep per-file、MCP annotations 审批、read-before-write、分支摘要）。
4. **架构级改造单独排期**：#104（实测锚点计量）、#105（后台预压缩）、#106（重试省略+退避）、#120（统一 exec）、#128（telemetry），以及 #134（foveated 归档）属于 L/M-L 级，需要与现有会话/agent 热路径的逐字节基线一起评审（#112 已落地）。
5. **发版门禁**：#74~#93 与 #135 均已落地（版本/文档/CI/脚本一致性、行数锚点机器校验、fuzz workflow、产物自报版本校验、离线 model-quality 评测、体积/启动门禁）。
6. **验证方式**：每条的「验收」栏都给了可执行命令；涉及默认路径的改动一律先跑 `gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1` 与逐字节 golden，再跑发版门禁 `-race`/`GOOS=linux go build ./...`。
