# 能力对标矩阵

这份文档回答一个问题：**在用户真正能感知到的能力上，argentgo 与 Claude Code / OpenCode / Crush / Codex 相比，哪里已经追平、哪里还差、哪里是有意不做。**

它是后续迭代的方向盘，不是又一份评分表。取证基线为 `HEAD=42fe404`，所有落点均为源码中的真实行号；行号或开关名与源码不一致时，以源码为准。

## 0. 口径与读法

### 0.1 与 STATUS 8.9 分的关系（重要）

[STATUS.md](STATUS.md) 的 **8.9 / 10** 评估的是**架构、安全与测试证据**：分层零违规、策略→审批→审计链、fuzz 目标、交叉编译矩阵、gopls 端到端测试等。这些分数高，说明"工程底座扎实"。

本文档刻意换一个镜头，只看**用户可感知能力**：一个真实用户坐到终端或桌面窗口前，能不能完成"读代码 → 改代码 → 回退 → 换模型 → 续会话 → 调工具"这条链路，以及做这些事时和主流竞品的体感差距。

> 举例：STATUS 给"安全性(架构) 10 分"，是因为链和 fuzz 都在；但用户感知到的"审批弹窗是否顺手""每行编辑是否要确认"是本文档的议题。两者不冲突，但不能互相替代。

### 0.2 三个标注

| 标注 | 含义 |
|---|---|
| ✅ **已对标或领先** | 能力存在且可用，与竞品同档或更好；有源码落点支撑。 |
| ⚠️ **真实差距** | 能力缺失或明显弱于竞品，且**属于可移植、值得补**的一类；每条给出确切落点文件与行号，作为 backlog。 |
| ⊘ **合理取舍** | 明确不做；给出理由，并说明用什么替代。 |

### 0.3 对标对象与限制

竞品一栏基于各项目的**公开产品形态与文档**归纳，不是对其源码逐行取证，也不锁定具体版本；口径是"这类产品当前普遍具备的能力"。凡竞品能力本身有争议的，本文档宁可标"视版本/配置而定"，不替对方夸大。

### 0.4 一条准确性提示

[TECHNICAL.md](TECHNICAL.md) 的 Feature parity 表**不在本文档修改范围内**，且可能与源码存在时间差。例如该表把 `secondary_model` 列为 "in progress"（`docs/TECHNICAL.md:708`），但源码已在 `internal/app/app_execute.go:94` 把它赋给子代理 `SubagentConfig.Model`（`internal/app/app_execute.go:92-100`）。遇冲突时，**一律以源码行号为准**。

---

## 1. 上下文管理

### 1.1 当前能力清单（源码落点）

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

### 1.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 自动压缩 | 有，模型生成式摘要 + 后台触发 | 多为自动/半自动压缩 | 有，但摘要为抽取式（见 ⚠️-1） | ⚠️ |
| 提示缓存 | 隐式自动 | 部分产品依赖 provider 支持 | 有，按协议显式控制，覆盖面略窄 | ✅ |
| 上下文用量可视化 | 有状态栏百分比 | 多数有 token/成本显示 | TUI 有预算提醒与 usage 工具（`internal/tools/tools_productivity.go:154`） | ✅ |
| 推理内容保留 | 有（extended thinking） | 视 provider 而定 | 有，`PreserveReasoning` + `reasoning_delta` | ✅ |
| 结构化压缩证据 | CLAUDE.md 记忆 + 自动压缩 | 多依赖会话摘要 | 压缩记录显式保留 files/failures/nextSteps，强于一般实现 | ✅ 领先 |
| 语法级上下文（AST 裁剪） | 有 IDE 侧符号理解 | OpenCode 内建 LSP | 走 LSP 符号，不做树内 AST（见 ⊘-1） | ⊘ |

