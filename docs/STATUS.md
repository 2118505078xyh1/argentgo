# 项目状态

argentgo 要往哪里去（Roadmap），以及一份基于证据的自我评估（Honest score）。行为清单在 [Technical reference](TECHNICAL.md#feature-parity) 中。

用户可感知能力与 Claude Code / OpenCode / Crush / Codex 的逐维对标见 [Capability matrix](CAPABILITY_MATRIX.md)；本文档 加权 8.85 / 等权 8.0 分评估的是架构/安全/测试证据，两者口径不同。

## Roadmap

Rust 参考实现（`argent` v0.4.0）已在上游归档，其本地检出也已删除。`argentgo` 是唯一本地的事实来源。这份路线图记录接下来要做什么以及为什么；`docs/TECHNICAL.md` 的 Feature parity 一节仍是行为清单。

### 下一步清单的来源

2026-11-04 → 11-11 的每周 star 增量由用户以快照形式提供：DeepSeek Harness (dsh)、Hermes Agent、Codex CLI、OpenCode、Pi。GitHub 不公开每周增量，因此这个排序是该快照的说法，不是独立验证过的统计。下面的功能是通过阅读各项目的主要文档选出的，而不是照抄它们的实现。这里没有任何一项会引入 Go 依赖、cgo 或托管服务。

### Wave 5 —— 补齐生产接线缺口（完成）

下面六项都已完成；完成状态见 `docs/TECHNICAL.md` 的 Feature parity 一节。

- 配置好的安全规则会进入 `security.Policy`，而不是空策略。
- hook 文档会派发 PreToolUse/PostToolUse/SessionStart/SessionEnd。
- 已验证自动压缩默认开启。
- TUI 增加 tasks / workspace / plan 面板。
- MCP 遵守每个服务器的 `enabled`、`readOnly`、`allowedTools`、`deniedTools`。
- `current_time`、`web_search`、`repo_map`、`submit_plan`、`tool_search` 已存在。

### Wave 6 —— 来自五个项目的可移植点子（进行中）

这一波由另一位 worker 负责，工作树中已有进行中的改动；本轮不触碰它的任何代码。

| # | 功能 | 来源点子 | 验收标准 |
|---|---|---|---|
| 1 | `/review` 只读 diff 评审 | Codex CLI | 脏树 → 给出发现；之后树的字节完全一致 |
| 2 | `sessions --grep` 全文搜索 | Hermes | 能找到工具输出中的匹配；片段已脱敏 |
| 3 | shell 超时 → 后台 job | OpenCode | 长命令在工具超时后仍存活；返回 job id |
| 4 | `/tree` 分支导航 + 离线 HTML 导出 | Pi | fork 的会话能渲染；输出中没有网络引用 |
| 5 | 自写 `SKILL.md` | Hermes + dsh | `save_skill` 写入一个可被发现的 skill；可选启用 |

从这些项目中明确拒绝的有：Node/Cordis 插件图、SQLite/FTS5、Honcho 用户建模、聊天平台机器人、Go 标准库之外的操作系统沙箱原语、远程沙箱、JS code-mode 虚拟机和托管目录。它们要么是重量级依赖、服务，要么是定位上的取舍，而不是可移植的行为。

### killi 研究迁移（2026-11-23）

从 Rust agent `killi`（github.com/timwuhaotian/killi）移植了资源有界、缓存纪律与可复现性三方面的多项改进：`read` 尺寸预检、prompt 预算护栏、钩子进程组终止、`ARGENT_LOOP_DETECTION` 接线、`eval history` 系列（git 历史任务 + worktree 隔离 + 离线自检 + `--scripted`/`--verify`）、`ARGENT_REQUEST_SNAPSHOT` 请求字节快照与 `ARGENT_RESUME_BYTE_REPLAY` 字节回放、`ARGENT_MCP_GATEWAY` 单网关、子代理共享 system/工具表、压缩后逐条淘汰、follow-up 队列。三项高风险迁移（MCP 网关、字节回放、子代理工具表）均已落地。逐项提交号与取舍说明见 `docs/KILLI_STUDY.md`。

### 发布门禁

1. `go vet ./...`、`go test ./...`、`gofmt -l .` 在 Windows 和 Linux 交叉编译上都干净。
2. `argentgo eval evals/core.json` 能通过脚本化 provider 和真实 agent 循环（不联网）。
3. `argentgo gui` 端到端：stream → 审批 → 工具 → `turn_done`。
4. 隐私扫描：已跟踪文件和 git 历史中没有密钥、token、真实邮箱或个人路径。推送前本地路径替换为 `<local-checkout>`。
5. 用 writer workspace 中的笔名作为提交作者推送；不包含其他个人信息。

### 发行范围

发行脚本 `scripts/build-release.ps1` 已收口为 Windows/amd64 单平台（本机实测
`GOOS=windows GOARCH=amd64`），只打包当前电脑能用的版本，其他平台不考虑。

自更新的 GitHub Release 路线不启用：本地自用，无需跨平台上架。

### 分发与 IDE

- `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` 是受支持的源码分发路径（Go 1.27.3+）；预编译的 Windows 发布 zip 仍是主要途径。见 [User guide](USER_GUIDE.md)。
- IDE 级 LSP：`documentSymbol` / `references` / `codeAction` / `rename` 等进阶方法已实现；下一步是补进阶方法硬断言与更多语言 server 验证（R24 已补真实 gopls 的 best-effort 结构化验证）——只用标准库，不新增 Go 模块。
- 超出范围：C-ABI 插件加载器、tree-sitter AST 和 DAP 适配器。

## Honest score

### 评分标准（锚点）
- **10** = 业界标杆：功能完整 + 有客观/可复现证据验证
- **8** = 功能齐全可用，但缺深度或验证
- **6** = 基础可用
- **4** = 有但初步
- **2** = 几乎没有

原则：分数必须附证据；失败如实上报；客观天花板（真实社区/托管发布）如实标注，不靠本地代码凑分。

### 最终评分表（源码证据口径）

本表由三个项目（argentgo / pi / omp）各自独立的源码取证得出：**只读 git tracked 代码，不采信 README 的自我声明**，逐维度区分"已实现 / 仅桩 / 无"。硬数据（行数、测试数）统一用 `git ls-files -z | xargs -0 wc -l` 复核——早期子代理给出的 pi 测试 95,596 行、omp 测试 90,294 行为漏统计，正确值分别为 360,816 与 1,119,502 行。

| 维度 | argentgo | pi (rpi) | omp | 源码证据要点 |
|---|:---:|:---:|:---:|---|
| 上下文管理/可恢复性 | **9** | 8 | **9** | arg：`checkpoint.go` SHA-256 清单 + 冲突检测、`compact.go` 1097 行 + 归档、recovery fuzz；pi：`session-manager.ts` 2036 行 + `agent-session.ts` 4578 行有 branch/navigateTree/fork，但 CLI 不依赖 durable 栈；omp：compaction 2561 行 + TTSR + advisor + memory curation + git-based checkpoint。omp 附加能力最全，arg 回滚最精细 |
| 工具/执行深度 | 8 | 6 | **10** | arg：35 工具 + 自研 shell（lexer/parser/interp）+ LSP 6 工具（注入前报不可用）；pi：**核心仅 8 工具**，无 LSP，subagent/plan 只在 `examples/extensions`（默认关闭）；omp：30+3 工具 + 12 个 Rust crate（shell 152rs/builtins 187rs/natives 105rs）+ LSP 22 文件 + DAP + 常驻 kernel + browser 50+ 文件 + ast_edit/hashline |
| 安全边界 | **9** | 3 | 8 | arg：`chain.go` 策略→审批→审计 + 3 sandbox + 路径逃逸 4 轮解码 + 7 fuzz 目标 + 脱敏先于审计；pi：**文档明写无内置审批/沙箱**，permission-gate 仅 34 行示例；omp：`approval.ts` 960 行完整策略链 + security 20 文件 + redaction，但**无 OS 级沙箱** |
| 模型/协议广度 | 8 | 9 | **10** | arg：3 协议真实实现 + 20 模板 + 12 别名；pi：10 种 KnownApi + 43~51 provider + 模型目录生成链 + OAuth；omp：99 provider + 261 个 KDL 规则 + 完整编译器 |
| 交互界面 | 8 | 8 | **10** | arg：TUI 13,524 行 + MyGo 原生 GUI（无 WebView）；pi：TUI 20,269 行差速渲染 + 组件体系 + theme；omp：TUI 816 个 .ts + collab-web SolidJS + browser-relay Chrome 扩展 + kitty 图形协议 |
| 部署/零依赖 | **10** | 6 | 4 | arg：核心 stdlib-only，mygo 仅 native/，无 cgo，5 平台交叉编译；pi：默认 Node 22 + ~20 外部依赖，但可 `bun build --compile` 单文件；omp：强依赖 Bun + Rust nightly + Bazel，musl 动态链接 libstdc++/libgcc |
| 工程/测试纪律 | 9 | 9 | **9.5** | arg：1,265 测试文件/257,428 行（占 Go 63%）+ archguard 6 条分层规则 + 版本一致性门禁；pi：1,302 文件/360,816 行 + check 链 9 项 + 16 workflow + npm-audit（供应链最严）；omp：3,564 文件/1,119,502 行 + 7,442 个 Rust 测试注解 + ci.yml 63.7KB |
| 生态/扩展 | 6 | **9.5** | **9.5** | arg：MCP/skills/hooks/commands 有实现，但**插件仅声明式 bundle，无代码执行/SDK/包管理**；pi：ExtensionAPI 完整（registerTool/provider + 30 种事件钩子）+ 18 skills + SDK + RPC + chord 70k 行独立栈；omp：marketplace + 5 子模块 + Go/Python/Rust 三套 RPC SDK + 8 种外部配置导入 |
| 社区/治理/发布* | 4 | 8 | 8 | *部分无法从源码判定：arg 单人维护、release 未端到端验证；pi RFC + pr-gate/issue-gate + HF 数据集；omp Stencil Labs 商业背书 + Marketplace |
| 性能/资源 | 9 | 8 | **9.5** | arg：25 bench + 大输出落盘/环形缓冲/哈希缓存，**无 pprof/GC 调优**；pi：bench 广泛 + profile-coding-agent-node.mjs 672 行 + 差速渲染；omp：68 bench + Rust in-process grep/glob/fd（绕过 fork/exec）+ 推测执行/预热 + profiling |

### 加权总分

| 口径 | argentgo | pi | omp |
|---|:---:|:---:|:---:|
| **等权平均** | **8.0** | 7.45 | **8.7** |
| **按本项目优先级加权**（上下文20%/安全15%/工具15%/工程15%，其余各7%） | **8.85** | 7.74 | **8.96** |

**与旧自评的差异（诚实说明）**：旧表给 argentgo 8.9、omp 8.5（基于文档声明）。源码取证后 omp 反超（8.7 / 8.96），原因是 omp 的工具深度、provider 广度、生态在代码里的实现规模比文档估计更实；而 argentgo 的三个折扣点被源码证实：`internal/agent/context_pressure.go` 纯原语未接线、LSP 工具在 app 注入前报告不可用、`internal/plugins/` 仅声明式 bundle（无代码执行/SDK/包管理）。按本项目优先级三者几乎打平（8.85 vs 8.96）——差距不在上下文/安全/工程，而在工具宽度与生态。

R22–R26 新能力证据见 [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md) 的 R24 / R25 / R26 更新记录（含 `session.tree` RPC、原生分支树渲染、opt-in 真实模型评测批处理、进阶 LSP 真实 server best-effort 验证、`auto_verify` 等）；R33 / R35 / R36 / R37 与 R39–R43 的已落地增量见下方小节与矩阵同名更新记录。

### R33–R37 已落地增量（已测试证据）

以下均已在源码中接线并有表驱动测试；opt-in 项默认关闭、逐字节不变，不计入"功能缺失"扣分。

- **原生推理（R36）**：`--thinking low|medium|high|xhigh`（含中文别名）经 `internal/provider/thinking.go` 的 `ResolveThinking` 映射为线上原生字段——OpenAI chat `reasoning_effort`、Responses `reasoning.effort`、Anthropic `budget_tokens`（钳制下限 1024）；FIM 不携带推理参数，默认不传时请求字节不变。Anthropic `signature_delta` 闭环随 `Message`/`Entry`（`omitempty`）保存，多轮仅带签名回放 thinking 否则降级文本（`internal/llm/reasoning_signature_test.go`、`internal/session/reasoning_signature_test.go`）。
- **会话 rewind（R35+R36）**：`Checkout` / `OpenSessionAt` + `sessions checkout <id> <entry>`，以及 `--session X --at Y` 就地切换活动叶续写（`internal/session/branch_checkout.go`、`internal/app/sessions.go`、`internal/cli/sessions_checkout_test.go`）；R37 `ForkBefore` 提示恢复接入 fork-at/list-messages。
- **后台 jobs（R36）**：作业注册表生产接线（创建/持久化/`StopAll`），opt-in `ARGENT_JOBS=1` 或 `ARGENT_OPT_INS=jobs`（`internal/app/app_jobs_wire.go` + 测试）。
- **TUI 交互（R33+R37）**：每轮统计行 `模型 · 时长 · 平均 tok/s`（默认、缺数据不显示）；推理块鼠标点击展开/折叠（CJK 安全）+ `Ctrl+X` 全局思考切换（逐块覆盖）；`/` 补全浮层与路径/`@mention` 补全（引号感知、fuzzy、仅标准库）。用量行增强（速度/缓存命中/目录）受 `ARGENT_TUI_USAGE=1` 门控。
- **精确测量（R36+R37）**：token/s 排除 TTFT、按 Σtokens/Σdurations 聚合，经 `llm.Usage`（`json:"-"`）→ agent → app 桥接 → TUI `Event`（引用计数 opt-in）；`eval compare` 配对统计 + n<30 样本不足守卫，评测框架灵敏度自测注入故障断言完成率下降。
- **安全（R35+R37）**：baseURL 来源追踪 fail-closed（`--trust-project` 放行）、Windows DPAPI 凭据加密（syscall 无 cgo）、read-only MCP 绕过修复、分阶段审批状态机（once/always/reject/esc）。
- **仍为默认关闭原语（R37 备料 R38）**：`StableSystemPrompt`、`SessionStatusRegistry`、`CriticResult`、`CondensationEvent`、`PruneToolResults`、`FileFreshness`、`TurnTiming` 等已实现并测试，但未接入用户路径，不计入能力清单。

### R39–R43 已落地增量（已测试证据）

以下均已在源码中接线并有表驱动测试；opt-in 项默认关闭、逐字节不变，不计入"功能缺失"扣分。逐轮细节见 [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md) 的同名更新记录。

- **测量闭环（R39）**：`eval ab`（`internal/eval/variant_ab.go` 的 `RunVariantAB`）按场景指纹配对两臂再跑配对统计，`internal/cli/eval.go` 带外分派；配套可接受基线回归账本（三态判定）与 token 经济配对诊断（完成率增益对照 token 成本）。n<30 仍记"样本不足"，不据少量样本刷分。
- **agent 质量指标（R39，opt-in）**：恢复成效账本（`ARGENT_RECOVERY_METRICS`）、跨情节重复踩坑签名计数（`ARGENT_REPEAT_PITFALL_TRACKING`）、错误签名归一化、上下文健康评分与观察适配器；子代理取消时成本对账修复统计缺失。规则发现（`ARGENT_RULE_DISCOVERY`）与 checkpoint 守卫（`ARGENT_CHECKPOINT_GUARD`）接入生产。
- **结构修复与可观测原语（R40）**：成本对账算术下沉 `internal/calcutil` 单一真源，消除 import 环；根除两个长期 flaky 测试而不弱化断言（变异反证确认内容回归仍失败）；新增工具耗时直方图、会话树 ASCII 导出、磁盘用量/上下文快照/配置变更 diff、提示注入扫描与 shell 风险分级等原语（默认字节不变）。
- **Hook additionalContext（R41）**：hook stdout 的 `additionalContext` 经脱敏 + 8192 字节上限提取，端到端注入下一轮模型上下文（SessionStart/UserPromptSubmit 在 `bridge.start` 前接线，不丢失）；doctor 增加配置注入与会话健康检查（warn 通道，不改退出码）。
- **MCP 注入扫描（R41，opt-in）**：`ARGENT_MCP_INJECTION_SCAN=1` 对高风险注入前置可读警示，只警示不改写、不过滤原文；已登记进 `IndependentOptIns` 与 `OPT_INS.md`，漂移守卫 4/4 覆盖。
- **计划审批循环（R42）**：只规划模式（`ARGENT_PLAN_MODE=1`）下 `exit_plan` 工具驱动计划批准闭环，未批准前修改类 bash 被既有计划守卫拦下；仅该模式注册工具，默认工具目录逐字节不变。
- **流中断回补（R42+R43）**：三协议 `ChatOnce` 非流式补答，仅在部分文本 + `ErrStreamError` + 活 ctx 时触发，每轮最多一次、只发精确后缀、带 `EventRetry stream_recovery`；`ARGENT_STREAM_RECOVERY=1`（R43）完成端到端用户接线，关闭时请求与事件逐字节不变。
- **可靠性加固（R43）**：server 四处 fire-and-forget goroutine 补 panic 恢复（不崩进程/不挂客户端，race-clean）；agent `callTool` panic 边界激活（panic 转普通工具错误）；JSONL `Close()` 加 fsync，干净退出不丢尾（崩溃恢复语义不变）。

### 客观天花板（诚实说明）
- **生态/扩展 6、社区/治理 4**：达到 10 需要真实发布运营（npm 包、官网、Discord 社区、包管理器仓库收录），这些不是本地代码能实现的，需要用户实际发布和运营。
- 其余 8 分维度：差距在"真实环境验证"（真实模型、真实生产任务、长期 CI 数据），可继续用工程手段部分缩小，但完全消除需要真实运行环境。
- **无法从源码判定的项**：真实模型通过率、社区活跃度、长期 CI 稳定性、真实生产任务表现——不为这些凑分。

### Linux 已知降级（诚实说明）

部署/零依赖 10 分只反映**编译期**证据：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 与 5 平台 matrix 全绿；项目在本机 Windows 开发与实测，**未在真实 Linux 环境跑过运行时验收**（TUI、原生 GUI、凭据存取未做 Linux 实机端到端验证）。已知降级点：

- **凭据加密**：Windows 用 DPAPI（`crypt32.dll` syscall，零依赖无 cgo）按当前用户加密令牌；非 Windows 无 DPAPI 可用，`internal/auth/token_crypt_other.go` 原样返回明文，仅靠 token 文件 `0600` 权限隔离。
- **TUI 剪贴板**：`CF_UNICODETEXT`（Win32 syscall）复制后端只在 Windows 实现（`internal/tui/clipboard_windows.go`）；非 Windows 的 `writeClipboard` 是空操作（`clipboard_other.go`），复制键无副作用。
- **原生桌面 GUI**：Windows-only，`argentgo gui` 在非 Windows 上有显式守卫直接报错返回（`cmd/argent/gui.go` 的 `runtime.GOOS != "windows"` 分支）；发布包也只收 Windows/amd64。

### 对比（源码证据口径）
- **等权平均**：omp 8.7 > argentgo 8.0 > pi 7.45
- **按本项目优先级加权**：omp 8.96 ≈ argentgo 8.85 > pi 7.74
- argentgo 领先项：部署/零依赖（10 vs 6/4）、安全边界（9 vs 3/8）；持平项：上下文管理（9）、工程纪律（9）；落后项：工具深度、provider 广度、交互界面、生态/扩展、社区治理
- pi 的"极简"是主动取舍：核心仅 8 工具、无 LSP/审批/沙箱，但 subagent/plan 在 `examples/extensions` 有完整实现（默认关闭）；其会话树与压缩实现强于"极简 harness"的印象，真正硬伤是安全（3 分）与 provider 数
