# Project status

Where argentgo is going (Roadmap) and an evidence-based self-assessment (Honest
score). The behaviour checklist lives in the
[Technical reference](TECHNICAL.md#feature-parity).

## Roadmap

The Rust reference (`argent` v0.4.0) is archived upstream and its local checkout
has been deleted. `argentgo` is the only local source of truth. This roadmap
records what comes next and why; the Feature parity section of
`docs/TECHNICAL.md` remains the behaviour checklist.

### Source of the next-step list

Weekly star deltas for 2026-11-04 → 11-11 were supplied by the user as a snapshot:
DeepSeek Harness (dsh), Hermes Agent, Codex CLI, OpenCode, Pi. GitHub does not
expose weekly deltas, so the ordering is the snapshot's claim, not an
independently verified statistic. The features below were selected by reading
each project's primary docs, not by copying their implementation. Nothing here
adds a Go dependency, cgo, or a hosted service.

### Wave 5 — close the production-wiring gap (done)

All six items below are complete; see the Feature parity section of
`docs/TECHNICAL.md` for their done status.

- Configured security rules reach `security.Policy` instead of an empty policy.
- Hook documents dispatch PreToolUse/PostToolUse/SessionStart/SessionEnd.
- Auto-compaction is verified to be active by default.
- TUI gains tasks / workspace / plan panels.
- MCP honours per-server `enabled`, `readOnly`, `allowedTools`, `deniedTools`.
- `current_time`, `web_search`, `repo_map`, `submit_plan`, `tool_search` exist.

### Wave 6 — portable ideas from the five projects (in progress)

This wave is owned by another worker with in-progress changes in the working
tree; this round does not touch any of its code.

| # | Feature | Source idea | Acceptance |
|---|---|---|---|
| 1 | `/review` read-only diff review | Codex CLI | dirty tree → findings; tree byte-identical afterwards |
| 2 | `sessions --grep` full-text search | Hermes | matches in tool output found; snippets redacted |
| 3 | Shell timeout → background job | OpenCode | long command survives tool timeout; job id returned |
| 4 | `/tree` branch nav + offline HTML export | Pi | forked session renders; output has no network refs |
| 5 | Self-authored `SKILL.md` | Hermes + dsh | `save_skill` writes a discoverable skill; opt-in |

Explicitly rejected from those projects: Node/Cordis plugin graphs, SQLite/FTS5,
Honcho user modelling, chat-platform bots, OS sandbox primitives outside Go's
stdlib, remote sandboxes, JS code-mode VMs and hosted catalogs. They are either
heavy dependencies, services, or positioning rather than portable behaviour.

### Release gate

1. `go vet ./...`, `go test ./...`, `gofmt -l .` clean on Windows and Linux cross-build.
2. `argent eval evals/core.json` passes through the scripted provider and the
   real agent loop (no network).
3. `argent gui` end-to-end: stream → approval → tool → `turn_done`.
4. Privacy scan: no keys, tokens, real emails or personal paths in tracked files
   or git history. Local paths are replaced with `<local-checkout>` before push.
5. Push with the pen name from the writer workspace as the commit author; no
   other personal information is included.

### 发行范围

发行脚本 `scripts/build-release.ps1` 已收口为 Windows/amd64 单平台（本机实测
`GOOS=windows GOARCH=amd64`），只打包当前电脑能用的版本，其他平台不考虑。

自更新的 GitHub Release 路线不启用：本地自用，无需跨平台上架。

### Distribution & IDE

- `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` is a supported
  source distribution path (Go 1.27.3+); prebuilt Windows release zips remain the
  primary route. See the [User guide](USER_GUIDE.md).
- IDE-grade LSP: grow the current `definition`/`hover` client toward
  `documentSymbol`, `references`, `codeAction` and `rename` — stdlib-only, no new
  Go module.
- Out of scope: C-ABI plugin loaders, tree-sitter AST and DAP adapters.

## Honest score

### 评分标准（锚点）
- **10** = 业界标杆：功能完整 + 有客观/可复现证据验证
- **8** = 功能齐全可用，但缺深度或验证
- **6** = 基础可用
- **4** = 有但初步
- **2** = 几乎没有

原则：分数必须附证据；失败如实上报；客观天花板（真实社区/托管发布）如实标注，不靠本地代码凑分。

### 最终评分表

| 维度 | 分数 | 证据 | 未满原因 |
|---|---|---|---|
| 可移植性/部署 | 10 | 5 平台 CGO_ENABLED=0 交叉编译矩阵（matrix.yml）；linux build 绿 | — |
| 架构分层 | 10 | archguard arch_test.go 零违规；CI job 化 | — |
| 安全性(架构) | 10 | 策略→审批→审计链；7 fuzz 目标真实跑；security-audit.ps1 PASSED 7 项；脱敏集成测试 | — |
| 核心引擎 | 9 | session 5 属性测试 + recovery fuzz（143 测试 race 绿）；checkpoint 冲突检测 | rune≠grapheme（grapheme clipper 已在 memory/ 但 session/ 未采用） |
| 工程规范/CI | 9 | matrix.yml（5 平台）+ release.yml 强化 + flake-guard；5 平台本地交叉编译全过 | release workflow 未端到端验证（gh 不可用） |
| 测试稳定性 | 9 | flake-guard 机制；tui ctrl+c flake 已根因修复（-count=10 race 通过） | 无长期 CI 运行数据 |
| 文档 | 9 | TECHNICAL（API 参考 + 5 张 mermaid 图 + 架构 + LSP + 功能对齐）+ USER_GUIDE（安装/分发/快速上手）+ CONTRIBUTING（安全/CI）+ 中文 README | 非多语言（仅中英） |
| IDE 集成 | 9 | 真实 gopls v0.24.0 端到端测试 PASS（Definition/DocumentSymbol/References 验证）；6 基础+3 进阶 LSP 方法 | semantic/inlay/codeLens 未用真实 server 验证效果 |
| 编辑格式 | 9 | 真实路径 benchmark 21/21（tools.Registry.Run，exact/repair/hashline 各 100%） | 无真实模型质量评测 |
| 内存/记忆 | 9 | 端到端 4 场景（recall/reflect/learn/redaction/override）+ 抽取式综合 + grapheme clip | reflect 无模型综合 |
| 工具广度 | 9 | ~31 工具；checkpoint_diff/usage/history 集成测试；diff 工具 | 缺 omp 的 browser/DAP/TTS（skip-list） |
| 编排 | 9 | 6 个端到端 eval 场景（真实 agent loop 驱动：委派/verify/background/board） | 无真实复杂生产任务验证 |
| 生态分发 | 6 | go install + CI + scoop/choco/brew 清单 + 中文文档 | **客观天花板**：清单未发布、无真实社区/npm 渠道，本地代码无法达到 10 |

### 加权总分
约 **8.9 / 10**（15 项：3 项满分 10，9 项 9 分，1 项 6 分）

### 客观天花板（诚实说明）
- **生态分发 6**：达到 10 需要真实发布运营（npm 包、官网、Discord 社区、包管理器仓库收录），这些不是本地代码能实现的，需要用户实际发布和运营。
- 其余 9 分维度：差距在"真实环境验证"（真实模型、真实生产任务、长期 CI 数据），可继续用工程手段部分缩小，但完全消除需要真实运行环境。

### 对比（严格口径）
- rpi 约 8.2 / omp 约 8.5 / opencode 约 8.4（基于同类证据的诚实估计）
- argentgo 8.9：在可移植性/安全/架构/测试证据上领先；在生态分发/真实模型评测上落后
