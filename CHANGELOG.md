# 更新日志

## v0.8.0 — 2026-11-20

针对 goose、aider、gptme、Crush、pi、opencode、dsh 和 Codex 做了多轮学习与加固。Go 代码达到 20 万行（wc -l，含测试），仍然只依赖标准库，MyGo 是唯一的原生壳依赖。亮点：

**安全与审批（审计驱动）**
- 命令守卫会递归地从 `bash -c` / `sh -c` / `bash -lc` 包装中提取字面量载荷（限制深度，失败即关闭）——堵住了一类通过去引号遮蔽危险命令来绕过 shell 守卫的漏洞。
- 路径策略识别 `path_a`/`path_b`、`file_path` 以及 `files`/`paths` 数组，因此 `diff` / `context_pack` 无法再绕过 `denyPaths`。
- 单词命令上的多重重定向（`echo x >/etc/passwd>/tmp/x`）现在会检查每一个重定向目标，而不只是最后一个。
- 当审批通道不可用（headless / nil）时，不再伪造"操作员拒绝"的授权错误；审计记录 `unavailable`，也不会告诉模型"不要重试"。
- 凭据脱敏现在对 HTTP 头 / auth scheme 名称不区分大小写（`AUTHORIZATION:`、`BEARER`），doctor 输出也会去掉 URL 里的 userinfo。
- 策略解析对无效决策会给出 `Issue`，而不是静默丢弃规则；配置指纹包含 SHA-256 内容哈希，因此能检测出大小相同/修改时间相同的重写。
- 审批分支中的 nil `*Chain` 不再 panic。

**agent 循环与正确性**
- Anthropic / Responses 客户端生成的工具调用 ID 不再与 provider 提供的 ID 冲突（后者可能导致整批调用被拒绝）。
- 重试不再重放带有杂散 `x-should-retry` 头的 2xx 成功响应；空的 SSE `data:` 保持连接事件会被跳过，而不是让流失败。
- provider 报错 / 取消时会捕获部分助手文本，作为附加的 `EventPartial` 发出，这样 UI 和恢复都能看到实际说了什么。
- MCP 列表会遵循 `nextCursor` 分页；较大的 MCP 工具结果可以溢出到 artifacts；stdio 启动错误会携带捕获到的服务端 stderr。
- `--tokens --session` 报告跨多次恢复的累计用量；代码模型支持 FIM 补全。
- 去重 / 无进展检测、轮次预算提示和可选的无进展停止，可用于加固循环。

**上下文与缓存**
- 输出语言偏好（`--locale`）现在会在 system prompt 中生效；WCAG 感知的徽章颜色，以及对工具输出的 ANSI/回车符清理。
- repo-map token 估算支持 CJK；todo 写入按 rune 预算控制；结构化摘要投影、导出时间/类型过滤和 YAML 导出。
- LSP 诊断适配器、结构化 MCP schema 归一化与能力门控、会话树成本统计与压缩修复。

**性能**
- 会话列表信任新鲜的 sidecar（大小 + mtime），不再重新读取并校验每份 transcript：100 个会话时约快 16 倍，500 个时约快 13 倍。
- 新增 `internal/tokenize` 包，让 CJK 感知的 token 估算只有一个定义（以前是三份手工同步的副本），并带 ASCII 快速路径；token 估算快 5-13 倍，Trim 维护后缀 token 合计而不是重新扫描尾部（长对话约快 25 倍）。
- Unix 上的会话追加每次写入只做一次 stat，而不是两次。
- bash 体积提示由原始命令输出计算，因此游程压缩后的输出不会再隐藏自身的上下文成本（修复了 `go test ./...` 中最后一个失败的测试）。

**工程**
- 结构化版本端点 `/v1/version`，以及关停时的 health 503。
- doctor 环境探测、审计查询标志、nofollow 安全的导出写入、断管容错和分页决策钩子。
- 会话的许多小修复（fork 标题、压缩摘要/保留、大写扩展名 ID）、导出 JSON 合法性、TUI 缓存与渲染。

## v0.7.0 — 2026-11-20

