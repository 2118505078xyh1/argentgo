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

---

## 三、工具与执行深度（29–38）

---

## 四、模型/协议与召回（39–50）

---

## 五、交互界面与可用性（51–61）

---

## 六、生态与扩展（62–73）

---

## 七、工程纪律 / 测试 / CI / 发布（74–93）

---

## 八、性能 / 资源 / 可观测（94–99）

---

## 九、竞品源码深读新增（101–135）

### 106. 重试前的持久上下文省略 + 可中止退避 + 摘要调用共享退避 `[P1/M]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/agent-session.ts:1269-1283` 的 `_omitRecoveryAttempt()`，`:1313-1354`/`:3936-3995` 在重试前对失败助手消息及其工具结果写 `appendContextEdit(targetId, null)` **永久省略**；`pi-mono/packages/ai/src/utils/retry.ts:160-164` 的退避可被 `abortRetry` 中断；`:2073-2080,3883-3918` 让压缩/分支摘要的 LLM 调用复用同一 retry 预算与事件。
- **argentgo 现状**：`internal/agent/fallback.go:48-160` 在可重试错误时立即切换模型，无退避、失败消息留在 `s.messages`；`internal/agent/compact_retry.go:44-100` 的压缩重放是 opt-in 且最多一次，同样保留失败消息。`internal/session/context_edit.go:57` 的 `AppendContextEdit` 已有 Text 替换/隐藏语义，但只有 CLI 手动调用。
- **问题**：失败尝试及其工具结果持续占用上下文，重试时重复污染；瞬时限流立即切模型可能不是最优策略。
- **建议**：opt-in 同模型重试策略（`maxRetries`/base/cap），重试前通过 app 回调把失败助手消息写 `ContextEdit{TargetID, Text:""}` 永久省略；退避可被 ctx 取消；复用 `internal/llm/retry.go:416-440` 的分类。默认仍走当前模型回退，事件/字节不变。
- **验收**：`internal/app/retry_omit_test.go`：429 → 重试成功，断言失败轮被 context_edit 省略、退避受 ctx 取消、默认路径无 edit 记录；`go test ./internal/app ./internal/agent -count=1`。

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

---

## 附：优先处理建议

> 2026-11-23 更新：以下顺序综合了新一轮 6 个竞品源码深读的结论；已落地条目（原 #2/#11/#14/#25/#30/#32/#54/#58/#60/#61/#100/#136，以及 #1/#15/#39/#74~#77/#81/#82/#84~#87/#90/#92/#94/#101~#103/#107~#110/#113/#116~#118/#123~#125）已从本文档删除。P0、hook、小项与发版门禁批次均已落地并全绿。

1. **第一优先（真 bug / 安全绕过）**：已全部完成——#101（apply_patch 策略绕过）、#102（畸形参数以 `{}` 执行）、#103（带图消息 token 估算为 0）连同原清单 P0（#1/#15/#39/#74/#75/#76/#77/#94）均已落地，默认路径逐字节不变。
2. **第二优先（hook 表达力与压缩事件接线）**：#108/#109/#110/#112/#123 已全部落地（allow 预审批章、updatedInput/Output 改写、PreCompact/PostCompact 事件、事件日志/SSE 续传、todo 入摘要）。
3. **第三优先（小而实的体验/正确性）**：#107/#113/#116/#117/#118/#124/#125 已全部落地（patch context 锚定、生成文件保护、data URL 裁剪、grep per-file、MCP annotations 审批、read-before-write、分支摘要）。
4. **架构级改造单独排期**：#104（实测锚点计量）、#105（后台预压缩）、#106（重试省略+退避）、#120（统一 exec）、#128（telemetry）属于 L/M-L 级，需要与现有会话/agent 热路径的逐字节基线一起评审（#112 已落地）。#134（foveated 归档纯文本版第一步）已落地：opt-in `ARGENT_FOVEATED_ARCHIVE` 基于配对单元做两端保真 + 中段稀疏 + 可解析 gap，零 wire 变化。
5. **发版门禁**：#74~#93 与 #135 均已落地（版本/文档/CI/脚本一致性、行数锚点机器校验、fuzz workflow、产物自报版本校验、离线 model-quality 评测、体积/启动门禁）。
6. **验证方式**：每条的「验收」栏都给了可执行命令；涉及默认路径的改动一律先跑 `gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1` 与逐字节 golden，再跑发版门禁 `-race`/`GOOS=linux go build ./...`。
