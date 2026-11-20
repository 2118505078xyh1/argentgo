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

> 本文件是 [README.md](README.md) 的中文版。英文原版为准；命令与标志保持原样。

## 安装 / Install

完整安装指南见 [docs/INSTALL.md](docs/INSTALL.md)。最快路径（Windows 预编译包）：从
[最新 Release](https://github.com/2118505078xyh1/argentgo/releases/latest) 下载
`argent_<version>_windows_amd64.zip` 与 `checksums.txt`，校验后解压运行：

```powershell
sha256sum -c checksums.txt                              # Git Bash；PowerShell 用 Get-FileHash
Expand-Archive .\argent_<version>_windows_amd64.zip -DestinationPath .
.\argent.exe --version
```

解压后让 `argent-native.exe` 与 `argent.exe` 保持在同一目录（`argent gui` 需要它）。
桌面客户端**始终跟随最新 Release**：请从同一个 zip 中取出两个二进制，保证版本一致。

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
- **多界面** — 命令行一次性问答、全屏 TUI、**MyGo 原生桌面窗口**（单进程约 76 MB、空闲 0% CPU，无 WebView），以及 HTTP/SSE、JSON-RPC、ACP 三种无头传输。
- **双语** — zh-CN / en-US 文案嵌在二进制里，无需外部 locale 文件。

体积是实测数字：无头运行工作集约 **10 MB**，二进制约 **10.6 MB**。

## 快速开始

```powershell
# 配好 key（也支持 ARGENT_API_KEY 环境变量或配置文件）
$env:ARGENT_API_KEY = "<your-key>"

argent "把 src 里的 unwrap 都找出来"     # 一次性提示，打印回答
argent --mode json "列出目录"            # 严格 JSONL：事件 + 结果
argent tui                                # 全屏终端 UI（SSH 也能跑）
argent gui                                # Windows 原生聊天窗口（MyGo，无浏览器）
```

`<your-key>` 是占位符，请替换成你自己的 API key，不要提交到版本库。也可以用
`argent login <provider> --api-key <your-key>` 把 key 存进配置文件（不会回显）；详见
[docs/QUICKSTART.md](docs/QUICKSTART.md)。

想换成国产模型？内置模板直接切：

```powershell
argent --provider deepseek  "帮我写个函数"
argent --provider qwen      "解释这段代码"
argent --provider anthropic "review 一下这个改动"
```

`argent gui` 在 Windows 上默认启动 **MyGo 原生聊天窗口**（`argent-native.exe`）：
纯 Go 绘制、无 WebView、单进程约 76 MB、空闲 0% CPU，视觉按 Claude / Codex 风格
（暖灰底、珊瑚色强调）。它通过已有的 `--rpc` 传输驱动 `argent.exe` 引擎，核心包保持
stdlib-only；只有桌面壳这一层依赖 MyGo。

发布包同时包含 `argent.exe`（引擎/CLI）和 `argent-native.exe`（原生前端）。
浏览器前端已移除，桌面只走原生窗口。详见 [docs/FRONTEND.md](docs/FRONTEND.md)。

其他入口：

```powershell
argent --serve --serve-token <token>      # HTTP + SSE
argent --rpc                              # stdio 上的换行分隔 JSON-RPC
argent acp                                # Agent Client Protocol 服务端
```

## 它会做什么

```
你：把 login 的 token 校验抽成函数，并补测试

argent：
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
失败与下一步，不丢上下文。可用 `--no-auto-compact` 关闭本次运行的自动压缩。

| 命令 | 作用 |
|---|---|
| `sessions --grep <词>` | 在所有会话里搜索（含脱敏后的尝试片段） |
| `sessions tree` | 查看 fork / retry 关系树 |
| `sessions export` | 导出为离线 HTML |
| `argent --undo --session <id>` | 从最近检查点恢复文件（冲突编辑会拒绝覆盖） |
| `argent --retry --session <id>` | 从检查点分叉重跑 |
| `argent --title "名字"` | 覆盖自动生成的会话标题 |

## 配置

分层，低 → 高：编译默认值 → `~/.argentgo/config.json` → `<cwd>/.argent/config.json`
→ `<cwd>/argent.json` → profile → `ARGENT_*` 环境变量 → CLI 标志。
`-c/--config key=value` 在配置文件层生效（点分键嵌套），低于环境变量与标志。

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

这不是操作系统级隔离；要强隔离请把 argent 放进容器。

- 每个文件工具都把路径解析到工作区内，`..` 和绝对路径逃逸一律拒绝；符号链接不能绕过白名单。
- 修改类工具走同一条链：**策略（硬门禁）→ 审批 → 脱敏审计日志**。策略拒绝直接结束运行；
  审批拒绝变成模型可读的工具错误。`--yolo` 等价于自动批准。
- 沙箱：`read-only` 移除修改类工具；`workspace-write`（默认）保留边界；
  `danger-full-access` 移除边界。`--add-dir` 扩展可写根目录。
- 审计写在 `<storage>/audit/<day>.jsonl`，密钥、token 在落盘前脱敏。

## 文档

- [安装指南](docs/INSTALL.md) · [5 分钟快速上手](docs/QUICKSTART.md)
- [分发架构](docs/DISTRIBUTION.md) — 两个二进制如何打包发布
- [API 参考](docs/API_REFERENCE.md) — 命令、工具与字段
- [架构](docs/ARCHITECTURE.md) — 请求流、会话存储、权限、TLS、传输
- [架构图](docs/ARCHITECTURE_DIAGRAMS.md) — Mermaid 图
- [前端方案](docs/FRONTEND.md) — MyGo 原生窗口（唯一桌面前端）
- [安全模型](#工作区安全模型)（英文版：[Workspace security model](README.md#工作区安全模型)）
- [CI 契约](docs/CI.md) · [CI 工作流](.github/workflows/ci.yml) — Linux 上的测试、clippy 与 Docker 构建
- [功能对齐](docs/PARITY.md) · [路线图](docs/ROADMAP.md) · [下一步](docs/NEXT_STEPS.md)
- [迁移记录](docs/MIGRATION.md) · [更新日志](CHANGELOG.md)

## 开发与测试

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # Go 不在 PATH 时
go build -o argent.exe ./cmd/argent
go test ./...
go vet ./...
gofmt -l .                        # 必须无输出

go build -o argent-native.exe ./native   # 原生桌面前端
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
  （Bug / 功能 / 文档模板，提交日志请脱敏）
- **提问或讨论** → [Discussions](https://github.com/2118505078xyh1/argentgo/discussions)
- **想贡献代码** → 先读 [CONTRIBUTING.md](CONTRIBUTING.md)：
  fork → 主题分支 → 加边界测试 → 全量验证 → 提 PR（用 PR 模板，关联 issue）。
  核心包必须保持 stdlib-only、无 cgo；唯一的第三方依赖 MyGo 只用于 `native/` 桌面壳。
- **行为准则**：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)

## 许可

[Apache-2.0](LICENSE) © 寒弦问月夜未阑