针对 qwen-code、Cursor、gptme、pi、oh-my-pi、Roo-Code、dsh、OpenHands、Codex、Crush、goose、kilocode、aider 和 opencode 做了十轮学习与加固。仅依赖标准库的 Go；MyGo 仍是唯一的原生壳依赖。亮点：

**安全与审批（审计驱动）**
- `--yolo` / `approval=auto` 不再静默执行修改性 shell 命令：它们会回退到交互式确认（只认 y/yes，没有批量的 "All"）。
- 新增威胁模式检测器（强制 `rm`、`sudo`/`env`/`trap` 包装、`curl|bash`、反弹 shell、编码执行），带 `guardrails` shadow/enforce/off 模式，未知情况一律失败即关闭。
- 拒绝反馈会作为模型可读的工具错误呈现；checkpoint 撤销在写入前会重新检查每个目标（针对并发编辑做 CAS）。
- checkpoint 抓取按 (path,size,mtime,mode) 缓存文件哈希，避免每轮重新读取未变化的文件。

**agent 循环与正确性**
- `InvalidArgBreaker` 接入了工具分发：连续三次无效参数错误会结束本轮；成功 / 跨轮重置会重新武装它。
- 结构化恢复分类（clean / interrupted prompt / interrupted turn / degraded history），带自动继续与确认决策。
- 用户声明的轮次预算（`+500k`、`use 2m tokens`）从 prompt 中解析，注入的块会被剥离，然后钳制并应用到预算上。
- 重复调用检测现在使用规范化后的参数；trim 保留 0.7 的目标比例余量，并按 rune 安全地裁剪大工具结果的头尾。
- 工具 panic 会被恢复成清理过的错误，因此一轮运行不会被打断。

**上下文与缓存**
- system prompt 拆成缓存稳定的静态前缀和每轮动态的用户消息（cwd / thinking / schema），并由项目信任度门控。
- prompt-cache 预热决策、schema 编译缓存，以及 10% 的压缩节省门槛。压缩归档可以用字节范围引用写入以便精确回放；大 Go 文件支持 outline 模式。
- MCP 分级发现超时 + 瞬时错误分类；分级 inbox 缓冲组件；PowerShell 输出固定为 UTF-8；PTY 终端查询过滤。

**工程**
- 配置热重载元数据（`Changed`/`Reload`/`Generation`）、多语言校验命令、schema 展平（`flattenAllOf`）作为可选投影，以及输出语言的闭集校验。

## v0.6.0 — 2026-11-20

针对 charmbracelet/crush、block/goose、Aider、opencode、Cline、fzf、glamour 和 bubbletea 做了十六轮学习与加固，并做了一次完整的独立代码评审。仅依赖标准库的 Go；MyGo 仍是唯一的原生壳依赖。亮点：

**TUI 与交互**
- 会话面板模糊搜索（Ctrl+L）：评分式子序列匹配、fzf 查询语法（`^` 前缀、`!` 取反、`|` 或、精确/词边界）、相关性排序和字符级匹配高亮。
- Markdown 表格（两遍布局）、词级 diff 高亮（只高亮变化的 token），以及流式 Markdown 边界缓冲（不会在 `**`/代码围栏中间闪烁，也不会在轮次结束时丢文本）。
- 按字素簇计算宽度（ZWJ 家族、旗帜、肤色）、LRU 风格的条目缓存、带省略号的按键列表帮助栏、单行 thinking 指示器、活动摘要、带阈值颜色的上下文用量进度条。

**agent 循环与正确性**
- 单飞 prompt，Abort 所有权无竞态；受监视的 job 标记修复了长期的退出码 `-1` 竞态（1500 次压力测试干净通过）。
- 错误追踪器带可注入的恢复指引；恢复时工具配对归一化；gemini/moonshot 模型的 schema 投影。
- 压缩现在会保留非首行的下一步 / 失败 / 改动文件，轮次中途压缩会带上工具调用 ID，因此不会留下孤立的工具结果。前缀哈希摘要缓存避免重复摘要。

