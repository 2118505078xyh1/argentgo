<div align="center">

# argentgo

**一个把想法直接变成代码的小型 Agent 运行时。**

单文件、零依赖、离线优先。流式对话、真实工具调用、可回放的会话树——
一个二进制，从命令行到桌面窗口都能用。

[下载 Windows 版](https://github.com/2118505078xyh1/argentgo/releases/latest) ·
[快速开始](#快速开始) ·
[特性](#为什么选-argentgo) ·
[文档](#文档)

</div>

---

## 安装

完整安装指南见 [docs/USER_GUIDE.md](docs/USER_GUIDE.md#install)。最快路径（Windows 预编译包）：从
[最新 Release](https://github.com/2118505078xyh1/argentgo/releases/latest) 下载
`argentgo_<version>_windows_amd64.zip` 与 `checksums.txt`，校验后解压运行：

```powershell
sha256sum -c checksums.txt                              # Git Bash；PowerShell 用 Get-FileHash
Expand-Archive .\argentgo_<version>_windows_amd64.zip -DestinationPath .
.\argentgo.exe --version
```

解压后让 `argentgo-native.exe` 与 `argentgo.exe` 保持在同一目录（`argentgo gui` 需要它）。

从源码安装（`go install`，需 Go 1.27.3+）：

```powershell
go install github.com/2118505078xyh1/argentgo/cmd/argent@latest
```

## 为什么选 argentgo

- **单文件，零负担** — 纯 Go 标准库，**无第三方依赖、无 cgo**。下载一个 `.exe` 就能跑，不用装 Node、Python 或运行时。
- **三种协议** — OpenAI Chat Completions、OpenAI Responses、Anthropic Messages，都支持流式输出、工具调用、推理增量和 token 统计。
- **工具真的能干活** — 读写文件、跑 shell、搜索、补丁、后台任务、MCP、LSP、hooks、子代理，全部走同一条安全链。
- **会话是数据，不是黑盒** — 每轮都是 JSONL 记录，连成可 fork 的树。`/undo` 按 SHA-256 清单精确回滚，`/retry` 从检查点分叉重跑，失败的尝试也不会污染模型历史。
- **省钱** — 稳定的 system prompt / 工具 schema 支持 prompt cache，usage 区分 cached / fresh，一键 `--no-prompt-cache` 关闭。
- **隐私优先** — 密钥和 token 落盘前脱敏；本地生成会话标题不联网；桌面窗口由 MyGo 原生绘制，不起 HTTP 服务、不带浏览器/WebView。
- **多界面** — 命令行一次性问答、全屏 TUI、**MyGo 原生桌面窗口**（单进程约 76 MB、空闲 0% CPU，无 WebView），以及 HTTP/SSE、JSON-RPC、ACP 三种无头传输。TUI 支持 Ctrl+G 调用 `$EDITOR` 编辑长提示，工具调用可实时预览流式参数。
- **安全默认** — `--yolo` / `auto` 审批下，修改性 shell 命令仍需真人确认；内置危险命令检测（`rm -rf /`、`curl | bash`、反弹 shell 等）支持 `ARGENT_GUARDRAILS=shadow|enforce|off` 三段开关。策略拒绝会回灌给模型供其修正重试。
- **双语** — zh-CN / en-US 文案嵌在二进制里，无需外部 locale 文件。

体积是实测数字：无头运行工作集约 **10 MB**，二进制约 **10.6 MB**。

## 下载与安装

Windows 用户直接下载 [最新 Release](https://github.com/2118505078xyh1/argentgo/releases/latest) 的
`argentgo_*.zip`，解压即用的 `argentgo.exe`。校验：

```powershell
sha256sum -c checksums.txt      # 哈希也写在了 Release Notes 里
```

本地构建（需要 Go 1.27.3+，桌面壳使用 MyGo）：

```powershell
go build -o argentgo.exe ./cmd/argent
```

## 快速开始

```powershell
# 配好 key（也支持 ARGENT_API_KEY 环境变量或配置文件）
$env:ARGENT_API_KEY = "sk-..."

argentgo "把 src 里的 unwrap 都找出来"     # 一次性提示，打印回答
argentgo --mode json "列出目录"            # 严格 JSONL：事件 + 结果
argentgo tui                                # 全屏终端 UI（SSH 也能跑）
argentgo gui                                # Windows 原生聊天窗口（MyGo，无浏览器）
```

想换成国产模型？内置模板直接切：

```powershell
argentgo --provider deepseek  "帮我写个函数"
argentgo --provider qwen      "解释这段代码"
argentgo --provider anthropic "review 一下这个改动"
```

`argentgo gui` 在 Windows 上默认启动 **MyGo 原生聊天窗口**（`argentgo-native.exe`）：
纯 Go 绘制、无 WebView、单进程约 76 MB、空闲 0% CPU，视觉按 Claude / Codex 风格
（暖灰底、珊瑚色强调）。它通过已有的 `--rpc` 传输驱动 `argentgo.exe` 引擎，核心包保持
stdlib-only；只有桌面壳这一层依赖 MyGo。

发布包同时包含 `argentgo.exe`（引擎/CLI）和 `argentgo-native.exe`（原生前端）。
浏览器前端已移除，桌面只走原生窗口。详见 [docs/TECHNICAL.md](docs/TECHNICAL.md#frontend-decision)。

其他入口：

```powershell
argentgo --serve --serve-token <token>      # HTTP + SSE
argentgo --rpc                              # stdio 上的换行分隔 JSON-RPC
argentgo acp                                # Agent Client Protocol 服务端
```

## 它会做什么

```
你：把 login 的 token 校验抽成函数，并补测试

argentgo：
  ✓ 读取 internal/auth/login.go
  ✓ 编辑：抽出 validateToken(...)
  ✓ 写入 internal/auth/login_test.go
  ✓ 运行 go test ./internal/auth  →  全部通过
  ✓ 提交前的检查点已保存（SHA-256 清单）

你：/undo                      → 只回滚这一轮改过的两个文件
你：/retry                     → 从检查点分叉，换个思路重跑
```

每一步都是可观察事件，TUI/GUI 实时渲染；会话可用 `sessions --grep / tree / export` 检索与导出。

## 会话与检查点

会话是 JSONL，一行一条记录，用 `id` / `parent` 连成树。压缩（`compact`）保留改动文件、
失败与下一步，不丢上下文。

| 命令 | 作用 |
|---|---|
| `sessions --grep <词>` | 在所有会话里搜索（含脱敏后的尝试片段） |
| `sessions tree` | 查看 fork / retry 关系树 |
| `sessions export` | 导出为离线 HTML |
| `argentgo --undo --session <id>` | 从最近检查点恢复文件（冲突编辑会拒绝覆盖） |
| `argentgo --retry --session <id>` | 从检查点分叉重跑 |
| `argentgo --title "名字"` | 覆盖自动生成的会话标题 |

## 配置

分层，低 → 高：编译默认值 → `~/.argentgo/config.json` → `<cwd>/.argent/config.json`
→ `<cwd>/argent.json` → profile → `ARGENT_*` 环境变量 → CLI 标志。
`-c/--config key=value` 在配置文件层生效（点分键嵌套），高于环境变量、低于 CLI 标志。

| 设置 | 标志 | 环境变量 | 默认值 |
|---|---|---|---|
| 模型 | `-m`, `--model` | `ARGENT_MODEL` | `gpt-4o-mini` |
| provider 模板 | `--provider` | `ARGENT_PROVIDER` | 按协议推断 |
| 协议 | `--protocol` | `ARGENT_PROTOCOL` | `openai` |
| base URL | `--base-url` | `ARGENT_BASE_URL`, `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| API key | `--api-key` | `ARGENT_API_KEY`, `OPENAI_API_KEY` | 空 |
| locale | `--locale` | `ARGENT_LOCALE` | `en-US` |
| 沙箱 | `--sandbox-mode` | `ARGENT_SANDBOX_MODE` | `workspace-write` |
| 审批 | `--approval` | `ARGENT_APPROVAL` | `ask` |
| 存储目录 | — | `ARGENT_STORAGE` | `~/.argentgo` |
| 关闭 prompt cache | `--no-prompt-cache` | — | 关 |

内置模板：`anthropic`、`openai`、`deepseek`、`moonshot`、`zhipu`、`zai`、`bigmodel`、
`qwen`、`minimax`、`openrouter`、`ollama`、`lmstudio`、`local`，另有别名（`claude`、`kimi`、
`glm`、`grok` 等），可在配置文件 `providers` 块覆盖或新增。

## 工作区安全模型

这不是操作系统级隔离；要强隔离请把 argentgo 放进容器。

- 每个文件工具都把路径解析到工作区内，`..` 和绝对路径逃逸一律拒绝；符号链接不能绕过白名单。
- 修改类工具走同一条链：**策略（硬门禁）→ 审批 → 脱敏审计日志**。策略拒绝直接结束运行；
  审批拒绝变成模型可读的工具错误。`--yolo` 等价于自动批准。
- 沙箱：`read-only` 移除修改类工具；`workspace-write`（默认）保留边界；
  `danger-full-access` 移除边界。`--add-dir` 扩展可写根目录。
- 审计写在 `<storage>/audit/<day>.jsonl`，密钥、token 在落盘前脱敏。

## 文档

- [安装与上手](docs/USER_GUIDE.md#install) · [架构](docs/TECHNICAL.md#architecture) · [架构图](docs/TECHNICAL.md#diagrams)
- [前端](docs/TECHNICAL.md#frontend-decision) — MyGo 原生窗口（唯一桌面前端）
- [功能对齐](docs/TECHNICAL.md#feature-parity) — 与冻结的 Rust 版逐项对照
- [路线图](docs/STATUS.md#roadmap) · [诚实评分](docs/STATUS.md#honest-score) · [更新日志](CHANGELOG.md)

## 开发与测试

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # Go 不在 PATH 时
go build -o argentgo.exe ./cmd/argent
go test ./...
go vet ./...
gofmt -l .                        # 必须无输出

go build -o argentgo-native.exe ./native   # 原生桌面前端
```

一键发布 Windows（需要带 `repo` 权限的 GitHub token）：

```powershell
powershell -File scripts/publish-win.ps1
```

它会校验版本 / tag / CHANGELOG，跑全部门禁，构建单个 Windows zip + checksums，
推送并发布真正的 GitHub Release（不是只打标签）。只验证 Windows 主机版本。

## 贡献

欢迎 bug 报告、功能提议、文档修正和代码 PR。

- **发现问题** → [开 Issue](https://github.com/2118505078xyh1/argentgo/issues/new/choose)
  （通用 Issue 模板覆盖 Bug / 功能 / 文档，提交日志请脱敏）
- **提问或讨论** → [Discussions](https://github.com/2118505078xyh1/argentgo/discussions)
- **想贡献代码** → 先读 [CONTRIBUTING.md](CONTRIBUTING.md)：
  fork → 主题分支 → 加边界测试 → 全量验证 → 提 PR（用 PR 模板，关联 issue）。
  核心包必须保持 stdlib-only、无 cgo；唯一的第三方依赖 MyGo 只用于 `native/` 桌面壳。
  维护者会 review PR，可能要求修改或拆分；合并后计入下个 Release Notes，并按提交作者署名。
- **行为准则**：[CONTRIBUTING.md 中的行为准则章节](CONTRIBUTING.md#code-of-conduct)

维护者会定期查看 Issue 和 PR；有贡献会认真研究后决定是否合并。

## 许可

[Apache-2.0](LICENSE) © 寒弦问月夜未阑