### 1.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **压缩摘要是抽取式，不是模型生成式。** `Summarize` 用 `firstLine` / `nextStepLines` 逐行拼接，信息密度和连贯性弱于 Claude Code 的自动压缩摘要。**R20 已交付 opt-in 模型摘要原语**（`internal/app/model_summary.go`，`ARGENT_OPT_INS=model_summary`）：模型只补增量叙事、绝不覆盖 files/failures 权威字段、失败回落抽取式；但压缩点调用接线待后续（属体验增量非正确性）。 | `internal/session/compact.go:329`（`Summarize`）；原语 `internal/app/model_summary.go`（`BuildSummaryPrompt`/`ParseModelSummary`/`ApplyModelSummary`/`GenerateModelSummary`） | 接线模型摘要到压缩点（app 层持 ctx/chatter，加超时与回落）；保持 `Structured()` 契约不变。 |
| 2 | ~~会话侧截断仍按 rune，不是 grapheme。~~ **R20 已解决**：`session/` 的 4 处主路径截断（`session_message.go` 的 `truncateRunes`、`attempt.go` 的 `capProgressRunes`、`branch_summary.go`、`search.go` 的 `contextWindow`）统一改走 `internal/textutil/clip.go` 的 `ClipRunes`/`ClipWindow`——保持 rune 预算语义、只向字簇边界吸附，默认路径（ASCII/纯汉字/单独 emoji）逐字节不变；`implicit_context.go` 早已采用 `ClusterBoundaryAfter`。字簇边界为 UAX#29 务实子集（组合符/ZWJ/旗帜/肤色/jamo/CRLF），非完整 Unicode 合规。 | `internal/textutil/clip.go`（`ClipRunes`/`ClipRunesTail`/`ClipWindow`）；调用点 `internal/session/session_message.go`、`attempt.go`、`branch_summary.go`、`search.go`。STATUS.md「核心引擎」已升至 10 分。 | 残留：`internal/memory/sclip.go` 是第二套近似实现（RI 成对分组、非法字节丢弃语义不同），留待后续统一；不影响 session 主路径正确性。 |
| 3 | **提示缓存对 OpenAI 兼容端点默认关闭。** `cacheEnabled` 对 `openai-chat-completions` 返回 false，需显式 `--force-prompt-cache` 类开关，用户不知情时拿不到缓存收益。 | `internal/llm/options.go:109-120`（`cacheEnabled` 的 `default` 分支） | 增加按 `baseURL` 的探测/配置显式声明，不改默认安全语义；补充用户可见提示。 |

### 1.4 ⊘ 合理取舍

- **⊘-1 不做内嵌 tree-sitter AST。** 会引入 cgo，破坏 `GOOS=linux go build ./...` 的静态交叉编译（见 STATUS.md:57、TECHNICAL.md:753）。替代：`repo_map` + 六个 `lsp_*` 工具，靠外部语言服务器提供符号，不捆绑解析器。

---

## 2. 可恢复性

### 2.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 只追加 JSONL 会话树 | `internal/session/session_append.go:14/21/82` | `id`/`parent` 链接，fork 不重写历史。 |
| 分支 / fork | `internal/session/session_branch.go:41/54`、`internal/session/tree.go:31/68`（`Branches`） | 从任意记录分叉。 |
| Checkpoint（改动前快照） | `internal/session/checkpoint.go:122`（`CaptureCheckpoint`）、`:215`（`RecordCheckpoint`） | 记录改动前字节与 sha256。 |
| Undo（按 checkpoint 还原） | `internal/session/checkpoint.go:271`（`UndoCheckpoint`）、`:288`（`undoCheckpoint`） | 文件被改动后拒绝还原，还原也走安全链。 |
| Retry（从 checkpoint 重试） | `internal/session/checkpoint.go:480`（`RetryFromCheckpoint`）、`:518`（`Retry`） | 从 checkpoint 父节点长出新叶。 |
| 遗弃分支摘要 | `internal/session/branch_summary.go:47`（`SummarizeAbandonedBranch`） | fork 后的旁支不丢失上下文。 |
| 压缩归档 | `internal/session/compact.go:635/655/771` | 被丢弃消息写入归档，可回读。 |
| 离线导出 | `internal/session/export.go:91`（`ExportProject`）、`:94`，格式 `html`/`json`/`markdown` | 可选择脱敏，2 MiB 上限（`internal/session/export.go:25`）。 |
| CLI 恢复入口 | `sessions undo / retry / tree / export / find --grep`（TECHNICAL.md:423） | 命令级完整。 |
| `--rpc` 会话读取 | `session.list` / `session.load`（TECHNICAL.md:519-520） | 原生桌面窗口的会话栏依赖它。 |

