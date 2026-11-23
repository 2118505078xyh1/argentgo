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

---

## 附：优先处理建议

> 2026-11-23 更新：以下顺序综合了新一轮 6 个竞品源码深读的结论；已落地条目（原 #2/#11/#14/#25/#30/#32/#54/#58/#60/#61/#100/#136，以及 #1/#15/#39/#74~#77/#81/#82/#84~#87/#90/#92/#94/#101~#103/#107~#110/#113/#116~#118/#123~#125）已从本文档删除。P0、hook、小项与发版门禁批次均已落地并全绿。

1. **第一优先（真 bug / 安全绕过）**：已全部完成——#101（apply_patch 策略绕过）、#102（畸形参数以 `{}` 执行）、#103（带图消息 token 估算为 0）连同原清单 P0（#1/#15/#39/#74/#75/#76/#77/#94）均已落地，默认路径逐字节不变。
2. **第二优先（hook 表达力与压缩事件接线）**：#108/#109/#110/#112/#123 已全部落地（allow 预审批章、updatedInput/Output 改写、PreCompact/PostCompact 事件、事件日志/SSE 续传、todo 入摘要）。
3. **第三优先（小而实的体验/正确性）**：#107/#113/#116/#117/#118/#124/#125 已全部落地（patch context 锚定、生成文件保护、data URL 裁剪、grep per-file、MCP annotations 审批、read-before-write、分支摘要）。
4. **架构级改造单独排期**：#104（实测锚点计量）、#105（后台预压缩）、#106（重试省略+退避）、#120（统一 exec）、#128（telemetry）属于 L/M-L 级，需要与现有会话/agent 热路径的逐字节基线一起评审（#112 已落地）。#134（foveated 归档纯文本版第一步）已落地：opt-in `ARGENT_FOVEATED_ARCHIVE` 基于配对单元做两端保真 + 中段稀疏 + 可解析 gap，零 wire 变化。
5. **发版门禁**：#74~#93 与 #135 均已落地（版本/文档/CI/脚本一致性、行数锚点机器校验、fuzz workflow、产物自报版本校验、离线 model-quality 评测、体积/启动门禁）。
6. **验证方式**：每条的「验收」栏都给了可执行命令；涉及默认路径的改动一律先跑 `gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1` 与逐字节 golden，再跑发版门禁 `-race`/`GOOS=linux go build ./...`。
