# 用户指南

argentgo 的安装、快速上手、分发和包管理器配方。命令标志、传输和工具见 [Technical reference](TECHNICAL.md)；维护者与发布门禁笔记见 [CONTRIBUTING.md](../CONTRIBUTING.md)。全部 opt-in 开关（默认关闭的可选特性）及独立门控环境变量见 [Opt-in 开关使用指南](OPT_INS.md)。

## Install

argentgo 在同一个发布包中提供两个 Windows 二进制：

- `argentgo.exe` —— CLI、TUI 和 agent 引擎（从 `./cmd/argent` 构建）。
- `argentgo-native.exe` —— MyGo 原生聊天窗口（从 `./native` 构建）。

桌面客户端**始终跟随最新 Release**：请让两个二进制来自同一个 zip，以保证版本一致。没有安装服务、遥测或后台进程。

所有下载和发布说明都在公开的 GitHub 页面上：
<https://github.com/2118505078xyh1/argentgo/releases>。

### 要求

| 安装方式 | 要求 |
|---|---|
| 预编译 zip（推荐） | Windows amd64；PowerShell（或用 Git Bash 跑 `sha256sum`）。无需运行时。 |
| 从源码 `go install` | Go 1.27.3 或更新版本（`go.mod` 中固定为 `go 1.27.3`），需要联网拉取模块。完全从源码构建。 |
| 从源码构建原生桌面 | 本地检出、Go 1.27.3+ 和 Windows（桌面客户端仅支持 Windows）。 |

### 预编译二进制（Windows，最快路径）

1. 从 <https://github.com/2118505078xyh1/argentgo/releases/latest> 下载最新的发布资产：
   - `argentgo_<version>_windows_amd64.zip`
   - `checksums.txt`

2. 用 SHA-256 清单校验 zip。`checksums.txt` 是 `sha256sum` 风格的文件（`<hash>  <filename>`），所以在 Git Bash 中：

   ```bash
   sha256sum -c checksums.txt
   ```

   在 PowerShell 中，自己把文件哈希与清单行比较：

   ```powershell
   $zip = 'argentgo_<version>_windows_amd64.zip'
   $want = (Get-Content .\checksums.txt | Where-Object { $_ -match '\s' + [regex]::Escape($zip) + '$' } | Select-Object -First 1) -split '\s+'
   $got = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($got -ne $want[0]) { throw 'checksum mismatch' }
   ```

   发布说明里也写有哈希。构建脚本还会为每个资产生成一个
   `argentgo_<version>_windows_amd64.zip.sha256`，供只接受单哈希文件的工具使用。

3. 解压并做 smoke 测试：

   ```powershell
   Expand-Archive -LiteralPath '.\argentgo_<version>_windows_amd64.zip' -DestinationPath .
   .\argentgo.exe --version
   ```

   zip 会在根目录解出 `argentgo.exe` 和 `argentgo-native.exe`。**让两个文件保持在同一目录**：`argentgo gui` 会在 `argentgo.exe` 旁边找 `argentgo-native.exe`，找不到就会失败。把该目录加进 `PATH`，就能从任意 shell 运行 `argentgo`。

### PowerShell 单行安装脚本