### 2.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 会话续跑 | `--continue` / `--resume` | 均有 resume | `--resume` / `--session` / `--continue`（TECHNICAL.md:470） | ✅ |
| Checkpoint 回退 | Esc Esc 回退 + fork | 部分产品有类似 rewind | 文件级 checkpoint + undo/retry，且带 sha256 防串改 | ✅ 领先 |
| 分支树导航 | 有 | 部分有 | 有 `sessions tree` 与 fork；CLI 侧成熟，GUI 侧仅列表 | ⚠️-1 |
| 会话内容搜索 | 有 | 视产品而定 | `sessions --grep` + `find`，片段脱敏 | ✅ |
| 离线分享 | 视产品/企业策略 | 不统一 | HTML/JSON/Markdown 离线导出，无网络引用 | ✅ |
| ACP 会话恢复 | Zed/ACP 生态通常支持 session/load | ACP 客户端多自带会话状态 | **明确不支持** `session/load`（见 ⚠️-2） | ⊘/⚠️ |

### 2.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **GUI 侧缺少分支树的可视导航。** 会话树、fork 都在 CLI 和 JSONL 层完备，但原生窗口的会话栏只做 `session.list` / `session.load` 的线性列表。 | `native/nav_panel.go`、`native/bridge.go:39-47`（`rpcMessage`）；CLI 对照 `internal/session/tree.go:31`（`Branches`） | 在 `--rpc` 暴露一个只读 `session.tree` 方法（复用 `Branches`），桌面侧渲染分支；不引入新依赖。 |
| 2 | **ACP 不支持 `session/load`。** 直接返回 method-not-found，因此 ACP 客户端无法续跑历史会话。 | `internal/server/acp.go:131-132`（`case "session/load"`） | 视目标 ACP 客户端需求决定是否接入；若不做，保留 ⊘ 理由：ACP 客户端通常自带会话状态，续跑走 CLI `--resume` / `--rpc session.load`。 |

### 2.4 ⊘ 合理取舍

- **ACP `session/load` 暂不做**：见上表；`--rpc` 已提供 `session.load`（`internal/server/rpc.go` 方法分发），能力未丢失，只是不在 ACP 协议面上。

---

## 3. 工具执行

### 3.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 默认工具集 | `internal/tools/builtin_register.go:30-71`（30 个静态注册）+ `internal/tools/tools_registry.go:120-127`（`context_pack`、`memory_reflect`/`memory_learn`、`tool_search`） | 默认共 34 个；比 STATUS.md:84 的"~31"更新更多。 |
| 可选工具按开关挂载 | `internal/app/mcp.go:69-78`（`wireOptInTools`：apply_patch / grep_ex）、`internal/app/checkpoint_source.go:52`（`checkpoint_diff`） | 不污染默认 spec 顺序。 |
| 工作区边界 | `internal/security/path_real.go:32`（`CheckPathInRoots`）、`internal/tools/tools_registry.go:357`（`resolve` 拒绝 `..`/绝对路径逃逸） | 每个 FS 工具都解析到工作区。 |
| 安全链 | `internal/security/chain.go:139`（`NewChain`）、`:148`（`Check`）、`:163`（`check`） | 策略 → 审批 → 审计，顺序固定。 |
| 三种沙箱模式 | `internal/security/sandbox.go:14-18`（`read-only` / `workspace-write` / `danger-full-access`）；过滤见 `internal/tools/catalog.go:84` | read-only 移除全部 `Mutating()` 工具。 |
| 审批模式 | `--approval ask\|never\|auto`、`--yolo`（`internal/cli/flags_register.go:17/27`）；实现见 `internal/security/approver.go:49/104/154/258` | 含作用域授予、TTL、缓存。 |
| 脱敏与审计 | `internal/security/redact.go:14`（`Redact`）、`internal/security/audit.go` | 写审计前脱敏。 |
| 长命令转后台 job | `internal/shell/tools_builtin_bash.go:74/81`（`background_after_ms`）、`internal/jobs/` | 超时后不杀进程，返回 job id。 |
| MCP | `internal/mcp/`；per-server `enabled` / `readOnly` / `allowedTools` / `deniedTools`（`internal/mcp/permissions.go`） | stdio + HTTP/SSE，项目级 `mcp.json` 受 `--trust-project` 门控（`internal/app/mcp.go:272/575`）。 |
| Hooks | 11 事件（`internal/hooks/event_handlers.go:91-103`）、SHA-256 信任、exit-2 阻断 | 工具调用前后可拦截。 |
| LSP 工具 | 六个 `lsp_*`，惰性启动（`internal/app/lsp_adapter.go`、`internal/agent` 不 import `lsp`） | 预览类工具只读，不落盘。 |

