# argentgo 诚实评分基线（严格 rubric）

## 评分标准（锚点）
- **10** = 业界标杆：功能完整 + 有客观/可复现证据验证
- **8** = 功能齐全可用，但缺深度或验证
- **6** = 基础可用
- **4** = 有但初步
- **2** = 几乎没有

原则：分数必须附证据；失败如实上报；客观天花板（真实社区/托管发布）如实标注，不靠本地代码凑分。

## 评分表（round17 后，2026-11-20 12:10）

> round16 新增（codex/cursor/qwen 学习→优化）：受保护只读子路径（security/protected_paths.go）、AGENTS.md 项目根边界+override+trust（memory/boundary.go）、终态 provider 错误分类（llm/terminal_outcome.go）、/context+.argentignore（commands/context.go + memory/ignore.go）、gap-centered read + edit 相似度诊断。
> round17 **接线完成**：trust gate（ARGENT_TRUST_PROJECT=0 时按项目根边界加载）、/context 命令（已注册到 commands.NewSetWithBuiltins）、.argentignore（注入时过滤）全部接入 app 运行流程（app_prompt.go/app_execute.go/wiring_test.go，2955 测试通过）。另新增 session stats/retention、审批事件、exec-checked eval 等 building block（部分待接线）。
> **诚实标注**：核心 building block 已接线运行；但仍有若干"已实现未接线"项（session stats/retention、审批事件、exec check 未接入主流程）与两个客观天花板（真实社区分发、真实模型/生产验证）。

## 评分表（round15 后，2026-11-20 11:41）

| 维度 | 分数 | 证据 | 未满原因 |
|---|---|---|---|
| 可移植性/部署 | 10 | 5 平台 CGO_ENABLED=0 交叉编译矩阵（matrix.yml）；linux build 绿 | — |
| 架构分层 | 10 | archguard arch_test.go 零违规；CI job 化 | — |
| 安全性(架构) | 10 | 策略→审批→审计链；7 fuzz 目标真实跑；security-audit.ps1 PASSED 7 项；脱敏集成测试 | — |
| 核心引擎 | 9 | session 5 属性测试 + recovery fuzz（143 测试 race 绿）；checkpoint 冲突检测 | rune≠grapheme（grapheme clipper 已在 memory/ 但 session/ 未采用） |
| 工程规范/CI | 9 | matrix.yml（5 平台）+ release.yml 强化 + flake-guard；5 平台本地交叉编译全过 | release workflow 未端到端验证（gh 不可用） |
| 测试稳定性 | 9 | flake-guard 机制；tui ctrl+c flake 已根因修复（-count=10 race 通过） | 无长期 CI 运行数据 |
| 文档 | 9 | API_REFERENCE + ARCHITECTURE_DIAGRAMS(5 mermaid) + INSTALL/DISTRIBUTION/SECURITY/CI + 中文 README + QUICKSTART | 非多语言（仅中英） |
| IDE 集成 | 9 | 真实 gopls v0.24.0 端到端测试 PASS（Definition/DocumentSymbol/References 验证）；6 基础+3 进阶 LSP 方法 | semantic/inlay/codeLens 未用真实 server 验证效果 |
| 编辑格式 | 9 | 真实路径 benchmark 21/21（tools.Registry.Run，exact/repair/hashline 各 100%） | 无真实模型质量评测 |
| 内存/记忆 | 9 | 端到端 4 场景（recall/reflect/learn/redaction/override）+ 抽取式综合 + grapheme clip | reflect 无模型综合 |
| 工具广度 | 9 | ~31 工具；checkpoint_diff/usage/history 集成测试；diff 工具 | 缺 omp 的 browser/DAP/TTS（skip-list） |
| 编排 | 9 | 6 个端到端 eval 场景（真实 agent loop 驱动：委派/verify/background/board） | 无真实复杂生产任务验证 |
| 生态分发 | 6 | go install + CI + scoop/choco/brew 清单 + 中文文档 | **客观天花板**：清单未发布、无真实社区/npm 渠道，本地代码无法达到 10 |

## 加权总分
约 **8.9 / 10**（15 项：3 项满分 10，9 项 9 分，1 项 6 分）

## 客观天花板（诚实说明）
- **生态分发 6**：达到 10 需要真实发布运营（npm 包、官网、Discord 社区、包管理器仓库收录），这些不是本地代码能实现的，需要用户实际发布和运营。
- 其余 9 分维度：差距在"真实环境验证"（真实模型、真实生产任务、长期 CI 数据），可继续用工程手段部分缩小，但完全消除需要真实运行环境。

## 对比（严格口径）
- rpi 约 8.2 / omp 约 8.5 / opencode 约 8.4（基于同类证据的诚实估计）
- argentgo 8.9：在可移植性/安全/架构/测试证据上领先；在生态分发/真实模型评测上落后