**安全（审计驱动）**
- 修复了跨读/写、MCP 路由和审批显示中通过符号链接/Windows junction 逃逸工作区的问题（通过 GetFinalPathNameByHandle 解析真实路径）。
- PowerShell 的 `script` 别名无法再绕过 `denyCommands` / 命令规则。
- 凭据脱敏覆盖主流密钥格式（`sk-ant-…`、`sk-proj-…`、`Authorization: Bearer/Basic …`、URL userinfo），同时作用于 LLM 错误和审计日志。

**LLM 与 provider**
- chat completions 现在会重试瞬时失败（重试分类与硬配额快速失败生效），解析多行 SSE `data:`，避免生成的工具调用 ID 冲突，并校验格式错误的工具参数。
- `ActionableError` 分类在 CLI 中呈现，并给出恢复性的下一步。

**工程**
- CI 增加 `-race` 和 `GOOS=linux` 交叉编译门禁；维护者与 API 参考文档；eval 场景的基准基线与 golden 快照。

## v0.5.0 — 2026-11-20

针对 opencode、DeepSeek Harness、pi (rpi) 和 oh-my-pi (omp) 做了六轮学习与加固。所有改动都是仅依赖标准库的 Go；桌面壳仍是 MyGo。亮点：

**agent 循环与正确性**
- 输出上限续写：文本回复被长度截断时，会注入一条"把工作拆成更小的片段"消息，并在连续两次截断后停止（以前会静默报告为完成）。
- 类型化重试分类：provider fallback 现在使用 `llm.ClassifyRetryError` 和 `IsRetryableStatus`，而不是脆弱的字符串匹配。
- 取消操作现在会记录一条终态的 "Operation aborted" 助手消息。
- 请求前/工具前的持久化屏障接入了循环，并注册到 `Session.Sync()`。
- 工具调用意图（`i`）在执行前被提取并出现在事件上。
- 轮次边界事件上暴露非消费中的待处理消息计数。

**会话持久化**
- `Session.Sync()` 在模型请求和工具分发前 flush。
- 尾部撕裂恢复：只修复 LAST JSONL 行上的语法错误（原子发布前缀）；schema 错误或文件中部损坏一律失败即关闭。
- 工具配对安全的压缩切点：调用永远不会与其结果分离。
- 按字节限制的 transcript 分页（`ReadPage`）绝不会截断一条记录。
- 为活跃子目录发现嵌套的 `AGENTS.md` / `CLAUDE.md`。

**安全与工具**
- 审批提示现在可以取消 context，并按 rune 安全地渲染多行参数。
- 从子进程中剥离宿主机私有环境变量。
- ANSI/CR shell 输出清理器；跨读取块的增量 UTF-8 解码。
- 为 Windows 宿主注册 PowerShell 工具。
- 集中式工具结果归一化器，处理格式错误/超大的输出。

**prompt cache**
- 会话尾部缓存断点，带经过校验的 TTL 档位。

**并发与 CI**
- 修复了已取消审批的 reader 泄漏，它可能吞掉下一个 prompt。
- 压缩证据采集前对事件切片做防御性快照。
- CI race 门禁移到 ubuntu-latest（gcc 可靠）；移除了过时的 web UI 任务。

## v0.4.0 — 2026-11-19

- **移除了浏览器前端；native MyGo 现在是唯一的桌面前端。** 删除了 `ui/`（SolidJS + Vite）、`internal/gui/`（HTTP+SSE 桥）、`internal/launcher/`（Edge/Chrome `--app`）、`scripts/build-ui.ps1` 以及 web 运行器/审批适配器。`argent gui` 现在始终启动 `argent-native.exe`。
- 简化了 `cmd/argent/gui.go`，只做引擎解析 + 原生前端启动；移除了 `--web`。发布 zip 与构建脚本不再构建/嵌入 web UI。
- 文档（README、AGENTS 和合并后的 docs）更新为原生-only 架构。核心包仍然只用标准库；MyGo 是唯一的第三方依赖，且仅限于 `native/`。

## v0.3.1 — 2026-11-19

源自研究 rpi coding-agent 运行时得出的正确性修复：