### 3.2 对标

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

### 3.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **`web_search` 没有内置后端。** 未配置端点时报错并建议 `web_fetch`。**R20 已补可发现性**：`doctor` 增加 web_search 配置检查（warn 提示 `ARGENT_WEB_SEARCH_URL`/`BRAVE_API_KEY`/`TAVILY_API_KEY`）；`internal/tools/web_search_presets.go` 提供 Brave/Tavily/SearXNG 预设元信息。诚实边界：**无用户 key / 自托管服务就没有联网搜索**，不用 HTML 抓取冒充内置搜索。 | `internal/tools/web_search.go:17/26/53`；`internal/cli/doctor.go`（`checkWebSearch`）；`internal/tools/web_search_presets.go` | 可选：把预设接入 `Run` 的请求头注入（需 key）；保持不联网测试可用。 |

### 3.4 ⊘ 合理取舍

- **⊘-2 不做浏览器 / computer-use 工具**：Rust 参考版对应工具未移植（TECHNICAL.md:697 `--enable-browser` → parse-then-error），引入无头浏览器会破坏"核心零依赖、无 cgo、无托管服务"的底线。
- **⊘-3 不做 DAP 适配器**：明确不在范围（TECHNICAL.md:393）；成本高、与 LSP 的文本交互模型不同，收益低于把 IDE 级 LSP 方法补齐。

---

## 4. 交互体验

### 4.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 全屏 TUI | `internal/tui/`（自绘 ANSI，无 WebView，可跑 SSH）；面板 Ctrl+T/B/P（TECHNICAL.md:56） | tasks / workspace / plan 三面板。 |
| 原生桌面窗口 | `native/`（MyGo），两个二进制，`--rpc` 驱动（`native/bridge.go:55`、`:112`） | 唯一 GUI；`prompt/delta` 流式渲染。 |
| 桌面交互 | undo / retry / 主题 / 审批 Y/N（`native/main.go:202-208`、`:503-508`、`:1020`、`:1041`） | 会话栏 + 消息列表 + 工具卡片。 |
| 三种无头传输 | `--serve`（SSE）、`--rpc`、`acp`（TECHNICAL.md:42-51） | 事件语义需在三者间保持一致。 |
| 审批可视化 | TUI 审批弹窗 + 危险命令高亮（`internal/tui/approval*.go`） | 与 `security.Chain` 同源。 |
| 国际化 | `zh-CN` / `en-US`（`internal/i18n/i18n.go:19-20`），目录为二进制内 Go 表；含翻译完整性审计（`:363`） | 缺键返回键并记录。 |
| 流式事件 | `text_delta` / `reasoning_delta` / `tool_start` / `tool_end`（`internal/agent/events.go:19` 起） | 传输层转发同一份流。 |

### 4.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 终端体验 | 强（含 IDE 扩展） | OpenCode/Crush TUI 都很强 | 自绘 ANSI，SSH 可用，面板齐全 | ✅ |
| 桌面 GUI | 有 | 部分有 Web/桌面端 | 原生 MyGo，低内存（约 76MB vs WebView ~400MB，TECHNICAL.md:304-305） | ✅ 领先（内存/无 WebView） |
| 流式与审批 | 成熟 | 成熟 | 端到端一致，原生窗口可 Y/N | ✅ |
| 语言覆盖 | 多语言 | 多为英文 | 仅 zh-CN / en-US | ⚠️-1 |
| 桌面多平台 | 跨平台 | 多平台 | **仅 Windows/amd64 发布**（STATUS.md:48-51） | ⚠️-2 |
| 真实模型质量评测 | 有内部评测体系 | 视产品而定 | `evals/core.json` 20 个脚本场景，但无真实模型质量评测（STATUS.md:82） | ⚠️-3 |
| IDE 级 LSP 效果 | 有 | OpenCode 内建 LSP 强 | 6 基础 + 3 进阶方法已实现；semantic/inlay/codeLens 未用真实 server 验证效果（TECHNICAL.md:392） | ⚠️-4 |

