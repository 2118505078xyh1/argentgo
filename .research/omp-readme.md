# omp (oh-my-pi) README — 抓取于 2026-11-21，来源 raw.githubusercontent.com/can1357/oh-my-pi/v17.0.5/README.md

> 这是从 omp 官方仓库抓取的只读研究资料，供学习代理分析对比 argent-go。不属于项目源码。

**定位**：A coding agent with the IDE wired in. Fork of Pi (@mariozechner) by Can Bölük，TypeScript + Rust core，MIT。40+ providers / 32 built-in tools / 14 LSP ops / 28 DAP ops / ~55k 行 Rust core。

## 关键能力（与 argent-go 对比的重点）

1. **Code execution w/ tool-calling**：持久化 Python + Bun worker，两个 kernel 都能通过 loopback bridge 回调 agent 自己的工具（read/search/task）。"在 cell 里用 tool.read 加载 CSV，用 JS 画图，不离开 cell"。
2. **LSP wired into every write**：rename 走 workspace/willRenameFiles，re-export/barrel/alias import 在文件移动前更新。
3. **DAP 真实调试器**：attach lldb/dlv/debugpy，断点/步进/栈/变量，不是 print 调试。
4. **Time-traveling stream rules (TTSR)**：规则平时休眠，模型偏离时 regex 命中 → 流中途 abort → 注入 rule 作为 system reminder → 从同点重试。注入能存活于 compaction。"course-correction without paying context tax on every turn"。
5. **First-class subagents**：task 分发到隔离 worktree，每个 worker 自己的工具面，结果 schema-validated object，父直接读，无 prose 解析/无 merge 冲突。
6. **Advisor 第二模型**：reviewer 模型读主 agent 每一轮，注入 inline notes（concern/blocker），独立 context。
7. **/collab**： live session 上 relay，链接+QR，多人协作（读写配对/只读观看），frames 客户端加密 relay 看不到 key。
8. **web_search → read 管道**：18 providers，URL 直接交给 read，arxiv PDF/GitHub/SO 转结构化 markdown 保留 anchors。
9. **原生实现**：ripgrep/glob/find 在进程内，bash 是持久 session（brush），无 fork-exec，跨平台无 WSL。
10. **/review 优先级**：P0-P3 + confidence score，并行 reviewer subagents。
11. **hashline edit**：行锚点哈希，模型指 anchor 不重打整行，空白/字符串找不到循环消失，stale-anchor 提前拒绝。Grok4Fast -61% tokens。
12. **GitHub is filesystem**：PRs are paths，read 统一接口。
13. **Hindsight memory**：retain/recall/reflect，每会话压缩成 mental model 下一轮首 turn 加载，project-scoped。
14. **ACP editor-driven**：Zed 等编辑器里读写 buffer 走 editor save path。
15. **继承已有工具配置**：读 .claude/.cursor/.windsurf/.gemini/.codex/.cline/.github/copilot/.vscode 的 rules/skills/MCP，无迁移。
16. **omp commit**：git_overview/git_file_diff/git_hunk，把无关改动拆成原子提交按依赖排序，cycle 拒绝，源文件优先，锁文件排除。
17. **12 internal schemes**：pr:// / issue:// / agent:// / skill:// / rule:// 等，在 FS-shaped 工具里透明解析，"read pr://1428 和 read src/foo.ts 同形"。
18. **冲突解决**：每个 merge conflict 是 URL，写 @theirs/@ours/@base 到 conflict://N。
19. **ast_edit preview**：_(proposed)_ card + 替换数，staged，resolve 接受，atomic all-or-nothing。
20. **browser / Electron app**：Puppeteer + stealth，驱动任意 Electron app（如 Slack）。

## 32 工具（命名空间与 read/bash 同级）
- 文件搜索：read(文件/目录/归档/SQLite/PDF/notebook/URL/internal :// schemes)、write、edit(hashline)、ast_edit、ast_grep(50+ tree-sitter)、search、find
- 运行时：bash(PTY/后台)、eval(持久 Python+JS cells 共享 prelude + tool re-entry)、ssh
- 代码智能：lsp(14 ops)、debug(28 DAP ops)
- 协调：task(并行 subagents/worktree隔离)、hub(消息/wait/cancel后台job)、todo(phase tracking)、ask(结构化提问)
- 外部：browser、web_search、github、generate_image、inspect_image、tts
- 记忆状态：checkpoint、rewind、retain、recall、reflect
- 杂项：resolve(接受/丢弃 queued preview)
- setting-gated 默认关：github/inspect_image/tts/checkpoint/rewind/retain/recall/reflect

## 25+ web_search providers
auto(chain)/perplexity/gemini/anthropic/codex/xai/zai/exa/tinyfish/jina/kagi/tavily/firecrawl/brave/kimi/parallel/synthetic/searxng/duckduckgo(no key)/bing(no key)/yahoo/startpage/google/ecosia/mojeek/public。specialized handlers：code hosts/registries/research(arxiv)/forums/docs。安全数据库 NVD/OSV/CISA KEV。页面转 markdown 保 link structure。

## Provider routing
40+ providers，roles：default/smol/slow/plan/commit，四个旋钮：custom providers(yaml)/fallback chains/path-scoped models/round-robin credentials。7 种 wire：openai-completions/openai-responses/openai-codex-responses/azure-openai-responses/anthropic-messages/google-generative-ai/google-vertex。

## 4 入口
interactive TUI / -p one-shot / --mode rpc(NDJSON) / acp。SDK @oh-my-pi/pi-coding-agent 嵌入 Node。

## Prompt controls（magic keywords，lowercase standalone）
ultrathink / orchestrate / workflowz — 只在 prose 触发，不在 code span/fence/XML/identifier/path 里。

## ~55k 行 Rust core 模块
shell(3700, brush)、grep(1900)、keys(1490 Kitty protocol)、text(1450)、summary(1040 tree-sitter)、ast(1000 ast-grep)、fs_cache(840)、highlight(470)、pty(455)、glob(410)、workspace(385)、iso(245 workspace isolation APFS/btrfs/zfs/reflink/overlayfs)、prof(240)、ps(195)、clipboard(80)、tokens(65 tiktoken)、sixel(55)、html(50)。
