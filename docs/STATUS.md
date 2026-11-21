# 项目状态

argentgo 要往哪里去（Roadmap），以及一份基于证据的自我评估（Honest score）。行为清单在 [Technical reference](TECHNICAL.md#feature-parity) 中。

用户可感知能力与 Claude Code / OpenCode / Crush / Codex 的逐维对标见 [Capability matrix](CAPABILITY_MATRIX.md)；本文档 8.9 分评估的是架构/安全/测试证据，两者口径不同。

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

### 发布门禁

1. `go vet ./...`、`go test ./...`、`gofmt -l .` 在 Windows 和 Linux 交叉编译上都干净。
2. `argent eval evals/core.json` 能通过脚本化 provider 和真实 agent 循环（不联网）。
3. `argent gui` 端到端：stream → 审批 → 工具 → `turn_done`。
4. 隐私扫描：已跟踪文件和 git 历史中没有密钥、token、真实邮箱或个人路径。推送前本地路径替换为 `<local-checkout>`。
5. 用 writer workspace 中的笔名作为提交作者推送；不包含其他个人信息。

### 发行范围

发行脚本 `scripts/build-release.ps1` 已收口为 Windows/amd64 单平台（本机实测
`GOOS=windows GOARCH=amd64`），只打包当前电脑能用的版本，其他平台不考虑。

自更新的 GitHub Release 路线不启用：本地自用，无需跨平台上架。

### 分发与 IDE

- `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` 是受支持的源码分发路径（Go 1.27.3+）；预编译的 Windows 发布 zip 仍是主要途径。见 [User guide](USER_GUIDE.md)。
- IDE 级 LSP：把当前的 `definition`/`hover` 客户端扩展到 `documentSymbol`、`references`、`codeAction` 和 `rename`——只用标准库，不新增 Go 模块。
- 超出范围：C-ABI 插件加载器、tree-sitter AST 和 DAP 适配器。

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
| 核心引擎 | 10 | session 属性测试 + recovery fuzz（race 绿）；checkpoint 冲突检测；R20 起 session 5 处截断统一走 textutil 字簇边界吸附（保持 rune 预算、默认路径逐字节不变） | 无（字簇边界为 UAX#29 务实子集，非完整 Unicode 合规） |
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