### 4.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **只有两种语言环境。** UI 字符串与提示语言仅 zh-CN / en-US。 | `internal/i18n/i18n.go:19-20`（`EnUS` / `ZhCN`）、`:24`（`DefaultLocale`） | 若目标用户含日语等，先扩目录 + 用现有 `AuditTranslations`（`:363`）保证完整性；否则保留为取舍。 |
| 2 | **桌面 GUI 只有 Windows 构建。** 发行脚本与 CI 收口 Windows/amd64。 | `scripts/build-release.ps1`、`STATUS.md:48-51` | 需要 Linux GUI 时先解决 MyGo 多平台打包与本地验证，不改核心。 |
| 3 | **无真实模型质量评测。** eval 场景脚本化、provider 可替换，但不衡量回答质量。 | `evals/core.json`、`internal/eval/`、`STATUS.md:82` | 增加可选的真实 provider 评测集（默认离线不跑），与脚本 eval 分层。 |
| 4 | **进阶 LSP 效果未经真实 server 验证。** 方法已实现，但 semantic tokens / inlay hints / code lens 的实际返回值取决于服务器。 | `internal/lsp/features_advanced.go:11/31/52/75`（`SemanticTokens` / `InlayHints` / `CodeLens` / `ResolveCodeLens`）、`internal/lsp/gopls_integration_test.go` | 参照现有 gopls 集成测试的"硬断言/尽力而为"分层，为进阶方法补可选验证。 |

### 4.4 ⊘ 合理取舍

- **不做浏览器前端**：Web 前端已完全移除（TECHNICAL.md:275-287）。理由：WebView2 常驻约 400 MB、需 HTTP+SSE 桥和 SolidJS 工具链；原生窗口单进程约 76 MB、复用既有 `--rpc`，核心保持零依赖。

---

## 5. Provider 协议

### 5.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 三套线协议 | `internal/llm/options.go:16-17`（`ProtocolAnthropicMessages` / `ProtocolResponses`）、`internal/provider/templates.go:18-22`（`anthropic-messages` / `openai-chat-completions` / `openai-responses`） | 协议细节只在 `internal/llm`。 |
| 20 个内置 provider 模板 | `internal/provider/templates.go:61-202`（`builtInTemplates`） | Anthropic / OpenAI / DeepSeek / Moonshot / 智谱 / Qwen / OpenRouter / Ollama / LM Studio / xAI / Gemini / Groq / Mistral 等。 |
| 重试与错误分类 | `internal/llm/failure_contract.go:53`（`IsRetryableError`）、`:89`（`ClassifyRetry`）、`internal/llm/attempt.go` | 流完整性、重试原因、可恢复错误统一。 |
| Fallback 模型 | `internal/app/app_chatter.go`、`internal/cli/flags_register.go`（`fallback_model`） | Chatter 层注入。 |
| 提示缓存 | `internal/llm/options.go:95/109` | 见 §1。 |
| 推理协议映射 | `internal/llm/anthropic.go:226`、`internal/llm/options.go:71-79` | `reasoning_content` / `thinking` 块与保留策略。 |
| 认证 | `internal/auth/`（PKCE / 设备码）、`argent login/logout/auth` | 密钥不回显。 |

### 5.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 协议深度 | 单一 Anthropic 协议做到极深 | 多 provider 为主 | 三协议 + 20 模板，广度好、单协议深度不及 Claude Code | ✅（广度）/ ⚠️（深度） |
| 重试 / 流完整性 | 有 | 有 | 显式失败契约 + 流完整性校验，工程证据强 | ✅ |
| 缓存 / 推理 | 强 | 视 provider | 有，按协议显式控制 | ✅ |
| 云厂商长尾（Bedrock / Vertex） | 有 | 部分有 | **无**（源码零命中） | ⊘-1 |
| 本地模型 | 不主打 | 部分支持 | Ollama / LM Studio / local 模板（TECHNICAL.md:113-159） | ✅ |

### 5.3 ⊘ 合理取舍

- **⊘-1 不做 Bedrock / Vertex 长尾**：源码内无 `bedrock` / `vertex` 命中。理由是 AWS/GCP 认证链路（签名、角色、区域、凭证链）复杂、个人自用收益低；且 `openrouter` / 兼容端点模板（`internal/provider/templates.go:107/114`）与 `--base-url` + `--protocol`（`internal/cli/flags_register.go`）已覆盖绝大多数接入需求。若未来确有需要，优先走兼容网关而非内建签名逻辑。