- 拒绝来自长度截断回复的工具批次：不完整的参数绝不会被执行；每个调用都会收到一条有序的错误结果。三个 wire 客户端现在都把 provider 的 finish reason 附到 `llm.Message` 上。
- 整批拒绝带有重复或空工具调用 ID 的批次——当结果无法关联时，不执行任何调用。
- 工具返回后抑制迟到的流式工具进度，这样杂散的 goroutine 就无法再往 transcript 追加过期行。
- 兜住 hook 回调中的 panic：发生 panic 的回调会变成一条带其调用 ID 的结构化错误，运行继续。

## v0.3.0 — 2026-11-19

- **前端全面迁移到 MyGo 原生客户端。** `argent gui` 现在默认启动 `argent-native.exe`：纯 Go 原生 UI（单进程，约 76 MB 工作集，空闲 CPU 0%），没有 WebView2。它通过已有的 stdio `--rpc` 传输驱动引擎，因此核心包保持只用标准库。
- 原生客户端用 MyGo 的 `ui` 渲染侧栏、消息列表、工具卡片、流式 Markdown 回复、审批、按键帽以及深色/浅色主题。
- 移除了 Wails 和实验性的 Win32 syscall 窗口；唯一的第三方 Go 依赖是 MyGo，且仅限于 `native/` 桌面壳。
- 发布 zip 现在同时包含 `argent.exe`（引擎/CLI）和 `argent-native.exe`。`argent gui --web` 保留浏览器 UI 作为后备。

## v0.2.1 — 2026-11-19

- 桌面壳从实验性的自绘 Win32 窗口切换到 **Wails v3 + WebView2**（`argent gui` 默认）。它加载已有的本地 HTTP+SSE 桥，因此前端与所有 IPC 保持不变；WebView2 提供成熟文本选择、Markdown 渲染和输入处理。
- `--native` 保留旧的 syscall 窗口供参考；`--web` 保留 Edge/Chrome `--app` 路径。Wails 入口仅 Windows 可用，其他平台有桩实现。
- 消除了间歇性启动挂起和窗口离屏/最小化问题：WebView2 窗口位置由 Wails 处理，exe 仍是 GUI 子系统二进制，所以不会出现黑色控制台。
- 核心包仍然只用标准库；桌面壳有意引入 Wails 作为唯一的第三方依赖（`go.mod` 记录了该例外）。

## v0.2.0 — 2026-11-19

- **原生 GUI** 在 Windows 上取代浏览器壳：纯 Win32 syscall GDI 绘制，无 WebView、无本地 HTTP 服务、无 token。`argent gui --web` 保留可选的 SolidJS 界面。
- 移植了可移植的 OpenCode 模式：沙箱/拒绝过滤的工具目录、轮次上限处的无工具最终回答、类型化密钥脱敏、多环境凭据链、重试分类、配置诊断 / `{env:}` / `{file:}` 替换 / JSONC、权限通配符规则，以及有界的 context/session/TUI 内存占用。
- GUI 视觉打磨：语义化配色、居中的 720px 阅读列、工具行状态、闪烁的流式光标、审批按键帽、双缓冲绘制。
- 修复了 80ms 动画计时器导致的绘制循环挂起，并去掉了黑色控制台窗口：发布二进制构建为 GUI 子系统可执行文件，同时从已有终端运行时的 CLI 输出仍然可用。
- 通过会话 checkpoint 在两个前端接入撤销/重试（`Ctrl+Z` / `Ctrl+Y`）。

## v0.1.2 — 2026-11-19

- 项目从 `argent-go` 改名为 `argentgo`；Go 模块路径以及所有文档、脚本和协议标识符都采用新名称。
- 采用 Apache-2.0 许可（见 `LICENSE`）。
- checkpoint/撤销/重试：带 SHA-256 的文件清单、冲突安全的恢复和重试时分叉，接入 agent 循环与 CLI（`--undo`、`--retry`、`--title`）。
- Anthropic / OpenAI chat / Responses 的 prompt cache，带 cached-token 统计和 `--no-prompt-cache`。
- 失败、取消和流错误的尝试会记录在 JSONL 中，但不进入模型历史；会话标题在本地生成并脱敏。
- 一键 Windows 发布（`scripts/publish-win.ps1`）会真正的发布 GitHub Release，附带 Windows zip 与校验和，而不只是打标签。
- TUI/GUI：撤销/重试操作、尝试徽章、cached-token 计数、命令模板、SSE 重连和无障碍打磨。