对于有下载好的发布 zip 的本地检出，`scripts/install-win.ps1` 会把 `argentgo.exe` 安装到 `%LOCALAPPDATA%\argentgo\bin`（可用 `-InstallDir` 覆盖）。文档中的用法是：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1 `
    [-ZipPath dist/release/argentgo_<version>_windows_amd64.zip] `
    [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
```

把 zip 放在脚本所在检出旁边之后，一个具体的单行命令是：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1 -ZipPath '.\argentgo_<version>_windows_amd64.zip'
```

不带 `-ZipPath` 时，脚本会在 `dist/release`（`scripts/build-release.ps1` 写产物的地方）中查找唯一一个 `argentgo_*_windows_amd64.zip`。它**只**安装 CLI 二进制；`~/.argentgo` 下的会话/配置不会被触碰。对于桌面窗口，请把 zip 中的 `argentgo-native.exe` 复制到同一个安装目录。

### 从源码安装（`go install`）

`cmd/argent` 是一个 `main` 包，因此标准的 Go 安装路径有效：

```powershell
go install github.com/2118505078xyh1/argentgo/cmd/argent@latest
```

这会拉取公开模块并**从源码构建**；它需要 Go 工具链（1.27.3+），不是预编译二进制。生成的 `argentgo.exe` 会放在 `$GOBIN`（如果设置了），否则放在 `$(go env GOPATH)/bin`（用 `go env GOPATH` 查看）。用 `argentgo.exe --version` 验证。

`go install` 只安装 CLI/引擎，不构建桌面前端。它也能在非 Windows 宿主上用于 CLI/TUI，但 `argentgo gui` 需要 Windows 以及单独构建的 `argentgo-native.exe`。

### 从源码构建原生桌面前端

在本地检出中：

```powershell
go build -o argentgo-native.exe ./native
```

`native/` 是唯一依赖 MyGo 模块的地方（`github.com/egoist/mygo`，已在 `go.mod` 中固定）；引擎核心保持只用标准库且无 cgo。`argentgo gui` 会从引擎所在目录启动这个二进制，并通过已有的 `--rpc` 传输驱动它。桌面客户端仅支持 Windows。两个二进制的架构见 [Distribution](#distribution)。

### 更新

- **预编译：** 下载最新的 zip 和 `checksums.txt`，校验，并**同时**替换两个二进制。重跑安装脚本只更新 `argentgo.exe`，所以也要复制匹配的 `argentgo-native.exe`。
- **从源码：** 重跑 `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest`，并从更新后的检出重新构建 `./native`。

### 卸载

```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-win.ps1 `
    [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
```

该脚本只移除已安装的 `argentgo.exe`（以及空的安装目录）。`~/.argentgo` 下的用户数据——会话、配置、hook 和审计日志——会被保留。手动/zip 安装的话，删除解压出的目录；`go install` 的话，从 `$(go env GOPATH)/bin` 删除该二进制。如果你装过桌面前端，还要单独删除 `argentgo-native.exe`。

## Quickstart

这是从"已下载"到"第一个有用回答"的、语言无关的分步路径。除特别说明外，所有命令都是 Windows/PowerShell 的。完整安装指南见 [Install](#install)，完整命令表面见 [API reference](TECHNICAL.md#api-reference)。

> 下面的 `<your-key>` 是占位符。请替换成你自己的 API key，绝不要提交到版本库。

### 1. 安装

最快路径——预编译的 Windows zip：

1. 从 [latest release](https://github.com/2118505078xyh1/argentgo/releases/latest) 下载 `argentgo_<version>_windows_amd64.zip` 和 `checksums.txt`。
2. 校验 SHA-256 清单（Git Bash：`sha256sum -c checksums.txt`；PowerShell：把 `Get-FileHash` 输出与 `checksums.txt` 比较）。
3. 解压并做 smoke 测试：

   ```powershell
   Expand-Archive .\argentgo_<version>_windows_amd64.zip -DestinationPath .
   .\argentgo.exe --version
   ```

让 `argentgo.exe` 和 `argentgo-native.exe` 待在**同一目录**——`argentgo gui` 会在引擎旁边找原生二进制。桌面客户端始终跟随最新 Release，所以一定要同时替换两个二进制。从源码安装用 `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest`（Go 1.27.3+）。

### 2. 配置 provider

`argentgo login <provider>` 会把 key 存进配置文件（`~/.argentgo/config.json`），绝不回显：

```powershell
argentgo login openai --api-key <your-key>   # explicit key
argentgo login openai                        # prompts on stdin, without echo
argentgo auth                                # show stored credential status
```

key 从 `--api-key`、`<PROVIDER>_API_KEY`、`ARGENT_API_KEY` 或交互提示中读取。你也可以跳过 `login`，直接为当前 shell 设置环境变量：

```powershell
$env:OPENAI_API_KEY = "<your-key>"
```

运行 `argentgo models` 列出内置的 provider 模板（`openai`、`anthropic`、`deepseek`、`qwen` 等）。用 `argentgo logout [provider]` 移除已存储的凭据。

### 3. 在 TUI 中发出第一个 prompt

启动全屏终端 UI：

```powershell
argentgo tui
```

> 不带 prompt 的 `argentgo` 会读取 stdin 然后退出；它不会自动打开 TUI。
> 交互界面请用 `argentgo tui`（或 `argentgo --tui`）。一次性回答请用 `argentgo "your task"`。

输入任务并按 **Enter** 发送。常用按键：

| 按键 | 动作 |
|---|---|
| `Enter` | 发送 |
| `Alt+Enter` / `Ctrl+J` | 换行 |
| `PgUp` / `PgDn`, `Shift+Up` / `Shift+Down` | 滚动 |
| `Ctrl+C` | 取消当前轮次；再按一次退出 |
| `Ctrl+X` | 全局展开/折叠所有推理（思考）块；再按一次还原 |
| 鼠标点击推理块 | 单独展开/折叠该块（中文安全） |
| `/quit` 或 `/exit` | 退出 |

在输入框里输入 `/help` 可以显示应用内按键列表。

TUI 还有以下交互（默认开启）：输入 `/` 会弹出斜杠命令补全浮层，用上下键或鼠标选择，`>` 为高亮行、`(N/M)` 为候选计数；输入文件路径或 `@` 提及会触发行内补全，含空格/引号的路径会自动加引号。每轮结束后会追加一行统计信息 `模型 · 时长 · 平均 tok/s`（缺少打点或输出 token 时不显示）。若要让头部/状态栏额外显示速度与缓存命中率，设 `ARGENT_TUI_USAGE=1`（默认关闭，关闭时界面字节不变）。

### 4. 原生桌面窗口

```powershell
argentgo gui
```

它会从引擎所在目录启动 `argentgo-native.exe`。原生客户端通过内置的 `--rpc` 传输驱动引擎——没有浏览器、没有 WebView、没有 HTTP 服务。它仅支持 Windows，且要求 `argentgo-native.exe` 与 `argentgo.exe` 在同一目录。见 [Frontend decision](TECHNICAL.md#frontend-decision) 和 [Distribution](#distribution)。

### 5. 切换模型或 provider

```powershell
argentgo models                                   # list templates
argentgo --provider deepseek "帮我写个函数"        # use a built-in template
argentgo -m gpt-4o "explain this diff"             # -m / --model overrides the model
```

其他有用的开关：`--protocol anthropic|openai|responses`、`--base-url <url>`、`--profile <name>`，以及用于点分配置覆盖的 `-c/--config key=value`。设置的解析顺序是：标志 → `ARGENT_*` 环境变量 → 配置文件 → 默认值。

**原生推理档位**：`--thinking low|medium|high|xhigh`（也接受中文如 `低/中/高/超高`，或用 `ARGENT_THINKING`）会让请求携带各协议原生的推理控制字段——OpenAI chat 用 `reasoning_effort`、Responses 用 `reasoning.effort`、Anthropic 用 `budget_tokens`（下限 1024 并按模型上限钳制）。FIM 补全请求不会携带这些字段；不设置 `--thinking` 时请求保持原样。Anthropic 多轮扩展思考的思考签名会随会话保存，缺签名时自动降级为纯文本以避免 400。

> 安全提示：项目级的 `mcp.json` 或 `baseURL` 改写属于不可信内容。若不可信项目配置试图改写 baseURL 且请求同时带有环境变量/命令行传入的 API key，argentgo 会拒绝发送；确认可信后加 `--trust-project` 放行。Windows 上存储的令牌会用 DPAPI 按当前用户加密。

### 6. 审批与沙箱模式

```powershell
argentgo --approval ask   "refactor this"   # default: prompt for approval
argentgo --approval auto  "refactor this"   # auto-approve every request
argentgo --approval never "refactor this"   # deny every approval request
argentgo --sandbox-mode read-only "audit this dir"
```

出现审批提示时：**y** 允许，**n** 拒绝，**a** 本次会话允许，**esc** 拒绝。`--yolo` 是 `auto` 的快捷方式。沙箱模式有 `read-only`（移除修改类工具）、`workspace-write`（默认，强制工作区边界）和 `danger-full-access`（移除边界）。审批和策略决策会记录在 `~/.argentgo/audit/` 下脱敏后的审计日志中。

### 7. 斜杠命令

已验证的内置 TUI 命令：`/help`、`/clear`、`/expand`、`/undo`、`/retry`、`/name <title>`、`/quit`（别名 `/exit`）。`/undo` 和 `/retry` 作用于最新的 SHA-256 checkpoint。

Markdown 斜杠命令也会从 `.argent/commands`（项目）和 `~/.argentgo/commands`（用户）中发现；`argentgo commands` 会列出它们。它们的正文会展开 `$ARGUMENTS` / `$@` 以及 `$1`…`$N`。

> **没有**内置的 `/compact` 命令。上下文压缩是自动的；用 `--no-auto-compact` 可以在本次运行中禁用它。

### 8. 疑难解答

```powershell
argentgo doctor                          # config, storage, provider, git, Go
argentgo doctor --base-url <url>         # probe a specific provider endpoint
argentgo bug                             # write a diagnostics dump
```

`doctor` 会按检查项打印 `ok` / `warn` / `fail`。安装问题请重读 [Install](#install)；确切的标志和返回值见 [API reference](TECHNICAL.md#api-reference)。

### 9. 会话回退（rewind）与结果对比

不必删除整段会话就能从历史中的某条消息重新开始。两种入口都会把会话的活动叶**就地切换**到指定记录，后续新消息从该分叉点续写，不复制既有历史：

```powershell
argentgo sessions checkout <session-id> <entry-id>   # 切到指定记录
argentgo --session <session-id> --at <entry-id> "从这里继续"  # 恢复时直接定位
```

不带 `--at` 的 `--session` 恢复沿用原有行为。`/undo` 与 `/retry`（见 §7）作用于文件改动 checkpoint；`sessions checkout` 作用于会话树记录本身，两者互补。`sessions fork-at <id> <message-id>` 可在分叉时恢复当时的提示上下文。

评测结果可作统计对比（配对统计；样本少于 30 对时标记"样本不足"，避免据少量样本刷分）：

```powershell
argentgo eval compare <baseline-results.json> <candidate-results.json>
```

还可做两臂变体对比：按场景指纹把两组结果配对，再跑配对统计，并给出完成率增益对照 token 成本的诊断；样本少于 30 对时标记"样本不足"，不给出晋级结论。可选的两个指纹文件是「场景名 → 指纹」的 JSON 映射；省略时按场景名配对。

```powershell
argentgo eval ab <baseline-results.json> <candidate-results.json> [fingerprints-a.json fingerprints-b.json]
```

后台作业注册表默认关闭；需要跨工具调用管理长任务时设 `ARGENT_JOBS=1`（或在 `ARGENT_OPT_INS` 中加入 `jobs`）启用。分阶段审批（一次/始终/拒绝/esc）同样是 opt-in 安全能力，默认不接管审批。

### 10. 计划模式（只规划 + `exit_plan` 审批）

想让模型先给出计划、经你批准后再动手改代码时，开启只规划模式：

```powershell
$env:ARGENT_PLAN_MODE = "1"
argentgo "重构这个模块"
```

该模式下模型只做规划、不修改任何文件；工具目录会额外公布一个 `exit_plan` 工具，模型用它提交计划并请求批准。**未批准前修改类命令会被既有审批/安全链拦下**，批准后才解锁后续写操作。`exit_plan` 仅在该模式注册——不设置 `ARGENT_PLAN_MODE` 时，工具目录与默认逐字节一致。完整开关说明见 [Opt-in 开关使用指南](OPT_INS.md)。

## Distribution

本节记录 argentgo 如何打包和交付。面向维护者的发布门禁在 [CONTRIBUTING.md](../CONTRIBUTING.md#release-checklist)。

### 两个二进制

一个发布恰好交付两个 Windows 可执行文件，从同一份源码树、同一版本构建：

| 二进制 | 来源 | 角色 |
|---|---|---|
| `argentgo.exe` | `./cmd/argent`（`package main`, `func main`） | CLI、全屏 TUI 和 agent 引擎。还提供无头传输 `--serve`（HTTP/SSE）、`--rpc` 和 `acp`。通过 `--version` 报告版本。 |
| `argentgo-native.exe` | `./native` | MyGo 原生桌面聊天窗口，唯一的 GUI 前端。仅支持 Windows。 |

引擎核心只用标准库且无 cgo。`github.com/egoist/mygo`（固定在 `go.mod` 中）是唯一的第三方依赖，且只被 `native/` 使用。浏览器前端已移除。

### 它们如何通信（`--rpc`）

两个程序并不链接在一起；它们通过一个已有的传输对话：

1. `argentgo gui` 在 `cmd/argent/main.go` 中被识别，它调用 `cmd/argent/gui.go` 中的 `runGUI`。后者解析配置、打开会话存储，并拒绝在非 Windows 宿主上运行。
2. 它会在**正在运行的 `argentgo.exe` 旁边**（`os.Executable()` 所在目录）查找 `argentgo-native.exe`，以指向引擎路径的 `ARGENT_EXE` 和指向工作区根的 `ARGENT_CWD` 启动它。`ARGENT_CWD` 也成为子进程的工作目录。
3. `native/main.go` 读取 `ARGENT_EXE`；存在时，`native/bridge.go` 以工作区为工作目录拉起 `argentgo --rpc`。不存在时，原生窗口改为运行演示用的 canned 内容，而不是真实引擎。
4. `--rpc` 是 stdio 上换行分隔的 JSON-RPC 2.0（`internal/server/rpc.go`）。方法包括 `prompt`、`prompt/delta` 流式通知、`abort`、`session.list` 和 `session.load`。原生 UI 发送 prompt 并渲染 delta；它从不 import 引擎包，因此核心保持只用标准库。

### 发布资产布局

发布在公开的 GitHub 页面上：<https://github.com/2118505078xyh1/argentgo/releases>。打了 tag 的 commit **本身**不是发布：`scripts/publish-win.ps1` 会等待一个同时具备两个必需资产的已发布 Release，只有 tag 存在时会失败。

每次发布：

```text
argentgo_<version>_windows_amd64.zip     # contains argentgo.exe + argentgo-native.exe at the root
argentgo_<version>_windows_amd64.zip.sha256  # single-hash sidecar
checksums.txt                          # sha256sum-style manifest for all zips
```

- `scripts/build-release.ps1` 是产物的唯一事实来源。它用 `CGO_ENABLED=0`、`GOOS=windows`、`GOARCH=amd64` 和 `-trimpath` 构建 `./cmd/argent` 和 `./native`，然后把两个二进制一起打包。
- zip 是 GUI 子系统构建（`-H=windowsgui`）：从桌面快捷方式启动不会出现黑色控制台窗口，同时从已有终端运行的 CLI 输出仍然可用。
- `checksums.txt` 是无 BOM、LF 换行的 UTF-8，因此 `sha256sum -c` 能接受它。每行是 `<sha256>  <filename>`。合并清单还驱动更新器的自动校验；每个 zip 的 `.sha256` 附带文件供那些期望每个资产一个哈希的工具使用。
- 发布说明会重述哈希。`scripts/publish-win.ps1` 可以用仓库 CI 工作流及其内置凭据发布，也可以手动创建 Release 并上传同样两个资产。

### 校验和

SHA-256 在构建时用 `Get-FileHash` 计算。校验方式：

```bash
# Git Bash
sha256sum -c checksums.txt
```

```powershell
# PowerShell: compare against the manifest line for your zip
(Get-FileHash -LiteralPath '.\argentgo_<version>_windows_amd64.zip' -Algorithm SHA256).Hash.ToLowerInvariant()
```

完整校验示例见 [Install](#install)。

### 支持的平台

- **Windows amd64 是唯一发布的二进制目标。** `scripts/build-release.ps1` 定义一个 `windows/amd64` 目标，`scripts/publish-win.ps1` 要求存在一个 `argentgo_*_windows_amd64.zip` 资产。
- **没有 cgo**，因此 `GOOS=linux go build ./...` 能交叉编译成功，CI 会在每个 PR 上验证。不发布 Linux 二进制或其他平台的 zip。
- `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` 在任何装有 Go 1.27.3+ 的宿主上都可用，但只安装 CLI/引擎。桌面客户端仅支持 Windows，必须构建为 `./native` 或从发布 zip 中取用。
- 桌面客户端始终跟随最新 Release：请从同一个 zip 安装两个二进制，不要混用版本。

### Version locations

版本在三个源码字面量中重复出现，必须一起升级：

| 文件 | 常量 / 字段 |
|---|---|
| `internal/cli/cli.go` | `const Version` —— 由 `--version`、`/v1/health` 和 ACP `initialize` 报告；发布脚本读取它。 |
| `internal/mcp/mcp.go` | `clientVersion` —— 作为 MCP `clientInfo.version` 发送。 |
| `internal/lsp/client.go` | `initialize` 请求的 LSP `clientInfo` 中的 `version`。 |

`scripts/build-release.ps1` **只**从 `internal/cli/cli.go` 读取版本，并据此校验 `-Version` / `-Tag`，所以另外两个文件里不一致不会被打包脚本抓到。请让三个保持同步。

## 包管理器

本节收集社区为安装 argentgo 准备的**配方模板**。它们是模板，不是已发布的产物：本项目**不**维护或托管 Scoop、Chocolatey 或 Homebrew 中的目录条目。维护者（或下游打包者）把配方复制到相应的 bucket / 包仓库 / tap，并在发布时填好占位符。这里没有任何东西依赖本项目自己托管的目录。

公开仓库：<https://github.com/2118505078xyh1/argentgo>

### 文件

| 文件 | 包管理器 | 平台 | 安装内容 |
| --- | --- | --- | --- |
| `packaging/scoop/argentgo.json` | Scoop | Windows (amd64) | 下载并解压发布 zip；为 `argentgo.exe` 和 `argentgo-native.exe` 创建 shim。 |
| `packaging/choco/argentgo.nuspec` | Chocolatey | Windows (amd64) | NuGet 包元数据（无依赖）；版本必须与发布一致。 |
| `packaging/choco/tools/chocolateyinstall.ps1` | Chocolatey | Windows (amd64) | 用 `Install-ChocolateyZipPackage` 下载发布 zip 并校验其 SHA-256。 |
| `packaging/homebrew/argentgo.rb` | Homebrew | macOS / Linux | 用 `go install` 从源码构建 CLI/TUI。原生桌面 GUI 仅支持 Windows，不包含在内。 |

所有配方只使用公开的 GitHub Releases URL。没有凭据、token 或私有路径。

### 占位符约定

配方有意**不**硬编码当前版本。发布自动化在发布包之前会替换这些占位符：

- `{{VERSION}}` —— 不带 `v` 的纯发布版本，例如 `0.1.1`。用于资产名（`argentgo_{{VERSION}}_windows_amd64.zip`）和发布 tag（`v{{VERSION}}`）。
- `{{SHA256}}` —— Windows zip 的小写十六进制 SHA-256。从 `checksums.txt` 中第二列为 `argentgo_<version>_windows_amd64.zip` 的那一行取（`scripts/build-release.ps1` 把该文件写进 `dist/release/`）。

以 `v0.1.1` 发布为例的替换：

- URL：
  `https://github.com/2118505078xyh1/argentgo/releases/download/v0.1.1/argentgo_0.1.1_windows_amd64.zip`
- `{{VERSION}}` -> `0.1.1`
- `{{SHA256}}` -> 匹配的 `checksums.txt` 行的第一列

`scripts/build-release.ps1` 会打印 zip 名并写出 zip 和 `dist/release/checksums.txt`。从它的 `argentgo_<version>_windows_amd64.zip` 输出中读取纯版本号（它对应 `internal/cli/cli.go` 中的 `cli.Version`），并从匹配的 `checksums.txt` 行读取哈希；不要凭空编造这两个值。

### 维护者如何填写每个文件

1. 运行 `powershell -File scripts/build-release.ps1`（或直接使用已发布的发布资产）。记下 Windows amd64 zip 的版本和 `checksums.txt` 中的哈希。
2. **Scoop** —— 在 `packaging/scoop/argentgo.json` 中替换 `{{VERSION}}` 和 `{{SHA256}}`。`hash` 字段来自 `checksums.txt`，这样 Scoop 会校验下载。清单还带 `checkver` / `autoupdate` 块：Scoop 自己的 `checkver` 会读取 GitHub releases（无需项目托管的目录），`autoupdate` 会重写 URL 并通过正则从 `checksums.txt` 拉取新哈希。JSON 不能包含注释，所以这段说明放在本指南里。
3. **Chocolatey** —— 在 `packaging/choco/argentgo.nuspec` 中替换 `{{VERSION}}`，在 `packaging/choco/tools/chocolateyinstall.ps1` 中替换 `{{VERSION}}` / `{{SHA256}}`，然后运行 `choco pack`。安装脚本把 `checksums.txt` 中的哈希作为 `checksum64` / `checksumType64` 传给 `Install-ChocolateyZipPackage`，这样被篡改的下载会失败。该包没有依赖。
4. **Homebrew** —— 在 `packaging/homebrew/argentgo.rb` 中替换 `{{VERSION}}` 并发布到 tap。`go install github.com/2118505078xyh1/argentgo/cmd/argent@{{VERSION}}` 要求一个 Go 工具链接受的版本；发布 tag 带 `v` 前缀，如果你的 tag 是 `v0.1.1`，就把那一处填成 tag 形式（例如 `v0.1.1`）。`url` 字段已经指向该 tag 的源码归档。

### 隐私说明

这些配方只引用公开仓库和公开发布资产。没有嵌入 API key、token（`sk-`、`ghp_` 等）、邮箱地址或私有/本地文件系统路径。请保持如此：每个值要么是公开 URL，要么是从发布派生的版本/哈希。

### 验证

替换占位符后，发布前先验证：

```bash
python -m json.tool packaging/scoop/argentgo.json >/dev/null
python -c "import xml.etree.ElementTree as ET; ET.parse('packaging/choco/argentgo.nuspec')"
ruby -c packaging/homebrew/argentgo.rb
```