---

## 6. 开发者体验

### 6.1 当前能力清单（源码落点）

| 能力 | 实现落点 | 说明 |
|---|---|---|
| 七层配置优先级 | `internal/config/`、`TECHNICAL.md:618-626` | 默认值 → home → 项目 → `argent.json` → profile → `ARGENT_*` → CLI。 |
| JSONC + 引用 | `internal/config/jsonc.go`、`internal/config/config.go` | 注释/尾逗号、`{env:VAR}` / `{file:path}`。 |
| Profile / env 覆盖 | `internal/config/`、`TECHNICAL.md:630-632` | `ARGENT_PROFILE` + `ARGENT_MODEL` 等。 |
| 插件（清单发现） | `internal/plugins/`、`--plugins <name>`（TECHNICAL.md:454） | 贡献 skills / commands / MCP；marketplace 不迁。 |
| Skills | `internal/skills/`（`SKILL.md` 发现 + 渐进披露）、`use_skill` | 含自写能力（`internal/skills/author.go`）。 |
| 斜杠命令 | `internal/commands/`、markdown 文件发现 | `argent commands` 列出。 |
| Hooks | `internal/hooks/` + SHA-256 信任（`internal/hooks/trust.go`） | 已配置规则进入安全链。 |
| LSP | `internal/lsp/`（6 基础 + 3 进阶方法）、gopls 集成测试 | 惰性启动、按 root+server 缓存。 |
| Eval | `internal/eval/`、`evals/core.json`（20 场景）、`argent eval` | 脚本化、不联网。 |
| Workflow / 自动化 / jobs | `internal/workflow/`、`internal/automation/`、`internal/jobs/` | DAG、cron、后台 job。 |
| 诊断 CLI | `doctor` / `bug` / `audit` / `usage` / `policy` / `config`（TECHNICAL.md:421-452） | 机器可读 `--json`。 |

### 6.2 对标

| 子项 | Claude Code | OpenCode / Crush / Codex（公开形态） | argentgo | 标注 |
|---|---|---|---|---|
| 配置灵活性 | settings.json + 环境变量 | 各配置文件 | 七层 + JSONC + profile + 引用，强 | ✅ 领先 |
| 项目级说明文件 | CLAUDE.md | AGENTS.md 等 | AGENTS.md / CLAUDE.md 注入（`internal/memory/`）+ skills | ✅ |
| 插件生态 | marketplace | 部分有插件系统 | 清单发现 + opt-in，`--plugins`；无 marketplace | ⚠️-1 / ⊘-1 |
| 调试协议 | 有 IDE 集成 | OpenCode LSP 强 | LSP 完善，DAP 不做 | ⊘-2 |
| 可观测性 | 日志/审计 | 视产品 | 审计、脱敏、doctor、bug 包、jobs 面板，工程证据强 | ✅ |
| 脚本化评测 | 有内部体系 | 不统一 | `eval` + 20 场景，可复现 | ✅ |

### 6.3 ⚠️ 真实差距（backlog）

| # | 差距 | 确切落点 | 建议 |
|---|---|---|---|
| 1 | **没有 marketplace / 一键插件安装体验。** 插件靠清单发现 + `--plugins` 按次启用，缺少搜索/安装/更新的用户路径。 | `internal/plugins/`（`discovery.go` / `install.go` / `merge.go`）、`--plugins`（TECHNICAL.md:454） | 若要做，先补 `plugin search/install` 的本地清单操作；注意这是定位取舍，不与 Rust 版 marketplace 对齐（STATUS.md:36）。 |

### 6.4 ⊘ 合理取舍

- **⊘-1 不做 marketplace**：Rust 版 marketplace 明确不迁（TECHNICAL.md:742）；个人项目无分发运营收益。
- **⊘-2 不做 C-ABI 插件加载器 / Rhai 脚本插件**：Go 无对应生态，引入即破坏零依赖（TECHNICAL.md:752/754）。
- **不做 Node/Cordis 插件图、SQLite/FTS5、Honcho 用户建模、聊天机器人、远程沙箱、JS code-mode 虚拟机、托管目录**：均为重量级依赖/服务或定位取舍（STATUS.md:36）。
- **不做 telemetry 运营**：个人项目无收益，仅保留空操作子命令（STATUS.md:36、TECHNICAL.md:756）。

