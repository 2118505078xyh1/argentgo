# 项目状态

argentgo 要往哪里去（Roadmap），以及一份基于证据的自我评估（Honest score）。行为清单在 [Technical reference](TECHNICAL.md#feature-parity) 中。

用户可感知能力与 Claude Code / OpenCode / Crush / Codex 的逐维对标见 [能力对标矩阵](#能力对标矩阵)；本文档 加权 8.85 / 等权 8.0 分评估的是架构/安全/测试证据，两者口径不同。

> **默认化原则（用户 2026-11-23，覆盖下文历史条目中的「opt-in 默认关闭、逐字节不变」口径）**：凡改善体验的新功能一律默认开启、参数取最优值、用户零选择；环境变量仅留 `=0` 紧急回退。下文 R22–R43 各条目中「默认关闭/逐字节不变」的描述是当时的客观记录，保留作历史；自本轮起以 `AGENTS.md` 的默认化原则与 `todolist.md` 文末「参数归位」为准。

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

从 Rust agent `killi`（github.com/timwuhaotian/killi）移植了资源有界、缓存纪律与可复现性三方面的多项改进：`read` 尺寸预检、prompt 预算护栏、钩子进程组终止、`ARGENT_LOOP_DETECTION` 接线、`eval history` 系列（git 历史任务 + worktree 隔离 + 离线自检 + `--scripted`/`--verify`）、`ARGENT_REQUEST_SNAPSHOT` 请求字节快照与 `ARGENT_RESUME_BYTE_REPLAY` 字节回放、`ARGENT_MCP_GATEWAY` 单网关、子代理共享 system/工具表、压缩后逐条淘汰、follow-up 队列。三项高风险迁移（MCP 网关、字节回放、子代理工具表）均已落地。逐项提交号与取舍说明见 本文件"killi 研究"章节。

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

R22–R26 新能力证据见 [能力对标矩阵](#能力对标矩阵) 的 R24 / R25 / R26 更新记录（含 `session.tree` RPC、原生分支树渲染、opt-in 真实模型评测批处理、进阶 LSP 真实 server best-effort 验证、`auto_verify` 等）；R33 / R35 / R36 / R37 与 R39–R43 的已落地增量见下方小节与矩阵同名更新记录。

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

以下均已在源码中接线并有表驱动测试；opt-in 项默认关闭、逐字节不变，不计入"功能缺失"扣分。逐轮细节见 [能力对标矩阵](#能力对标矩阵) 的同名更新记录。

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

---

## 能力对标矩阵

这份文档回答一个问题：**在用户真正能感知到的能力上，argentgo 与 Claude Code / OpenCode / Crush / Codex 相比，哪里已经追平、哪里还差、哪里是有意不做。**

它是后续迭代的方向盘，不是又一份评分表。取证基线为 a30b0e4（取证 commit，可用 git log 复核），所有落点均为源码中的真实行号；行号或开关名与源码不一致时，以源码为准。

### 0. 口径与读法

#### 0.1 与 STATUS 加权 8.85 / 等权 8.0 分的关系（重要）

[项目状态](#项目状态) 的 **加权 8.85 / 10（等权 8.0 / 10）** 评估的是**架构、安全与测试证据**：分层零违规、策略→审批→审计链、fuzz 目标、交叉编译矩阵、gopls 端到端测试等。这些分数高，说明"工程底座扎实"。

本文档刻意换一个镜头，只看**用户可感知能力**：一个真实用户坐到终端或桌面窗口前，能不能完成"读代码 → 改代码 → 回退 → 换模型 → 续会话 → 调工具"这条链路，以及做这些事时和主流竞品的体感差距。

> 举例：STATUS 给"安全性(架构) 10 分"，是因为链和 fuzz 都在；但用户感知到的"审批弹窗是否顺手""每行编辑是否要确认"是本文档的议题。两者不冲突，但不能互相替代。

#### 0.2 三个标注

| 标注 | 含义 |
|---|---|
| ✅ **已对标或领先** | 能力存在且可用，与竞品同档或更好；有源码落点支撑。 |
| ⚠️ **真实差距** | 能力缺失或明显弱于竞品，且**属于可移植、值得补**的一类；每条给出确切落点文件与行号，作为 backlog。 |
| ⊘ **合理取舍** | 明确不做；给出理由，并说明用什么替代。 |

#### 0.3 对标对象与限制

竞品一栏基于各项目的**公开产品形态与文档**归纳，不是对其源码逐行取证，也不锁定具体版本；口径是"这类产品当前普遍具备的能力"。凡竞品能力本身有争议的，本文档宁可标"视版本/配置而定"，不替对方夸大。

#### 0.4 一条准确性提示

[TECHNICAL.md](TECHNICAL.md) 的 Feature parity 表**不在本文档修改范围内**，且可能与源码存在时间差。例如该表把 `secondary_model` 列为 "in progress"（`docs/TECHNICAL.md:708`），但源码已在 `internal/app/app_execute.go:94` 把它赋给子代理 `SubagentConfig.Model`（`internal/app/app_execute.go:92-100`）。遇冲突时，**一律以源码行号为准**。

---

### 1. 上下文管理

#### 1.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 轮次预算与超额停止 | `internal/agent/budget.go:84`（`Breach`）、`internal/agent/stop_decision.go:21` | token / 成本 / 秒数三类停止条件。 |
| 压缩阈值（按比例 + 上限） | `internal/agent/budget.go:133`（`CompactionThreshold`）、`internal/agent/compact_threshold_effective.go:12` | 上下文窗口百分比与绝对上限取小。 |
| 历史裁剪 | `internal/agent/trim.go:129`（`Trim`）、`internal/agent/trim.go:112`（`budgetTarget`）、`internal/agent/trim.go:395`（`effectiveCacheProtect`） | 保留头尾、保护缓存段、按 rune 安全切点。 |
| 去重（重复工具调用拦截） | `internal/agent/guard.go:51`（`newDedupe`）、`internal/agent/guard.go:199`（`check`） | 归一化后判断重复参数并阻断。 |
| Steering（运行中注入用户指令） | `internal/agent/steer.go:17/25`、`internal/agent/loop_helpers.go:196`（`injectSteering`） | 下一轮前排空待注入消息。 |
| 失败信号进入压缩证据 | `internal/agent/failure_signal_wire.go:25/48`、`internal/agent/compaction.go:40`（`CollectCompactionEvidence`） | 失败不会在压缩时丢失。 |
| 压缩结果缓存 | `internal/agent/compaction.go:141`（`cachingCompactor`）、`:152`（`MaybeCompact`） | 同一证据不重复总结。 |
| 压缩后溢出重试 | `internal/agent/compact_retry.go:59`（`compactAndReplay`）、`internal/agent/compact_retry_wire.go:15` | 溢出错误触发压缩并重放。 |
| 会话压缩（结构化保留项） | `internal/session/compact.go:51/69`、`internal/session/compact.go:42-45`（`keep/files/failures/nextSteps`）、`internal/session/compact_structured.go:201`（`Structured`） | 压缩记录保留改动文件、失败、下一步。 |
| 保留策略 | `internal/session/retention.go:49`（`SurvivesCompaction`）、`:147`（`SelectForCompaction`） | 标记哪些记录必须跨压缩存活。 |
| 提示缓存 | `internal/llm/options.go:95`（`CacheSupport`）、`internal/llm/options.go:109-120`（`cacheEnabled`） | Anthropic Messages / Responses 默认开；OpenAI chat 兼容端点需 opt-in。 |
| 推理 / 思考 | `internal/llm/options.go:33-79`（`ThinkingConfig`、`PreserveReasoning`）、`internal/llm/anthropic.go:226`、`internal/agent/events.go:19`（`reasoning_delta`） | 协议无关的推理控制 + 事件透出。 |
| 原生推理参数（R36） | `internal/provider/thinking.go`（`ResolveThinking` / `DefaultThinkingBudget` / `normalizeEffort`）、`internal/llm/client.go:343-504`（`ReasoningEffort`）、`internal/app/app_resolve.go:295` | `--thinking low\|medium\|high\|xhigh`（含中文别名）经 `ResolveThinking` 映射为线上原生参数：OpenAI chat → `reasoning_effort`，Responses → `reasoning.effort`，Anthropic → `budget_tokens`（钳制下限 1024，上限取 `maxTokens-1`）。FIM 请求永不携带推理参数；默认不传时请求字节不变。 |
| Anthropic 思考签名闭环（R36） | `internal/llm/anthropic.go`、`internal/session/session_message.go`、`internal/session/reasoning_signature_test.go` | 捕获 `signature_delta` 并随 `Message`/`Entry`（`omitempty`）保存；多轮仅在带签名时回放 thinking 块，无签名则降级为纯文本，避免 400。 |
| 压缩跨链保留（R35） | `internal/session/compact.go`、`internal/session/compact_summarize_test.go` | 多轮压缩链持续保留 Goal / Failures / NextSteps，配合 R20 的 files/failures/nextSteps 结构化保留项。 |

#### 1.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 自动压缩 | 有，模型生成式摘要 + 后台触发 | 多为自动/半自动压缩 | 有，但摘要为抽取式（见 ⚠️-1） | ⚠️ |
| 提示缓存 | 隐式自动 | 部分产品依赖 provider 支持 | 有，按协议显式控制，覆盖面略窄 | ✅ |
| 上下文用量可视化 | 有状态栏百分比 | 多数有 token/成本显示 | TUI 有预算提醒与 usage 工具（`internal/tools/tools_productivity.go:154`） | ✅ |
| 推理内容保留 | 有（extended thinking） | 视 provider 而定 | 有，`PreserveReasoning` + `reasoning_delta` | ✅ |
| 结构化压缩证据 | CLAUDE.md 记忆 + 自动压缩 | 多依赖会话摘要 | 压缩记录显式保留 files/failures/nextSteps，强于一般实现 | ✅ 领先 |
| 语法级上下文（AST 裁剪） | 有 IDE 侧符号理解 | OpenCode 内建 LSP | 走 LSP 符号，不做树内 AST（见 ⊘-1） | ⊘ |

#### 1.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **压缩摘要是抽取式，不是模型生成式。** `Summarize` 用 `firstLine` / `nextStepLines` 逐行拼接，信息密度和连贯性弱于 Claude Code 的自动压缩摘要。**R22 已接线**：`ARGENT_OPT_INS=model_summary` + `secondary-model` 时，压缩调用模型生成增量叙事（3s 超时、失败回落），模型只补叙事、绝不覆盖 files/failures 权威字段；开销经 EventUsage 在事件流可见但**不计入预算/stats**（已知边界）。默认关逐字节不变。 | `internal/app/model_summary.go`（原语 + `GenerateModelSummaryWithUsage`）、`internal/app/model_summary_wire.go`（接线）、`internal/app/compact.go`/`integration.go`（两处压缩点） | 若需计入预算/stats，得让压缩器回传 usage 给 agent 走 account()（改 agent 接口，R21 评估代价大于收益，暂不做）。 |
| 2 | ~~会话侧截断仍按 rune，不是 grapheme。~~ **R20 已解决**：`session/` 的 4 处主路径截断（`session_message.go` 的 `truncateRunes`、`attempt.go` 的 `capProgressRunes`、`branch_summary.go`、`search.go` 的 `contextWindow`）统一改走 `internal/textutil/clip.go` 的 `ClipRunes`/`ClipWindow`——保持 rune 预算语义、只向字簇边界吸附，默认路径（ASCII/纯汉字/单独 emoji）逐字节不变；`implicit_context.go` 早已采用 `ClusterBoundaryAfter`。字簇边界为 UAX#29 务实子集（组合符/ZWJ/旗帜/肤色/jamo/CRLF），非完整 Unicode 合规。 | `internal/textutil/clip.go`（`ClipRunes`/`ClipRunesTail`/`ClipWindow`）；调用点 `internal/session/session_message.go`、`attempt.go`、`branch_summary.go`、`search.go`。STATUS.md「核心引擎」已升至 10 分。 | 残留：`internal/memory/sclip.go` 是第二套近似实现（RI 成对分组、非法字节丢弃语义不同），留待后续统一；不影响 session 主路径正确性。 |
| 3 | **提示缓存对 OpenAI 兼容端点默认关闭。** `cacheEnabled` 对 `openai-chat-completions` 返回 false，需显式 `--force-prompt-cache` 类开关，用户不知情时拿不到缓存收益。 | `internal/llm/options.go:109-120`（`cacheEnabled` 的 `default` 分支） | 增加按 `baseURL` 的探测/配置显式声明，不改默认安全语义；补充用户可见提示。 |

#### 1.4 ⊘ 合理取舍

- **⊘-1 不做内嵌 tree-sitter AST。** 会引入 cgo，破坏 `GOOS=linux go build ./...` 的静态交叉编译（见 STATUS.md:59、TECHNICAL.md:753）。替代：`repo_map` + 六个 `lsp_*` 工具，靠外部语言服务器提供符号，不捆绑解析器。

---

### 2. 可恢复性

#### 2.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 只追加 JSONL 会话树 | `internal/session/session_append.go:14/21/82` | `id`/`parent` 链接，fork 不重写历史。 |
| 分支 / fork | `internal/session/session_branch.go:41/54`、`internal/session/tree.go:31/68`（`Branches`） | 从任意记录分叉。 |
| Checkpoint（改动前快照） | `internal/session/checkpoint.go:122`（`CaptureCheckpoint`）、`:215`（`RecordCheckpoint`） | 记录改动前字节与 sha256。 |
| Undo（按 checkpoint 还原） | `internal/session/checkpoint.go:271`（`UndoCheckpoint`）、`:288`（`undoCheckpoint`） | 文件被改动后拒绝还原，还原也走安全链。 |
| Retry（从 checkpoint 重试） | `internal/session/checkpoint.go:480`（`RetryFromCheckpoint`）、`:518`（`Retry`） | 从 checkpoint 父节点长出新叶。 |
| 会话回退 / rewind（R35+R36） | `internal/session/branch_checkout.go:12`（`Checkout`）、`internal/session/store.go:67`（`OpenSessionAt`）、`internal/app/sessions.go:21`、`internal/cli/sessions.go:161`（`sessions checkout`） | 从会话树中任意记录就地切换活动叶并续写，不复制既有历史。CLI 两条入口：`sessions checkout <id> <entry>`，或 `--session X --at Y` 恢复（不带 `--at` 时走原 `OpenSession`，逐字节不变）。 |
| ForkBefore 提示恢复（R37） | `internal/session/fork_before.go`、`internal/cli/sessions.go`（`fork-at` / `list-messages`） | 在分叉点恢复当时的提示上下文，接线到 CLI 的 fork-at / 列消息路径（opt-in，默认字节不变）。 |
| 遗弃分支摘要 | `internal/session/branch_summary.go:47`（`SummarizeAbandonedBranch`） | fork 后的旁支不丢失上下文。 |
| 压缩归档 | `internal/session/compact.go:635/655/771` | 被丢弃消息写入归档，可回读。 |
| 离线导出 | `internal/session/export.go:91`（`ExportProject`）、`:94`，格式 `html`/`json`/`markdown` | 可选择脱敏，2 MiB 上限（`internal/session/export.go:25`）。 |
| CLI 恢复入口 | `sessions undo / retry / tree / export / find --grep`（TECHNICAL.md:423） | 命令级完整。 |
| `--rpc` 会话读取 | `session.list` / `session.load`（TECHNICAL.md:519-520） | 原生桌面窗口的会话栏依赖它。 |
| `--rpc` 分支树（R24） | `session.tree`（`internal/server/rpc.go:348-383`，复用 `internal/session/tree.go:68` 的 `Branches`） | 返回 `{branches:[...]}`；经 `SessionTreeStore` 可选接口发现，未实现时返回干净的 "not configured" 错误。原生桌面侧据此渲染分支（R26）。 |
| 流中断非流式回补（R42+R43） | `internal/llm/` 三协议 `ChatOnce`（`stream:false`、不带 `stream_options`）、`internal/agent/fallback.go:18/98`（`ChatOnce` 接口与恢复分支）、`internal/app/app_execute.go` 的 `streamRecoveryEnabled` | 提供方流中途报 `llm.ErrStreamError` 且已输出部分文本时，用同一 model/messages/tools 非流式补一次，只发布精确后缀（不重复前缀）、每轮最多一次，发 `EventRetry stream_recovery`；opt-in `ARGENT_STREAM_RECOVERY=1`（R43 完成端到端接线），默认零值逐字节不变。 |

#### 2.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 会话续跑 | `--continue` / `--resume` | 均有 resume | `--resume` / `--session` / `--continue`（TECHNICAL.md:470） | ✅ |
| Checkpoint 回退 | Esc Esc 回退 + fork | 部分产品有类似 rewind | 文件级 checkpoint + undo/retry，且带 sha256 防串改 | ✅ 领先 |
| 分支树导航 | 有 | 部分有 | 有 `sessions tree` 与 fork；CLI 侧成熟，GUI 侧也已可点击导航（R24 `session.tree` RPC + R26 分支渲染） | ✅ |
| 会话内容搜索 | 有 | 视产品而定 | `sessions --grep` + `find`，片段脱敏 | ✅ |
| 离线分享 | 视产品/企业策略 | 不统一 | HTML/JSON/Markdown 离线导出，无网络引用 | ✅ |
| ACP 会话恢复 | Zed/ACP 生态通常支持 session/load | ACP 客户端多自带会话状态 | **明确不支持** `session/load`（见 ⚠️-2） | ⊘/⚠️ |

#### 2.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | ~~**GUI 侧缺少分支树的可视导航。**~~ **R24+R26 已完成**：会话树、fork 在 CLI/JSONL 层完备，R24 在 `--rpc` 暴露只读 `session.tree`（`internal/server/rpc.go:348`，复用 `internal/session/tree.go:68` 的 `Branches`），R26 原生窗口解析并在会话行下渲染可点击分支节点（`native/main.go:407-491`：`branchNodes` / `hasForkBranches` / `fetchSessionTree`）；单线或无分叉树逐字节不变。 | `internal/server/rpc.go:348-383`；`native/main.go:407-491` | 已闭环。仅 ACP `session/load` 仍不做（见 ⚠️-2），能力经 `--rpc session.load` 保留。 |
| 2 | **ACP 不支持 `session/load`。** 直接返回 method-not-found，因此 ACP 客户端无法续跑历史会话。 | `internal/server/acp.go:131-132`（`case "session/load"`） | 视目标 ACP 客户端需求决定是否接入；若不做，保留 ⊘ 理由：ACP 客户端通常自带会话状态，续跑走 CLI `--resume` / `--rpc session.load`。 |

#### 2.4 ⊘ 合理取舍

- **ACP `session/load` 暂不做**：见上表；`--rpc` 已提供 `session.load`（`internal/server/rpc.go` 方法分发），能力未丢失，只是不在 ACP 协议面上。

---

### 3. 工具执行

#### 3.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 默认工具集 | `internal/tools/builtin_register.go:30-71`（30 个静态注册）+ `internal/tools/tools_registry.go:120-127`（`context_pack`、`memory_reflect`/`memory_learn`、`tool_search`） | 默认共 34 个，与 STATUS.md「工具广度」一致。 |
| 可选工具按开关挂载 | `internal/app/mcp.go:69-78`（`wireOptInTools`：apply_patch / grep_ex）、`internal/app/checkpoint_source.go:52`（`checkpoint_diff`） | 不污染默认 spec 顺序。 |
| 工作区边界 | `internal/security/path_real.go:32`（`CheckPathInRoots`）、`internal/tools/tools_registry.go:357`（`resolve` 拒绝 `..`/绝对路径逃逸） | 每个 FS 工具都解析到工作区。 |
| 安全链 | `internal/security/chain.go:139`（`NewChain`）、`:148`（`Check`）、`:163`（`check`） | 策略 → 审批 → 审计，顺序固定。 |
| 三种沙箱模式 | `internal/security/sandbox.go:14-18`（`read-only` / `workspace-write` / `danger-full-access`）；过滤见 `internal/tools/catalog.go:84` | read-only 移除全部 `Mutating()` 工具。 |
| 审批模式 | `--approval ask\|never\|auto`、`--yolo`（`internal/cli/flags_register.go:17/27`）；实现见 `internal/security/approver.go:49/104/154/258` | 含作用域授予、TTL、缓存。 |
| 分阶段审批（R37） | `internal/security/chain.go:148-241`（`StagedApprover` / `checkStaged`）、`internal/security/approval_stage.go` | 可选的 `once` / `always` / `reject` / `esc` 状态机审批；仅当审批器实现 `StagedApprover` 且显式 opt-in 时接管决策，失败即关闭。默认关闭、字节不变。 |
| 脱敏与审计 | `internal/security/redact.go:14`（`Redact`）、`internal/security/audit.go` | 写审计前脱敏。 |
| 后台 jobs 注册表（R36） | `internal/app/app_jobs_wire.go`（`ARGENT_JOBS` / `ARGENT_OPT_INS=jobs`）、`internal/jobs/` | 后台作业注册表接入生产路径（创建、持久化、`StopAll`），opt-in 开启；默认不启用，路径逐字节不变。 |
| 长命令转后台 job | `internal/bash/tools_builtin_bash.go:74/81`（`background_after_ms`）、`internal/jobs/` | 超时后不杀进程，返回 job id。 |
| 工具输出头尾折叠 / todo 校验 / 文件新鲜度（R37） | `internal/tools/output_collapse.go`、`internal/tools/todo_validate.go`、`internal/tools/file_freshness.go` | `CollapseOutput` 接入 bash 裁剪、`ValidateTodoState` 接入 todo 写入、`FileFreshness` 原语；均为 opt-in、默认字节不变。 |
| MCP | `internal/mcp/`；per-server `enabled` / `readOnly` / `allowedTools` / `deniedTools`（`internal/mcp/permissions.go`） | stdio + HTTP/SSE，项目级 `mcp.json` 受 `--trust-project` 门控（`internal/app/mcp.go:272/575`）。R35 修复 `read-only` 模式下修改类工具可被绕过的问题（`internal/security/mutating.go`、`internal/app/mcp_readonly_test.go`）。 |
| Hooks | 11 事件（`internal/hooks/event_handlers.go:91-103`）、SHA-256 信任、exit-2 阻断 | 工具调用前后可拦截。 |
| Hook additionalContext 注入（R41） | `internal/hooks/verdict.go:161`（`AdditionalContextFromOutcome`）、`internal/hooks/dispatch.go:581`（`Summarize`）、`internal/app/hooks.go:257`、`internal/agent/loop.go:613`（`HookContextQueue`）、`internal/app/app_execute.go:333` | hook stdout 的顶层/`hookSpecificOutput.additionalContext` 经脱敏 + 8192 字节上限提取，聚合进 `Result.AdditionalContexts`，在下一轮注入模型上下文；在 `bridge.start` 之前接线，使 SessionStart/UserPromptSubmit 的上下文不丢失。 |
| MCP 工具输出注入扫描（R41） | `internal/mcp/mcp.go:404`（`mcpInjectionScanEnv` / `warnOnPromptInjection`）、`internal/security` 的 `ScanPromptInjection` | 对 `CallTool` 最终文本做注入检测，命中高风险时前置一行可读警示；只警示、不改写、不过滤原文任何字节，opt-in `ARGENT_MCP_INJECTION_SCAN=1`，默认逐字节不变。 |
| LSP 工具 | 六个 `lsp_*`，惰性启动（`internal/app/lsp_adapter.go`、`internal/agent` 不 import `lsp`） | 预览类工具只读，不落盘。 |

#### 3.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 内置工具广度 | 丰富，文件/shell/web/task 齐全 | 各有侧重；OpenCode LSP 强 | 34 默认 + 可选，覆盖主流编码场景 | ✅ |
| 工作区边界 | 有权限提示 | 多数有 | 路径逃逸硬拒绝 + 三沙箱模式，证据强 | ✅ |
| 审批粒度 | 每次/会话/规则 | 视产品而定 | scoped 授予 + TTL + 规则链，细于多数 | ✅ 领先 |
| MCP | 业界标准支持 | 广泛支持 | stdio/HTTP + 权限字段，齐全 | ✅ |
| Hooks | 有，事件丰富 | 部分有 | 11 事件 + 信任闸门 + exit-2 阻断 | ✅ |
| Web 搜索 | 内建 WebSearch/WebFetch | 多数内建 | **依赖外部 `ARGENT_WEB_SEARCH_URL`**，未配置时只能用 `web_fetch` | ⚠️-1 |
| 浏览器 / computer-use | 部分产品有 | 视产品而定 | 无，且为有意不做 | ⊘-2 |
| DAP 调试 | 无/插件 | 少数有 | 无，且为有意不做 | ⊘-3 |

#### 3.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **`web_search` 没有内置后端。** 未配置端点时报错并建议 `web_fetch`。**R20 已补可发现性**：`doctor` 增加 web_search 配置检查（warn 提示 `ARGENT_WEB_SEARCH_URL`/`BRAVE_API_KEY`/`TAVILY_API_KEY`）；`internal/tools/web_search_presets.go` 提供 Brave/Tavily/SearXNG 预设元信息。诚实边界：**无用户 key / 自托管服务就没有联网搜索**，不用 HTML 抓取冒充内置搜索。 | `internal/tools/web_search.go:17/26/53`；`internal/cli/doctor.go`（`checkWebSearch`）；`internal/tools/web_search_presets.go` | 可选：把预设接入 `Run` 的请求头注入（需 key）；保持不联网测试可用。 |

#### 3.4 ⊘ 合理取舍

- **⊘-2 不做浏览器 / computer-use 工具**：Rust 参考版对应工具未移植（TECHNICAL.md:697 `--enable-browser` → parse-then-error），引入无头浏览器会破坏"核心零依赖、无 cgo、无托管服务"的底线。
- **⊘-3 不做 DAP 适配器**：明确不在范围（TECHNICAL.md:393）；成本高、与 LSP 的文本交互模型不同，收益低于把 IDE 级 LSP 方法补齐。

---

### 4. 交互体验

#### 4.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 全屏 TUI | `internal/tui/`（自绘 ANSI，无 WebView，可跑 SSH）；面板 Ctrl+T/B/P（TECHNICAL.md:56） | tasks / workspace / plan 三面板。 |
| 原生桌面窗口 | `native/`（MyGo），两个二进制，`--rpc` 驱动（`native/bridge.go:55`、`:112`） | 唯一 GUI；`prompt/delta` 流式渲染。 |
| 桌面交互 | undo / retry / 主题 / 审批 Y/N（`native/main.go:202-208`、`:503-508`、`:1020`、`:1041`） | 会话栏 + 消息列表 + 工具卡片。 |
| 三种无头传输 | `--serve`（SSE）、`--rpc`、`acp`（TECHNICAL.md:42-51） | 事件语义需在三者间保持一致。 |
| 审批可视化 | TUI 审批弹窗 + 危险命令高亮（`internal/tui/approval*.go`） | 与 `security.Chain` 同源。 |
| 国际化 | `zh-CN` / `en-US`（`internal/i18n/i18n.go:19-20`），目录为二进制内 Go 表；含翻译完整性审计（`:363`） | 缺键返回键并记录。 |
| 流式事件 | `text_delta` / `reasoning_delta` / `tool_start` / `tool_end`（`internal/agent/events.go:19` 起） | 传输层转发同一份流。 |
| 命令面板模糊补全（R25） | `internal/tui/completion.go:178-195`（`matchCommand`：保留大小写不敏感前缀语义，非前缀走 `FuzzyMatch` 子序列，如 "rv" 命中 "review"） | 前缀路径逐字节不变；命中不重排，仍按主表字典序。 |
| 编辑后自动验证（R25） | `internal/app/verify_wire.go:591-595`（`verifyFuncFor`，`verifyFuncForAuto`）；opt-in 键 `auto_verify`（`internal/app/optin_registry.go:55`） | 未开启或传 false 时逐字节不变；开启且工作区含 `go.mod`、无显式配置时合成 `go test ./...` 回环，诊断回灌给模型。 |
| 状态栏配置查询（R24） | `config.info` RPC（`internal/server/rpc.go:509-526`） | 返回当前会话真实 model/provider/workspace；路径经与 `session.load` 相同的脱敏边界。 |
| 每轮统计行（R33） | `internal/tui/model_mutate.go:391`（`finalizeTurnStatsLocked`）、`internal/tui/events.go`、`internal/tui/turn_stats_test.go` | 轮末追加一行 `模型 · 时长 · 平均 tok/s`；仅有 TurnStart 打点、时钟前进、输出 token>0、模型名非空时才生成，缺数据不显示。默认开启。 |
| 用量行增强（R33） | `internal/tui/render.go:295-326` | 用量/成本行增加速度（⚡）、缓存命中率、上下文目录名；整体受 `usageVisible` 门控（`ARGENT_TUI_USAGE=1`，`internal/app/tui_event_wire.go`），默认关闭、帧字节不变。 |
| 推理块展开/折叠（R33+R37） | `internal/tui/model.go:389/428/430`（`toggleReasoningAt` / `toggleReasoningAllLocked`）、`internal/tui/keys.go:69`（`KindCtrlX`）、`internal/tui/reasoning_toggle_test.go` | 鼠标点击推理块按渲染行命中测试切换单块展开态（CJK 安全、逐块覆盖）；`Ctrl+X` 全局切换全部思考块，可配暗色占位符。 |
| `/` 补全浮层（R33） | `internal/tui/commands_overlay.go`（`paintCompletionOverlay`）、`internal/tui/model.go:303`（`completionOverlay`）、`internal/tui/completion_overlay_test.go` | 输入 `/` 自动弹出候选浮层，`>` 高亮，`(N/M)` 计数，上下键 + 鼠标点击选择；保留键盘 ShortHelp。 |
| 路径 / `@mention` 补全（R37） | `internal/tui/completion.go:247/299`（`activeInlineWord` / `quoteIfNeeded`）、`internal/tui/model_input.go:315-385`、`internal/tui/path_completion_test.go` | 行内路径与 `@` 提及补全：引号感知、fuzzy 匹配、仅标准库；含空白/引号/括号的路径写入时自动加引号。 |
| 终端标题 / 剪贴板（R33） | `internal/tui/terminal_title_test.go`、`internal/tui/clipboard_windows.go` | 终端标题设为 argentgo；移除底部快捷键提示条；修复 Windows 剪贴板 GBK 乱码（Win32 `CF_UNICODETEXT`，syscall、无 cgo，可交叉编译 linux）。 |
| 计划审批循环 / `exit_plan`（R42） | `internal/app/app_execute.go:195-214`（`SetPlanUnlock` / `wireExitPlan`）、`internal/security/chain.go` | 只规划模式（`ARGENT_PLAN_MODE=1`）下模型通过 `exit_plan` 工具请求批准计划；审批经既有 Chain，未批准前修改类 bash 被计划守卫拦下。仅在计划模式注册该工具，默认工具目录逐字节不变。 |

#### 4.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 终端体验 | 强（含 IDE 扩展） | OpenCode/Crush TUI 都很强 | 自绘 ANSI，SSH 可用，面板齐全 | ✅ |
| 桌面 GUI | 有 | 部分有 Web/桌面端 | 原生 MyGo，低内存（约 76MB vs WebView ~400MB，TECHNICAL.md:304-305） | ✅ 领先（内存/无 WebView） |
| 流式与审批 | 成熟 | 成熟 | 端到端一致，原生窗口可 Y/N | ✅ |
| 语言覆盖 | 多语言 | 多为英文 | 仅 zh-CN / en-US | ⚠️-1 |
| 桌面多平台 | 跨平台 | 多平台 | **仅 Windows/amd64 发布**（STATUS.md:48-51） | ⚠️-2 |
| 真实模型质量评测 | 有内部评测体系 | 视产品而定 | `evals/core.json` 11 个脚本场景；已有 opt-in 真实模型评测批处理（`ARGENT_EVAL_LIVE` / `eval --live` + `completion.json`/`completion_expanded.json`），默认离线不联网 | ✅（opt-in） |
| IDE 级 LSP 效果 | 有 | OpenCode 内建 LSP 强 | 6 基础 + 3 进阶方法已实现；R24 已补真实 gopls 结构化验证（`gopls_advanced_test.go`，best-effort，未做硬门禁） | ✅（best-effort） |

#### 4.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **只有两种语言环境。** UI 字符串与提示语言仅 zh-CN / en-US。 | `internal/i18n/i18n.go:19-20`（`EnUS` / `ZhCN`）、`:24`（`DefaultLocale`） | 若目标用户含日语等，先扩目录 + 用现有 `AuditTranslations`（`:363`）保证完整性；否则保留为取舍。 |
| 2 | **桌面 GUI 只有 Windows 构建。** 发行脚本与 CI 收口 Windows/amd64。 | `scripts/build-release.ps1`、`STATUS.md:48-51` | 需要 Linux GUI 时先解决 MyGo 多平台打包与本地验证，不改核心。 |
| 3 | ~~**无真实模型质量评测。**~~ **R23+R24+R25 已落地 opt-in 真实模型评测批处理，默认离线不联网。** R23：`internal/eval/completion.go` 的 `ScenarioCompletionRate` + `internal/eval/run.go:410-422` 的 `Check.FileEquals` 逐字节判据（`internal/eval/file_equals_test.go`），由 `go test` 退出码 + 文件 diff 客观判完成率，天然 model-agnostic；R24：`ARGENT_EVAL_LIVE` 精确等于 "1" 才开启（`internal/eval/live_gate.go:15/39/51`，需 API key，默认零配置断网）+ `eval --live` 带外分派（`internal/cli/eval.go:27-50`）+ `evals/completion.json`（F/N 2 场景，完成率 0.5）；R25：`evals/completion_expanded.json`（5 场景 4 正 1 负，完成率细化到 0.8，`liveSafeScenarios` 在 live 模式归零脚本字段只留客观 Check）。 | `internal/eval/completion.go`、`internal/eval/run.go:410-422`、`internal/eval/live_gate.go`、`internal/cli/eval.go:27-50`、`evals/completion.json`、`evals/completion_expanded.json` | 已闭环。诚实边界：opt-in 需要用户自己的 key / 协议配置，默认 `eval` 路径仍完全离线；判据只验"文件是否真的改对 + 测试是否转绿"，不打分回答文风。 |
| 4 | ~~**进阶 LSP 效果未经真实 server 验证。**~~ **R24 已补真实 server 结构化验证（best-effort，未做硬门禁）。** `internal/lsp/gopls_advanced_test.go:14-164` 复用真实 gopls 的 workspace/fixture/就绪门，对 `SemanticTokens` / `InlayHints` / `CodeLens` / `ResolveCodeLens` 四方法逐一调用；数量类结果与 RPC 错误只记日志（gopls 默认关闭 semantic tokens / inlay hints、codeLens/resolve 可能因不透明 data 报错，均为合法结果），仅对"已成功返回的结构"做不变量断言（token 数为 5 的倍数、Position.Line ≥ 0、resolve 须标记 resolved）。 | `internal/lsp/gopls_advanced_test.go:14-164`、`internal/lsp/features_advanced.go:11/31/52/75` | 硬门禁（数量下限/具体值断言）与更多语言 server（如 rust-analyzer/pyright）验证仍未做；当前为"实现存在 → 结构已验证"，效果强断言留待后续。 |

#### 4.4 ⊘ 合理取舍

- **不做浏览器前端**：Web 前端已完全移除（TECHNICAL.md:275-287）。理由：WebView2 常驻约 400 MB、需 HTTP+SSE 桥和 SolidJS 工具链；原生窗口单进程约 76 MB、复用既有 `--rpc`，核心保持零依赖。

---

### 5. Provider 协议

#### 5.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 三套线协议 | `internal/llm/options.go:16-17`（`ProtocolAnthropicMessages` / `ProtocolResponses`）、`internal/provider/templates.go:18-22`（`anthropic-messages` / `openai-chat-completions` / `openai-responses`） | 协议细节只在 `internal/llm`。 |
| 20 个内置 provider 模板 | `internal/provider/templates.go:61-202`（`builtInTemplates`） | Anthropic / OpenAI / DeepSeek / Moonshot / 智谱 / Qwen / OpenRouter / Ollama / LM Studio / xAI / Gemini / Groq / Mistral 等。 |
| 重试与错误分类 | `internal/llm/failure_contract.go:53`（`IsRetryableError`）、`:89`（`ClassifyRetry`）、`internal/llm/attempt.go` | 流完整性、重试原因、可恢复错误统一。 |
| Fallback 模型 | `internal/app/app_chatter.go`、`internal/cli/flags_register.go`（`fallback_model`） | Chatter 层注入。 |
| 提示缓存 | `internal/llm/options.go:95/109` | 见 §1。 |
| 推理协议映射 | `internal/llm/anthropic.go:226`、`internal/llm/options.go:71-79` | `reasoning_content` / `thinking` 块与保留策略。 |
| 原生推理参数（R36） | 见 §1.1；`internal/llm/thinking_wire_test.go`、`internal/llm/reasoning_signature_test.go` | 把 `--thinking` 下沉为线上原生字段；签名闭环见 §1.1。 |
| baseURL 来源追踪与 fail-closed（R35） | `internal/config/source.go`、`internal/app/app_resolve.go:119-183`、`internal/cli/flags_register.go:45`（`--trust-project`） | 不可信项目配置改写 baseURL 且请求携带 env/flag 凭据时拒绝发送（除非 `--trust-project`）；doctor 对不安全 baseURL 告警；校验和按资产名核对。 |
| 凭据加密（R35） | `internal/auth/token_crypt_windows.go`、`internal/auth/store.go`、`internal/auth/store_crypt_test.go` | Windows 用 DPAPI（`crypt32.dll` syscall，零依赖无 cgo）按当前用户加密令牌；旧明文读入后自动升级，失败优雅降级。 |
| 认证 | `internal/auth/`（PKCE / 设备码）、`argentgo login/logout/auth` | 密钥不回显。 |

#### 5.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 协议深度 | 单一 Anthropic 协议做到极深 | 多 provider 为主 | 三协议 + 20 模板，广度好、单协议深度不及 Claude Code | ✅（广度）/ ⚠️（深度） |
| 重试 / 流完整性 | 有 | 有 | 显式失败契约 + 流完整性校验，工程证据强 | ✅ |
| 缓存 / 推理 | 强 | 视 provider | 有，按协议显式控制 | ✅ |
| 云厂商长尾（Bedrock / Vertex） | 有 | 部分有 | **无**（源码零命中） | ⊘-1 |
| 本地模型 | 不主打 | 部分支持 | Ollama / LM Studio / local 模板（TECHNICAL.md:113-159） | ✅ |

#### 5.3 ⊘ 合理取舍

- **⊘-1 不做 Bedrock / Vertex 长尾**：源码内无 `bedrock` / `vertex` 命中。理由是 AWS/GCP 认证链路（签名、角色、区域、凭证链）复杂、个人自用收益低；且 `openrouter` / 兼容端点模板（`internal/provider/templates.go:107/114`）与 `--base-url` + `--protocol`（`internal/cli/flags_register.go`）已覆盖绝大多数接入需求。若未来确有需要，优先走兼容网关而非内建签名逻辑。

---

### 6. 开发者体验

#### 6.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 七层配置优先级 | `internal/config/`、`TECHNICAL.md:618-626` | 默认值 → home → 项目 → `argent.json` → profile → `ARGENT_*` → CLI。 |
| JSONC + 引用 | `internal/config/jsonc.go`、`internal/config/config.go` | 注释/尾逗号、`{env:VAR}` / `{file:path}`。 |
| Profile / env 覆盖 | `internal/config/`、`TECHNICAL.md:630-632` | `ARGENT_PROFILE` + `ARGENT_MODEL` 等。 |
| 插件（清单发现） | `internal/plugins/`、`--plugins <name>`（TECHNICAL.md:454） | 贡献 skills / commands / MCP；marketplace 不迁。 |
| Skills | `internal/skills/`（`SKILL.md` 发现 + 渐进披露）、`use_skill` | 含自写能力（`internal/skills/author.go`）。 |
| 斜杠命令 | `internal/commands/`、markdown 文件发现 | `argentgo commands` 列出。 |
| Hooks | `internal/hooks/` + SHA-256 信任（`internal/hooks/trust.go`） | 已配置规则进入安全链。 |
| LSP | `internal/lsp/`（6 基础 + 3 进阶方法）、gopls 集成测试 | 惰性启动、按 root+server 缓存。 |
| Eval | `internal/eval/`、`evals/core.json`（11 场景）、`argentgo eval` | 脚本化、不联网；R24/R25 另加 opt-in 真实模型批处理（`--live`），默认不联网。R35 新增 `eval compare`（配对统计，n<30 记"样本不足"防刷分，`internal/eval/compare.go`）与评测框架灵敏度自测（注入故障断言完成率下降，`internal/app/eval_completion_bench_sensitivity_test.go`）。R39 新增 `eval ab`（`internal/eval/variant_ab.go` 的 `RunVariantAB` + `internal/cli/eval.go:39` 带外分派）：按场景指纹配对两臂再跑配对统计，token 经济诊断对照完成率增益与 token 成本。 |
| Workflow / 自动化 / jobs | `internal/workflow/`、`internal/automation/`、`internal/jobs/` | DAG、cron、后台 job；R36 起 jobs 注册表可 opt-in 接入生产路径。 |
| 诊断 CLI | `doctor` / `bug` / `audit` / `usage` / `policy` / `config`（TECHNICAL.md:421-452） | 机器可读 `--json`。 |

#### 6.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 配置灵活性 | settings.json + 环境变量 | 各配置文件 | 七层 + JSONC + profile + 引用，强 | ✅ 领先 |
| 项目级说明文件 | CLAUDE.md | AGENTS.md 等 | AGENTS.md / CLAUDE.md 注入（`internal/memory/`）+ skills | ✅ |
| 插件生态 | marketplace | 部分有插件系统 | 清单发现 + opt-in，`--plugins`；无 marketplace | ⚠️-1 / ⊘-1 |
| 调试协议 | 有 IDE 集成 | OpenCode LSP 强 | LSP 完善，DAP 不做 | ⊘-2 |
| 可观测性 | 日志/审计 | 视产品 | 审计、脱敏、doctor、bug 包、jobs 面板，工程证据强 | ✅ |
| 脚本化评测 | 有内部体系 | 不统一 | `eval` + core.json 11 场景，可复现；另有 opt-in `--live` 真实模型批处理（默认离线） | ✅ |

#### 6.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **没有 marketplace / 一键插件安装体验。** 插件靠清单发现 + `--plugins` 按次启用，缺少搜索/安装/更新的用户路径。 | `internal/plugins/`（`discovery.go` / `install.go` / `merge.go`）、`--plugins`（TECHNICAL.md:454） | 若要做，先补 `plugin search/install` 的本地清单操作；注意这是定位取舍，不与 Rust 版 marketplace 对齐（STATUS.md:36）。 |

#### 6.4 ⊘ 合理取舍

- **⊘-1 不做 marketplace**：Rust 版 marketplace 明确不迁（TECHNICAL.md:742）；个人项目无分发运营收益。
- **⊘-2 不做 C-ABI 插件加载器 / Rhai 脚本插件**：Go 无对应生态，引入即破坏零依赖（TECHNICAL.md:752/754）。
- **不做 Node/Cordis 插件图、SQLite/FTS5、Honcho 用户建模、聊天机器人、远程沙箱、JS code-mode 虚拟机、托管目录**：均为重量级依赖/服务或定位取舍（STATUS.md:36）。
- **不做 telemetry 运营**：个人项目无收益，仅保留空操作子命令（STATUS.md:36、TECHNICAL.md:756）。

---

### 7. 不做清单（汇总，含理由）

| 不做项 | 理由 | 替代方案 | 证据 |
|---|---|---|---|
| tree-sitter AST / 语法裁剪 | cgo 破坏 `CGO_ENABLED=0` 静态交叉编译 | LSP 符号 + `repo_map` | STATUS.md:59、TECHNICAL.md:753 |
| 浏览器前端 / WebView / SolidJS | ~400MB 常驻、需新工具链与桥接 | MyGo 原生窗口（~76MB）+ `--rpc` | TECHNICAL.md:275-287、:302-308 |
| DAP 适配器 | 成本高、与文本交互模型不同、收益低 | 把 IDE 级 LSP 方法补齐 | TECHNICAL.md:393、STATUS.md:59 |
| Bedrock / Vertex 长尾 | 云厂商签名/认证复杂、个人自用收益低 | 兼容网关模板 + `--base-url` / `--protocol` | 源码零命中；`internal/provider/templates.go:61-202` |
| C-ABI 插件 / Rhai 脚本 / Node 插件图 | Go 无生态、破坏零依赖 | 清单发现 + opt-in 工具 | TECHNICAL.md:752/754、STATUS.md:36 |
| SQLite/FTS5、Honcho 建模、聊天机器人 | 重量级依赖/服务、定位取舍 | 现有 JSONL 搜索 + 记忆库 | STATUS.md:36 |
| 远程沙箱 / OS 沙箱原语 / JS code-mode VM | 引入标准库外依赖或服务 | 三沙箱模式 + 审计链 | STATUS.md:36、TECHNICAL.md:393 |
| marketplace / telemetry 运营 | 个人项目无运营收益 | 本地插件清单 + 空操作子命令 | STATUS.md:36、TECHNICAL.md:742/756 |
| document/office 预览、device 桌面控制、TTS | 只服务桌面端或无用户面 | `web_fetch` + 原生控件 | TECHNICAL.md:674-675/755、STATUS.md:84 |

---

### 8. 一图看懂（六维总览）

| 维度 | 总标注 | 一句话 |
|---|---|---|
| 上下文管理 | ✅ + ⚠️ | 预算/裁剪/去重/steering/结构化压缩证据齐全；R20 已修 grapheme 切点，摘要生成式 R22 已接线（opt-in `model_summary` + `secondary-model`，3s 超时失败回落，开销在事件流可见但不计预算/stats），默认仍走抽取式。R35 压缩跨链保留 Goal/Failures/NextSteps，R36 原生推理参数 + Anthropic 签名闭环已落地。 |
| 可恢复性 | ✅ 领先 | JSONL 树 + sha256 checkpoint + undo/retry + 离线导出；R35/R36 新增会话 rewind（`sessions checkout` / `--session --at`，就地切换活动叶续写），R37 ForkBefore 提示恢复；GUI 分支树已补（R24 `session.tree` RPC + R26 原生可点击渲染），仅 ACP load 明确不做（能力经 `--rpc session.load` 保留）。 |
| 工具执行 | ✅ 领先 | 34 默认工具 + 硬边界 + 细粒度审批 + MCP/Hooks；R35 修 read-only MCP 绕过、R37 加分阶段审批与输出折叠/todo 校验（均 opt-in）；`web_search` 无内置后端是短板。 |
| 交互体验 | ✅ + ⚠️ | TUI/原生 GUI/三传输齐全且内存占优；R33 每轮统计行 + 推理块点击展开 + `/` 补全浮层，R37 `Ctrl+X` 全局思考切换 + 路径/`@mention` 补全，token/s 精确算法（排除 TTFT、Σtokens/Σdurations）已落地；语言与桌面平台仍是差距，真实模型评测已 opt-in 落地（默认离线）。 |
| provider 协议 | ✅ + ⊘ | 三协议 20 模板 + 重试/缓存/推理；R35 baseURL 来源追踪 fail-closed + Windows DPAPI 凭据加密；云厂商长尾有意不做。 |
| 开发者体验 | ✅ 领先 | 七层配置 + skills/commands/hooks/LSP/eval + `doctor --json` / `config.info` RPC / `auto_verify` opt-in；R35 加 `eval compare`（n<30 样本不足守卫）与评测灵敏度自测；无 marketplace 是定位取舍。 |

---

### 9. 迭代建议（按投入产出排序）

1. ~~先修正确性：§1 ⚠️-2 的 grapheme 切点~~ **R20 已完成**：session 主路径统一字簇吸附，STATUS 核心引擎升至 10 分。
2. **再补开箱即用**：§3 ⚠️-1 的 `web_search`（R20 已加 doctor 提示 + 预设，仍需用户 key）；§1 ⚠️-3 指**提示缓存对 OpenAI 兼容端点默认关闭**（而非"缓存可见性"——累计统计 cached 恒 0 的口径问题已在 R23 由 `loop_helpers.go:account()` 修复），仍需按 `baseURL` 探测/显式声明。开发者体验侧的 `doctor --json`、`config.info`、`auto_verify` 已于 R24/R25 补齐。
3. ~~然后补可信度：§4 ⚠️-3/⚠️-4~~ **R23+R24+R25 已完成**：真实模型评测为 opt-in 批处理（`ARGENT_EVAL_LIVE` / `eval --live`，默认离线），进阶 LSP 已补真实 gopls 结构化验证（R24 best-effort，未做硬门禁）。剩余增量是把 best-effort 升级为硬断言、扩更多语言 server。
4. ~~最后考虑体验增量：§2 ⚠️-1 GUI 分支树~~ **R24+R26 已完成**（`session.tree` RPC + 原生可点击分支渲染）；§1 ⚠️-1 模型生成式压缩摘要 **R22 已接线**（opt-in，默认关逐字节不变），剩余是把旁路开销计入预算/stats（改 agent 接口，暂不做）。

所有 ⚠️ 项均保留源码落点；每完成一项，应同步更新本矩阵的标注与其 ⚠️ 表行。

#### R23 更新记录

- **会话续接摘要**（新增能力）：每轮 `Execute` 是独立 Runner、JSONL 从不回灌模型，跨轮 resume/追问会"失忆"（单次运行内历史完整，这不是长任务跑偏根因）。R23 新增 opt-in `ARGENT_CONTINUATION_BRIEF`：`internal/app/continuation_brief.go` 复用 `Compaction.Structured()` 生成有界续接摘要注入 system。明确拒绝"注入全部历史"（会破坏提示缓存前缀 `buildStaticSystem`、放大上下文污染）。
- **完成率验收闭环**（§4 ⚠️-3 部分推进）：`internal/eval` 新增 `Check.FileEquals`（精确字节判据）与 `ScenarioCompletionRate`；`internal/app/eval_completion_test.go` 用 F/N 双场景证明 grader 依据真实 `go test` 退出码 + 文件字节区分完成/未完成（`Passed==1 && Failed==1`、完成率 0.5）。**诚实边界**：脚本化 provider 仅证明框架能客观判完成率，真能力验收需 `ARGENT_EVAL_LIVE` + 真模型跑同一批场景；objective 判据（退出码/文件 diff）天然 model-agnostic。
- **缓存统计根因修复**：`internal/agent/loop_helpers.go` 的 `account()` 补累加 `CachedTokens`/`CacheWriteTokens`，修正累计统计/TotalUsage/`--tokens` 总额/usage 工具里 cached 恒 0 的口径不一致（逐模型行本就正确）。wire 变化为已有 `omitempty` 字段填值，字节 golden 全绿。
- **仍维持 ⊘**：ACP session/load、进阶 LSP 硬验证、i18n 扩语言、Linux GUI、图像输入贯通（原语在但仅 ~1/3 provider 支持，R22/R23 均评估推迟）、marketplace/tree-sitter/DAP/Bedrock-Vertex。

#### R24 更新记录

- **可恢复性 RPC 闭环（§2 ⚠️-1 部分完成）**：新增只读 `session.tree`（`internal/server/rpc.go:348-383`），{id} 入参、`{branches:[...]}` 出参，复用 `internal/session/tree.go:68` 的 `Branches`；通过 `SessionTreeStore` 可选接口发现，未实现的假存储得到干净 "not configured" 错误而非 panic，错误走 `scrubSessionLoadError` 脱敏路径，既有 wire 逐字节不变。同批新增 `config.info`（`internal/server/rpc.go:509-526`），返回真实 model/provider/workspace 供原生状态栏显示，workspace 折叠为 `<storage>/...` 占位符。
- **真实模型评测批处理落地（§4 ⚠️-3 部分完成）**：`internal/eval/live_gate.go:15/39/51` 仅当 `ARGENT_EVAL_LIVE` 精确等于 "1" 才开启，并要求 `ARGENT_API_KEY` / `OPENAI_API_KEY` 之一非空（`ErrLiveRequiresKey`），协议名走白名单（`isLiveProtocol`），Redacted 保证 key 不出报告；默认零配置断网。`internal/cli/eval.go:27-50` 把 `--live` 作为不进 flagSurface/parity 的带外前缀分派给 `LiveEvalSuiteRunner`，不支持时明确报错。`evals/completion.json` 把 R23 只活在测试常量里的 F/N 场景搬成数据（2 场景，完成率 0.5）。判据仍是 `go test` 退出码 + `file_equals` 逐字节（`internal/eval/run.go:410-422`、`internal/eval/scenario.go:125-127`），与模型自评无关。
- **进阶 LSP 真实 server 验证（§4 ⚠️-4 部分完成）**：新增 `internal/lsp/gopls_advanced_test.go:14-164`，复用 gopls 集成测试的 workspace/就绪门，对 `SemanticTokens` / `InlayHints` / `CodeLens` / `ResolveCodeLens` 做 best-effort 结构验证：数量与 RPC 错误只记日志，仅对成功返回的结构做不变量断言（token 数 5 的倍数、Position.Line ≥ 0、resolve 须标记 resolved）。**未做硬门禁**：gopls 默认关闭 semantic tokens / inlay hints，codeLens/resolve 可能因不透明 data 报错，都是合法结果。
- **开发者体验**：`doctor --json`（`internal/cli/doctor.go:66/101/344`）——检查计算与文本分支完全共享，输出稳定 JSON 报告（checks/failures/environment）。
- **仍维持 ⊘**：ACP `session/load`（`internal/server/acp.go:131-132` 直接 method-not-found，能力经 `--rpc session.load` 保留）、进阶 LSP 硬断言、i18n 扩语言、Linux GUI、marketplace/tree-sitter/DAP/Bedrock-Vertex。

#### R25 更新记录

- **完成率粒度扩充（§4 ⚠️-3 继续推进）**：新增 `evals/completion_expanded.json`，5 场景（4 正 1 负：S1 多文件重构、S2 双 bug 调试转绿、S3 needle 检索、S4 read→grep→edit→write 编排、S5 部分修复负对照），完成率从 0.5 细化到 0.8。每个 Go 场景用 `allow_exec` + `exec(go test ./... want_exit_code 0)` + `file_equals`，逐字节锁死 `*_test.go` 种子内容防改测试作弊；`liveSafeScenarios` 在 live 跑批时归零脚本字段，只保留客观 Check。配套 fixture 测试 `internal/eval/completion_expanded_fixture_test.go` 校验种子与判据一致。
- **编辑后自动验证 opt-in**：`internal/app/verify_wire.go:591-595` 的 `verifyFuncFor` 接受可选 `auto` 参数（`internal/app/optin_registry.go:55` 的 `auto_verify`）；开启且工作区含 `go.mod`、无显式 `VerifyCommand(s)` 时合成 `go test ./...` 回环（`autoVerifyArgv`），诊断经 `internal/app/hooks.go:293` 的 `autoDiagnostics` 回灌给模型；未开启/传 false 时逐字节不变。doctor 的检查与 CLI 可见性同步补齐（`internal/cli/doctor.go:235-286`）。
- **命令面板模糊补全**：`internal/tui/completion.go:178-195` 的 `matchCommand` 保留原大小写不敏感前缀语义（`prefixMatch`），非前缀走 `FuzzyMatch` 子序列（如 "rv" 命中 "review"）；query 先按 rune 折叠，rune 索引与字节位置无关故 CJK/emoji 不被截断；只看命中不读分数，前缀路径逐字节一致、不按分数重排。
- **仍维持 ⊘**：ACP session/load、进阶 LSP 硬门禁、i18n 扩语言、Linux GUI、marketplace/tree-sitter/DAP/Bedrock-Vertex。

#### R26 更新记录

- **原生桌面分支树渲染（§2 ⚠️-1 闭环）**：`native/main.go:407-491` 新增 `branchNodes` / `hasForkBranches` / `branchNodeLabel` / `parseSessionTree` / `fetchSessionTree`。会话行下按需渲染分支节点：仅当 `session.tree` 的 branches 中出现 `Fork=true`（父节点有多个孩子）时才构建元素，单线树或空树下一帧与旧版逐字节一致；节点按深度缩进，分叉/活动叶子分别带 `⑂` / `●` 字形，点击调用 `openSessionBranch` 以 `{"id":stem,"branch":recordID}` 重新加载该分支转录。`fetchSessionTree` 在 RPC 失败或解析失败时保留旧 branches/branchSession，不清空会话栏。
- **回填结论**：可恢复性维度已无 ⚠️（§2 ⚠️-1 完成，ACP load 维持 ⊘）；真实模型评测与进阶 LSP 验证已从"差距"改为"已 opt-in / best-effort 落地"。剩余差距收敛到 §1 ⚠️-1/⚠️-3、§3 ⚠️-1、§4 ⚠️-1/⚠️-2 与 §6 ⚠️-1，以及"硬门禁/更多语言 server/计入预算"等增量。

#### R33 更新记录（TUI 透明化与交互）

- **每轮统计行**（§4）：轮末追加 `模型 · 时长 · 平均 tok/s`（`internal/tui/model_mutate.go:391`）。`finalizeTurnStatsLocked` 对缺失 TurnStart 打点、时钟未前进、输出 token≤0、模型名为空四类情况均不产生记录，故无 NaN/半截行；默认开启。测试 `internal/tui/turn_stats_test.go` 覆盖三字段、多轮多行、缺数据不显示、CJK 模型名按 rune 安全裁剪。
- **推理块点击展开/折叠**：按渲染行命中测试翻转单个思考块（CJK 安全、逐块覆盖，`internal/tui/model.go:428/430`）。
- **Pi 风格 `/` 补全浮层**：输入 `/` 自动弹出，`>` 高亮、`(N/M)` 计数、上下键 + 鼠标选择（`internal/tui/commands_overlay.go`、`internal/tui/completion_overlay_test.go`）；移除底部快捷键提示条，终端标题设为 argentgo。
- **用量行增强（opt-in）**：速度/缓存命中率/上下文目录名，整体受 `ARGENT_TUI_USAGE=1` 门控（`internal/tui/render.go:295-326`），默认帧字节不变。
- **桥接与修复**：`TurnStart`/`TurnEnd` 事件；usage 门控下沉到渲染层；工具进度通知改走 `EventToolProgress`；Windows 剪贴板 GBK 乱码修复（Win32 `CF_UNICODETEXT`，syscall 无 cgo，可交叉编译 linux，`internal/tui/clipboard_windows.go`）。

#### R35 更新记录（安全加固、可靠性、测量）

- **会话 rewind**（§2）：`Checkout` / `OpenSessionAt` + `sessions checkout <id> <entry>` CLI；`--session X --at Y` 恢复（R36 完整接线）。
- **安全**：baseURL 来源追踪 + fail-closed（不可信项目改写端点且带 env/flag 凭据时拒绝，`--trust-project` 放行）；Windows DPAPI 凭据加密（旧明文自动升级）；read-only MCP 修改类工具绕过修复；hook 信任路径规范化、skill 索引长度预算 + 头脱敏、provider 模板校验补全。
- **可靠性**：优雅取消后回收孤儿孙进程（`shouldKillTreeAfterExit`）；压缩跨多轮链保留 Goal/Failures/NextSteps；本地裁剪优先 opt-in 接线；`shell` 的 `chcp &&` 改 `&` 避免 chcp 失败跳过命令。
- **测量（omp 风格）**：评测框架灵敏度自测（注入故障断言完成率下降）；`eval compare` 配对统计 + `minComparePairs=30` "样本不足"守卫防刷分。
- **工程**：修复 `context_windows.go` 被 Go 误判为 Windows-only 导致 linux 交叉编译失败的问题（更名 `context_window.go`）。

#### R36 更新记录（原生推理、结算、学习原语）

- **原生推理参数**（§1.1/§5）——本轮最大的协议差距闭合：`--thinking` 经 `ResolveThinking` 映射为线上原生字段（OpenAI `reasoning_effort`、Responses `reasoning.effort`、Anthropic `budget_tokens`，钳制下限 1024、`xhigh` 档）；FIM 永不携带推理参数；默认 nil 保持请求字节不变。Anthropic 签名闭环：捕获 `signature_delta` 随 `Message`/`Entry`（`omitempty`）保存，多轮仅在带签名时回放 thinking，否则降级纯文本（避免 400）。
- **结算与可靠性**：孤儿 `tool_call` 结算（sha256 短 ID，对会导致 400 的坏数据默认开启，干净会话字节不变）；session rewind 完整接线；后台 jobs 注册表生产接线（opt-in `ARGENT_JOBS`）；轮末压缩（无损归档 + 本地裁剪优先，与轮中路径对齐）。
- **token/s 精确算法**（§4）：排除首 token 等待（TTFT），按 Σtokens/Σdurations 聚合；字段缺失时回退 R33 的相邻增量，无样本不显示（`internal/tui/events.go:150-180`）。
- **仍为默认关闭原语**：budget 投影、审批阶段状态机、todo 状态不变量、确定性循环检测、摘要 schema 填充等，均有测试但默认字节不变。

#### R37 更新记录（激活原语、可观测、习得 UX）

- **TUI**：`Ctrl+X` 全局思考块切换 + 逐块覆盖 + 可选暗色占位符（`internal/tui/reasoning_toggle_test.go`）；路径与 `@mention` 行内补全（引号感知、fuzzy、仅标准库，`internal/tui/path_completion_test.go`）。
- **原语激活（opt-in，默认字节不变）**：`DetectLoop` 接入 agent 循环（软提醒 + 签名去重，不硬停）；`CollapseOutput` 接入 bash 裁剪、`ValidateTodoState` 接入 todo 写入、`EnsureSummarySections` 接入压缩、`Project` 接入 `Messages()`、`ForkBefore` 接入 CLI fork-at/list-messages；`StagedApprover` 接入安全链（once/always/reject/esc，失败即关闭）；token/s 打点经 `llm.Usage`（`json:"-"`）→ agent 值拷贝 → app 桥接 → TUI `Event`（引用计数 opt-in，不触碰推理/签名）。
- **备料 R38（未计入能力）**：`StableSystemPrompt`、`SessionStatusRegistry`、`CriticResult`、`CondensationEvent`、`PruneToolResults`、`FileFreshness`、`TurnTiming` 等原语已实现并测试，但默认关闭、未接入用户路径，按"只写已落地"口径不计入本矩阵能力清单。

#### R38 更新记录（opt-in 治理与分解）

- `argent opt-ins [--json]` 列出 14 个集中键 + 独立门控（单一真源 `optin_metadata.go`），`OPT_INS.md` 双向漂移守卫；`CollapseOutput` 下沉 `internal/textutil` 消除 R37 import 环镜像；`TurnTiming` / `StableSystemPrompt` / `PruneToolResults` / `CondensationEvent` 接入生产（均 opt-in、默认字节不变）。

#### R39 更新记录（测量闭环、agent 质量指标）

- **`eval ab` 变体对比（§6）**：`internal/eval/variant_ab.go` 的 `RunVariantAB` 按场景指纹把两臂结果配对，复用 `CompareResults` 出中文 verdict；`internal/cli/eval.go:39` 把 `ab` 作为带外前缀分派（与 `compare` / `--live` 同风格，不进 flagSurface/parity）。配套可接受基线回归账本（fingerprint/score/hash 三态判定）与 token 经济配对诊断（完成率增益对照 token 成本，可在 CI 运行）；n<30 继续记"样本不足"，不给出晋级结论。
- **agent 质量指标（opt-in、默认字节不变）**：恢复成效账本（`ARGENT_RECOVERY_METRICS`，attempt→success 且受 pending 门控）、跨情节重复踩坑签名计数（`ARGENT_REPEAT_PITFALL_TRACKING`，`Reset` 后仍存活、有上限）、错误签名归一化、上下文健康评分与观察适配器、回退成功统计；子代理取消时的成本对账修复了被取消子任务统计缺失的问题。
- **原语激活**：规则发现接入 system 提示（`ARGENT_RULE_DISCOVERY`，只列名不加载全文）、checkpoint 守卫接入 checkout（`ARGENT_CHECKPOINT_GUARD`，路径级只读校验），子代理成本对账默认开启（仅统计、不改行为）。

#### R40 更新记录（结构修复、flake 根除、可观测原语）

- **结构**：成本对账算术下沉 `internal/calcutil` 单一真源（session 薄转发、agent 去本地副本）；archguard + linux 交叉编译绿。根除两个长期 flaky 测试而不弱化断言（更新时间戳归一 + RPC 帧按 id 排序），变异反证确认内容回归仍会失败。
- **可观测 / 安全原语（均纯标准库、默认字节不变）**：工具耗时直方图（nearest-rank p50/p95）、会话树 ASCII 导出、磁盘用量统计、上下文快照 diff、配置变更 diff（脱敏）、评测覆盖诊断；token 预算预测（饱和算术）；提示注入扫描、shell 命令风险分级、JSONL 校验器、skill 描述注入过滤；LLM 错误分类（`ClassifyErrorClass`）与工具结果 diff 摘要。

#### R41 更新记录（hook additionalContext、安全能力激活）

- **Hook additionalContext 端到端（§3）**：hook stdout 的 `additionalContext` → `Outcome` → `Summarize` → `Result.AdditionalContexts` → `hookBridge` → `HookContextQueue` → 下一轮上下文；在 `bridge.start` 之前接线，SessionStart/UserPromptSubmit 声明的上下文不会丢失。解析带脱敏与 8192 字节上限。
- **MCP 注入扫描（§3）**：`ARGENT_MCP_INJECTION_SCAN` 只对高风险注入前置可读警示，只警示不改写、不过滤原文。
- **doctor 扩展**：配置注入与会话健康检查（走 warn issues 通道，不改 golden / 退出码）；工具耗时直方图接入 dispatch 与轮末 `TopSlow` 日志（opt-in）。

#### R42 更新记录（流中断回补、计划审批循环，CC 范式）

- **流中断非流式回补（§2）**：三协议全部获得 `ChatOnce`（非流式单 JSON 解析，`stream:false` 且不带 `stream_options`）；仅在"已有部分文本 + `ErrStreamError` + 活 ctx"时触发，每轮最多一次，只发布精确后缀（不重复已输出前缀），发 `EventRetry stream_recovery`。此时仍为默认关闭原语。
- **`exit_plan` 计划审批循环（§4）**：模型驱动的计划批准闭环；通过 tools ctx 回调解锁（`SetPlanUnlock`），仅计划模式注册 `exit_plan`；未批准前修改类 bash 走既有 Chain 被守卫拦下。
- 工具调用轨迹（Begin/End 成对，含 `ran=false`）接入轮末日志，与耗时直方图互补。

#### R43 更新记录（可靠性加固、流回补接线）

- **可靠性加固**：server 四处 fire-and-forget goroutine（RPC prompt/subscribe、ACP prompt、WireAgentEvents）补 panic 恢复——出错回包 + 清理 + 保证 channel 关闭，不崩进程、不挂客户端，race-clean；agent `callTool` panic 边界激活（`safeToolPanic`：panic 转普通工具错误，与 Registry recover 双保险，正常字节不变）；JSONL `Close()` 加 fsync，干净退出不丢尾（不做逐 append fsync、不改 `flushLocked`，崩溃恢复语义不变）。
- **能力接线（§2）**：`ARGENT_STREAM_RECOVERY=1` 把 R42 的流回补端到端交到用户手里（流错误时确实调用 `ChatOnce` 并发 `stream_recovery` 事件）；关闭时 `EnableStreamRecovery` 为零值、请求与事件逐字节不变。
- 新原语：provider 能力画像、transcript 统计、prompt store（JSONL 历史 + frecency + 原子写），均为标准库、默认惰性。

---

### 10. Linux 已知降级（诚实说明）

本矩阵的"可移植性/部署"证据只覆盖**编译期**：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 与 5 平台交叉编译矩阵全绿；但项目在本机 Windows 开发与实测，**没有在真实 Linux 环境跑过运行时验收**。若在非 Windows 平台使用，以下能力按设计降级，不算 bug 也不算对标领先：

| 降级点 | Windows 行为 | 非 Windows 行为 | 源码落点 |
|---|---|---|---|
| 凭据加密（§5） | DPAPI（`crypt32.dll` syscall）按当前用户加密令牌 | 无 DPAPI，明文存储，仅靠 token 文件 `0600` 权限隔离 | `internal/auth/token_crypt_windows.go`、`internal/auth/token_crypt_other.go` |
| TUI 剪贴板（§4） | `CF_UNICODETEXT`（Win32 syscall）写剪贴板 | `writeClipboard` 空操作，复制键无副作用 | `internal/tui/clipboard_windows.go`、`internal/tui/clipboard_other.go` |
| 原生桌面 GUI（§4 ⚠️-2） | `argentgo gui` 启动 MyGo 原生窗口 | 有显式守卫：非 Windows 打印 "native client is Windows-only" 并返回 1 | `cmd/argent/gui.go` 的 `runtime.GOOS != "windows"` 分支；`scripts/build-release.ps1` 只收 Windows/amd64 |

命令行引擎、`--serve` / `--rpc` / `acp`、TUI 主体与全部内置工具走纯标准库，交叉编译后可在 Linux 运行；上述三点是平台相关的已知边界，不计划在核心引入 cgo 或第三方 GUI 依赖来消除。

---

## killi 研究：可迁移思想与落地记录

本文件记录从 [`killi`](https://github.com/timwuhaotian/killi)（Rust 终端编码 agent）源码研究中提炼的工程思想，以及迁移到 argentgo 的结果。研究时间：2026-11-22 ～ 11-23。killi 源码快照位于 `D:\code\mpi\killi`，其设计决策见该仓库 `docs/decisions.md`（38 条）与 `docs/reference/` 两份 harness 清单。

### 核心结论

killi 的三条工程纪律：

1. **缓存前缀绝不破坏**：工具列表整会话固定、system prompt 冻结、history 只追加、提醒放末尾、所有"拦截"在工具执行期做。
2. **资源占用有界**：大文件拒绝、输出滚动上限、超时包含写入、杀整棵进程树、按 hash 去重。
3. **原始请求可复现**：日志记录实际发送的线协议字节，resume 回放字节而非重建语义。

argentgo 的底子并不弱：plan mode 已是"运行时拦截而非移除工具"、reminder 已在末尾、会话日志已单次写、thinking delta 已原地追加、参数强转已接线。差距主要在"把纪律变成 CI 断言"和"资源边界"。

### 已迁移（按提交顺序）

| 提交 | 来源 | 内容 |
|---|---|---|
| `17b0193` | 决策 35 | `read` 读前 `Stat` 检尺寸，>10 MB 拒绝并提示 `grep_ex`/`sed`，消除"文件+解码副本+行切片"数百 MB 峰值 |
| `7d6807f` | 决策 28/9 | prompt 预算护栏（system + 工具定义 ≤40 KB）+ 两条缓存前缀不变量（提示不随 cwd 变、`Specs()` 逐字节稳定） |
| `e77e844` | 决策 12/35 | `web_fetch` 读体前查 Content-Type，二进制零下载；模糊类型放行避免误伤 |
| `c3718b3`/`8ce2a0d` | 决策 35 | 钩子超时终止整棵子进程树；proc 导出 `ConfigureGroup`/`KillGroup`/`ProcessAlive`；修复 Windows `killTree` 顺序（先 `taskkill /T` 再终止作业对象），否则作业关联后启动的孙进程漏网 |
| `0f306ee` | 决策 26 | 把已有但只被测试调用的 `SetLoopDetection` 接成 `ARGENT_LOOP_DETECTION` opt-in，每次 Execute 按本次 env 显式设置 |
| `3254361` | 决策 15 | `eval history`：从 git 历史抽任务（commit message 当 prompt、新增测试种入父提交 worktree、`FileEquals` 防作弊、`go test` 评分）；`eval.Options` 加可注入 `WorkspaceFactory`（nil 默认路径逐字节不变） |
| `faefe3b`/`3e66c71` | 决策 21 | 请求字节快照：llm 三协议流式 marshal 点把实际发送的字节交给 context sink，opt-in 追加到会话；端到端测试验证 sink 字节与服务端实际收到的请求体逐字节相同 |
| `aff2cd2` | 决策 15 | `eval history --scripted`：离线脚本模式跑通 git→worktree→评分全流程，无需 API key |
| `00feb74` | 决策 15 | `eval history verify`：每个任务在父提交下应失败、参考提交下应通过，离线过滤无判别力任务 |
| `95d0e48` | 决策 21 | 快照钩子 benchmark（默认 3.4ns、开启 9.7ns，均零分配），确认不成为热路径瓶颈 |
| `33ef69f` | 决策 15 | 历史任务检查改为只跑新增测试所在的包而非 `go test ./...`，超时提到 120s，避免全新 worktree 首次编译超时误判 |
| `c359f9e` | 决策 15 | `eval history --verify`：跑模型前离线自检，跳过无判别力任务，避免浪费 token |
| `b47494a` | 决策 20 | `ARGENT_SUBAGENT_SHARED_PROMPT`：子代理以父级 system 为前缀、角色约束追加末尾，首请求复用缓存；system 构造抽成可测纯函数 |
| `4a8e768` | 决策 20 | 锁定"无角色白名单时子代理工具表与父级逐字节一致"不变量（工具表复用已天然成立），显式 AllowedTools 白名单语义保留 |
| `c55613e` | 决策 21 | resume 字节回放：三协议抽 `sendStream` + 可选 `RawChatter.ChatRaw`（跳过构造与快照写入），agent 首请求回放快照原始字节、校验失败回退，`ARGENT_RESUME_BYTE_REPLAY` 接线 |
| `b0352f3` | 决策 11 | MCP 单网关：`ARGENT_MCP_GATEWAY` 只公布一个固定 mcp 工具、服务首次使用才连接、发现走文本、Specs/RefreshTools 不改工具列表；派生名审批 + 只读沙箱拦截 + per-call 链副本 |
| `c5677c1` | 决策 15 | `--verify` 复用已抽取的任务集，不再重复跑 git log/diff/show |
| `325f6ad` | 决策 21 | `sessions snapshots <id>` 只读子命令：列出请求快照的协议/模型/字节数/hash 摘要，不打印完整 body |
| `650dd88` | — | 本研究总结文档 |

另：`9d8945a` 修复项目 TODO #28 的审计脱敏缺口（`--password value` 形式的 CLI flag 值此前明文进入审计）；`dcf9947` 落地项目 TODO #126（学自 codex 的 `ARGENT_COMPACT_DRAIN`）；`1a998f1` 落地项目 TODO #122（学自 pi-mono 的 follow-up 队列）。三者虽非 killi 迁移，但同属本轮工程纪律加固。

### 后续确认不做（功能等价或取舍不同）

| 项 | 来源 | 原因 |
|---|---|---|
| undo 用隐藏 git 快照 | 决策 24 | argentgo 已有 checkpoint 机制（文件旧字节存会话），功能等价但机制不同；切换代价大于收益 |
| bash 输出尾部遍历 | 决策 35 | `internal/proc.cappedBuffer` 已对每个流做 64 KB 头尾上限，同类内存峰值问题已解决 |

> 三项高风险迁移（MCP 单网关 `b0352f3`、resume 字节回放 `c55613e`、子代理工具表复用 `4a8e768`）已全部落地，均为严格 opt-in、默认零字节改动。此前"未迁移"的判断是风险预评估，非能力缺失。

### argentgo 原本就做对的点

- plan mode 运行时拦截、reminder 末尾注入（`budget_reminder.go`）
- 会话 JSONL 单次写（`session_append.go`）、单次 `json.Marshal` + 一次 `Write`
- `strings.Builder` + `WriteString` 原地追加 thinking delta（无 O(n²)）
- `io.LimitReader` 限制 fetch 体积、参数强转 `tool_argument_coercion.go` 已接线
- skill 名字-only 索引 + 按需加载（`internal/skills`）

### 验证结果（2026-11-23 09:16，三项高风险迁移落地后）

- 全量快验证（`gofmt -l .` / `go vet ./...` / `go test -p 4 ./... -count=1`）：**全绿**，11356 测试通过。
- 针对性 race（`./internal/app ./internal/agent ./internal/llm ./internal/session`）：**全绿**，6831 测试通过（覆盖 MCP 网关 catalog 与字节回放）。
- 三项 opt-in 均已登记 `OPT_INS.md` 与 `optin_metadata.go`，默认关闭时既有字节路径不变。

#### 早期验证记录（02:24）

- 全量快验证（`gofmt -l .` / `go vet ./...` / `go test -p 4 ./... -count=1`）：**全绿**，11364 测试通过。
- 针对性 race（`./internal/llm ./internal/agent ./internal/hooks ./internal/proc ./internal/session`）：**全绿**，5805 测试通过。
- Linux 交叉编译（`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`）：**通过**。
- 全量 `go test -race ./...`：2 处竞争均在 `internal/server/event_persist_test.go`（测试 goroutine 并发读 `httptest.ResponseRecorder` 的 buffer，SSE handler 在 `event.go:456` 并发写），是测试侧竞争，非本轮改动引入；普通全量测试全绿。
- `internal/computer` 的 `TestCapture` 曾在 headless 环境下 BitBlt 失败，后续全量验证已通过（环境相关，非回归）。

### 复现方式

- `eval history <repo> --scripted`：离线跑一遍 git→worktree→评分流程（任务预期失败，因为脚本不修 bug）
- `eval history verify <repo>`：离线自检任务判别力（无需模型）
- `eval history <repo> --scripted --verify`：先自检过滤，再离线跑有效任务
- `ARGENT_LOOP_DETECTION=1`、`ARGENT_REQUEST_SNAPSHOT=1`：开启对应 opt-in