## v0.1.1 — 2026-11-19

- 修复 GUI 壳：页面现在在初始 token URL 之后设置一个仅限 GET 的资源 cookie，这样内嵌的 JS/CSS 能加载，而不是被 401 拒绝。
- 前端性能：合并流式增量，在 hidden/pagehide 时 flush；聊天步骤计数为 O(n)；工具更新保留无关行的引用。
- 前端打磨：更紧凑的深色/浅色主题、更清晰的焦点状态、更轻的阴影，以及减少动态效果支持。没有新增前端依赖。

## v0.1.0 — 2026-11-19

首个 Go 版本：已冻结的 Rust `argent` 0.4.0 的仅标准库、无 cgo 重写；逐组件状态见 `docs/TECHNICAL.md`（Feature parity 一节）。

### 运行时

- agent 循环，支持流式输出、工具批次（默认顺序执行，只读调用并行）、审批门禁、重复守卫、上下文裁剪、token/成本预算、steering 和嵌套子代理。
- provider：OpenAI chat completions、OpenAI Responses、Anthropic Messages，均支持 SSE 流式输出、工具调用、thinking 增量和用量统计。共享重试逻辑支持 `Retry-After`。
- provider 模板：anthropic、openai、deepseek、moonshot、zhipu、qwen、ollama、lmstudio 和 openrouter。

### 工具

- `read`、`write`、`edit`、`multi_edit`、`bash`、`ls`、`glob`、`grep`、`find`、`todo`、`web_fetch`、`git_status`；注册表层级的 guard 钩子和每次运行的输出上限。

### 安全

- 路径/命令/工具策略、交互式与 `never` 审批器、按工具的会话缓存、沙箱模式（`read-only`、`workspace-write`、`danger-full-access`），以及脱敏的每日 JSONL 审计日志。

### 会话与配置

- JSONL 会话树，支持恢复、fork 和压缩；分层配置（home → project → `argent.json` → env → flags），带 profiles。

### 界面与入口

- CLI，含 prompt 模式、`sessions`、`doctor`、`login`、`eval`、`config`。
- 无头传输：`--serve`（HTTP + SSE）、`--rpc`（stdio 上的 JSON-RPC）、`acp`。
- 全屏 TUI：流式 transcript、轻量 markdown 渲染、输入编辑器、审批弹窗。
- 桌面壳：由 Go 二进制提供的 SolidJS UI，在 WebView2 应用窗口中打开（无 Tauri、无 Rust）。

### 集成

- MCP 客户端，支持 stdio 和 HTTP/SSE；带 SHA-256 信任的 hook 文档；插件清单（`.zcode-plugin`、`.claude-plugin`、`.codex-plugin`）；LSP 客户端；skills、斜杠命令和项目记忆注入。
- 自动化调度器（cron/interval、daemon、运行日志）和脚本化工作流。
- OAuth PKCE / 设备码登录，带本地 token 存储；GitHub 自更新，带校验和验证与原子替换。
- eval 框架，移植了 `evals/core.json` 场景。

### Wave 6 新增

- `/review` 斜杠命令，用于只读的、结构化代码评审。
- `sessions` 全文 grep、分支树和 JSON/JSONL 导出。
- 后台 shell job，可提升到前台，以及只读的 `jobs` 面板。
- skill 可由 agent 编写，并在后续轮次被发现。
- GUI 对每次修改性工具调用只显示一条审批，而不是重复多条。

### 未移植

- 原生 C-ABI 插件加载器（Go 使用进程/MCP 插件）。
- tree-sitter AST 和 `repo_map`（改用 LSP 符号）。
- Rhai 脚本插件。
- 桌面控制工具、遥测、市场。