---

## 7. 不做清单（汇总，含理由）

| 不做项 | 理由 | 替代方案 | 证据 |
|---|---|---|---|
| tree-sitter AST / 语法裁剪 | cgo 破坏 `CGO_ENABLED=0` 静态交叉编译 | LSP 符号 + `repo_map` | STATUS.md:57、TECHNICAL.md:753 |
| 浏览器前端 / WebView / SolidJS | ~400MB 常驻、需新工具链与桥接 | MyGo 原生窗口（~76MB）+ `--rpc` | TECHNICAL.md:275-287、:302-308 |
| DAP 适配器 | 成本高、与文本交互模型不同、收益低 | 把 IDE 级 LSP 方法补齐 | TECHNICAL.md:393、STATUS.md:57 |
| Bedrock / Vertex 长尾 | 云厂商签名/认证复杂、个人自用收益低 | 兼容网关模板 + `--base-url` / `--protocol` | 源码零命中；`internal/provider/templates.go:61-202` |
| C-ABI 插件 / Rhai 脚本 / Node 插件图 | Go 无生态、破坏零依赖 | 清单发现 + opt-in 工具 | TECHNICAL.md:752/754、STATUS.md:36 |
| SQLite/FTS5、Honcho 建模、聊天机器人 | 重量级依赖/服务、定位取舍 | 现有 JSONL 搜索 + 记忆库 | STATUS.md:36 |
| 远程沙箱 / OS 沙箱原语 / JS code-mode VM | 引入标准库外依赖或服务 | 三沙箱模式 + 审计链 | STATUS.md:36、TECHNICAL.md:393 |
| marketplace / telemetry 运营 | 个人项目无运营收益 | 本地插件清单 + 空操作子命令 | STATUS.md:36、TECHNICAL.md:742/756 |
| document/office 预览、device 桌面控制、TTS | 只服务桌面端或无用户面 | `web_fetch` + 原生控件 | TECHNICAL.md:674-675/755、STATUS.md:84 |

---

## 8. 一图看懂（六维总览）

| 维度 | 总标注 | 一句话 |
|---|---|---|
| 上下文管理 | ✅ + ⚠️ | 预算/裁剪/去重/steering/结构化压缩证据齐全；R20 已修 grapheme 切点（升至 ✅），剩余差距仅摘要生成式（opt-in 原语已交付，接线待续）。 |
| 可恢复性 | ✅ 领先 | JSONL 树 + sha256 checkpoint + undo/retry + 离线导出；GUI 分支树与 ACP load 待补。 |
| 工具执行 | ✅ 领先 | 34 默认工具 + 硬边界 + 细粒度审批 + MCP/Hooks；`web_search` 无内置后端是短板。 |
| 交互体验 | ✅ + ⚠️ | TUI/原生 GUI/三传输齐全且内存占优；语言、桌面平台、真实模型评测是差距。 |
| provider 协议 | ✅ + ⊘ | 三协议 20 模板 + 重试/缓存/推理；云厂商长尾有意不做。 |
| 开发者体验 | ✅ 领先 | 七层配置 + skills/commands/hooks/LSP/eval；无 marketplace 是定位取舍。 |

---

## 9. 迭代建议（按投入产出排序）

1. ~~先修正确性：§1 ⚠️-2 的 grapheme 切点~~ **R20 已完成**：session 主路径统一字簇吸附，STATUS 核心引擎升至 10 分。
2. **再补开箱即用**：§3 ⚠️-1 的 `web_search`（R20 已加 doctor 提示 + 预设，仍需用户 key）、§1 ⚠️-3 的缓存可见性。
3. **然后补可信度**：§4 ⚠️-3/⚠️-4 的真实模型评测与进阶 LSP 验证，把"实现存在"升级为"效果已验证"。
4. **最后考虑体验增量**：§2 ⚠️-1 GUI 分支树、§1 ⚠️-1 模型生成式压缩摘要——收益高但改动面大，需单独排期。

所有 ⚠️ 项均保留源码落点；每完成一项，应同步更新本矩阵的标注与其 ⚠️ 表行。
