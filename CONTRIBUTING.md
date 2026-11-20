# 为 argentgo 做贡献

感谢你有兴趣改进 argentgo。这是一个个人的、仅依赖标准库的 Go 编码 agent 框架，配有原生 MyGo 桌面客户端。我们欢迎 bug 报告、功能提议、文档修正和代码 PR。

这一份文档还承载了社区行为准则、安全模型、CI 契约、架构依赖守卫、维护者发布清单和本地发布验证脚本——它们原本都是独立文件，作为带标题的章节保留在下面。

## 基本规则

- **核心保持只用标准库。** `internal/{agent,session,llm,tools,security,...}` 不得引入第三方依赖。唯一的例外是 `native/`，它使用 `github.com/egoist/mygo` 来实现桌面 UI。
- **不使用 cgo。** 所有代码都必须能用 `GOOS=linux go build ./...` 交叉编译。
- **Windows 宿主**是发布目标；Linux 交叉构建必须保持通过。
- 提交、Issue、PR 或发布说明中**不得出现密钥、token、个人邮箱或本地路径**。
- 保持友善。参见[行为准则](#code-of-conduct)。

## 报告 bug

用 **Bug report** 模板开一个 Issue。包含：

- argentgo 版本（`argent --version`）以及 OS/Go 版本。
- 最小复现（CLI 命令，或在原生窗口中的操作步骤）。
- 期望行为与实际行为，以及相关日志行（把密钥脱敏）。
- 对于崩溃：说明你当时在做什么，以及是否有会话文件处于打开状态。

## 提议新功能

用 **Feature request** 模板开一个 Issue。说明问题、期望的行为，以及为什么它适合一个本地、离线优先、单二进制的工具。欢迎宏大的想法，但可能会拆成更小的 PR。

## 开发环境搭建

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # if Go not on PATH
go build ./...                        # host
$env:GOOS='linux'; go build ./...; Remove-Item Env:GOOS     # cross-build
go vet ./...
gofmt -l .                            # must print nothing
go test ./... -count=1
go build -o argent-native.exe ./native
```

用 `argent --help` 运行 CLI。原生客户端从 `native/` 构建，并通过已有的 stdio `--rpc` 传输与引擎通信。

## 提交 Pull request

1. fork 仓库，从 `main` 创建一个主题分支。
2. 做一个聚焦的改动，并在你触及的边界上添加测试。测试名要体现行为（`TestUndoRefusesConflictingManualEdits`），适用时用表驱动。
3. 运行上面完整的验证；CI 在 Linux 和 Windows 上跑同样的检查。
4. 用模板开 PR。关联它修复的 Issue（`Fixes #123`）。
5. 保持提交聚焦；使用 `<area>: <imperative summary>`（例如 `session: reject torn JSONL tail`）。

维护者会进行评审。我们可能要求修改、拆分 PR，或在它违反只用标准库/无 cgo/单二进制的约束时拒绝它。合并后，你的贡献会出现在下个发布说明中，你的提交也会署名归你。

## 我们不太可能接受的内容

以下事项有意超出这个个人框架的范围：C-ABI 插件加载器、tree-sitter AST、Rhai 脚本、设备控制、遥测、插件市场、插件图、SQLite/FTS5、远程沙箱和托管目录。在这些方向上投入之前，请先开 Issue 确认。

## 发布流程

维护者打 `vX.Y.Z` 标签，并通过 `.github/workflows/release.yml` 发布 Windows zip + `checksums.txt`。贡献者不需要触碰发布脚本。

## Code of Conduct

### 我们的承诺

我们承诺让参与 argentgo 社区的体验对所有人都免受骚扰，无论年龄、体型、残障、族裔、性征、性别认同与表达、经验水平、教育程度、社会经济地位、国籍、个人外貌、种族、宗教，或性认同与取向。

### 我们的标准

有助于营造积极环境的行为示例：

- 使用友善、包容的语言。
- 尊重不同的观点与经历。
- 优雅地接受建设性批评。
- 以社区的最佳利益为重。
- 对其他社区成员表达同理心。

不可接受的行为示例：

- 使用性化语言或图像，以及不受欢迎的性关注。
- 挑衅、侮辱或贬低性评论，以及人身或政治攻击。
- 公开或私下骚扰。
- 未经明确许可发布他人的私人信息，例如物理或电子地址。
- 其他在职业环境中可合理视为不当的行为。

### 执行责任

项目维护者负责澄清并执行这些标准，并会对任何他们认为不当、威胁、冒犯或有害的行为采取适当且公正的纠正措施。

### 适用范围

本行为准则适用于所有社区空间，也适用于个人在公共场合正式代表项目时的行为。

### 执行

辱骂、骚扰或其他不可接受的行为，可以通过开一个保密 Issue 或通过 GitHub 联系维护者来举报。所有投诉都会被及时、公正地审查和调查。维护者有义务尊重任何事件举报者的隐私与安全。

### 出处

本行为准则改编自 [Contributor Covenant][homepage] 2.1 版，见
https://www.contributor-covenant.org/version/2/1/code_of_conduct.html。

[homepage]: https://www.contributor-covenant.org

## Security

`argentgo` 把每一次工具调用都视为不可信，直到它通过了 `internal/security` 中一条简短、确定性的链条。这条链刻意做得很小、只用标准库，以便端到端审查它的行为。它是给在自己机器上跑 agent 的操作员的一组护栏，而不是用来对抗坚定攻击者的沙箱。

### 链条：策略 → 审批 → 审计

`security.Chain.Check` 按固定顺序评估一次调用：

1. **可选的 plan-mode 命令守卫。** 当 `PlanCommandGuard` 启用时，修改性 shell 命令会在其他一切之前被拦下，并返回一个模型可读、可重试的错误。没有审批提示、没有策略拒绝、没有审计条目——这次调用根本不会到达链条。
2. **策略决定。** `Policy.Evaluate` 应用拒绝/允许工具列表、路径规则、命令规则和一个有序规则表。拒绝会以包装了 `ErrDenied` 的 `StopError` 结束运行，并附上一句终结性的话，告诉模型不要重试或绕过拦截。
3. **审批询问。** 如果策略判定为 `Ask`，`Approver` 会询问操作员。只有 `y`/`yes` 表示批准；EOF 和空回答表示拒绝。拒绝会以 `ErrRefused` 结束运行，它同样是一个 `StopError`。nil 审批通道表示不可用（不是拒绝）。
4. **审计记录。** 每个到达的决策都会追加到私有目录下的 JSONL 审计文件中。审计是证据，不是门禁：写入失败会上报，但绝不阻断运行。

策略拒绝和操作员拒绝都是终态的；审批拒绝会变成一个模型可读的工具错误。checkpoint 恢复通过 `CheckRestore` 走同一条链，因此撤销无法绕过策略或审批。

**先脱敏，再留证。** `Chain.record` 会对工具参数调用 `RedactJSON`，对原因调用 `Redact`；`AuditLog.Record` 在写入前会再次脱敏。不存在把未脱敏参数写进审计日志的路径。`Redact` 能识别 provider 密钥前缀、认证头、键值凭据、URL userinfo 和常见 token 格式；`RedactPath` 会折叠主目录和凭据形状的路径段；`RedactJSON` 会清洗凭据命名键下的值，而不打平文档。

### 沙箱模式

`--sandbox-mode` 通过 `security.FromString` 选择文件工具策略：

| 模式 | 行为 |
| --- | --- |
| `read-only` | 移除所有修改类工具；只保留已知安全的工具。 |
| `workspace-write`（默认） | 允许写入，但限定在工作区以及任何 `--add-dir` 写入根内。 |
| `danger-full-access` | 移除写入根目录边界。策略规则仍然生效。 |

`InWriteRoots` 在比较前会同时解析候选路径和每个根目录，因此根目录内的符号链接或 Windows junction 无法把写入重定向到外面。无法解析的路径会被拒绝，而不是放行。

### 路径边界

每个工具路径都会先规范化（反斜杠、大小写、前导斜杠），再用 `GlobMatch` 匹配，它支持段内的 `*`、跨段的 `**` 以及表示单个字符的 `?`。字面模式使用前缀语义。`Policy.checkPath` 还会拒绝相对的 `..` 逃逸，并检查磁盘上的规范位置，因此即使没有配置显式拒绝规则，指向工作区外的链接也会被拒绝。权限规则使用 `WildcardMatch`，并采用拒绝优先的评估以保证安全。

### 不可见 Unicode

展示给操作员的内容在打印审批提示前会被清理。`ContainsInvisibleTags` 检测 Unicode Tag Block 字符（第 14 平面），它们不渲染任何内容，可以把注入载荷藏在可读文本里。`SanitizeInvisible` 会移除这些标签、报告是否发现过它们以便提示给出警告，并用 `EscapeBidi` 转义双向/格式控制字符，让操作员看到的是 `\uXXXX` 序列。只有提示副本会被清理：实际执行的参数从不修改，因此显示与执行不会悄悄分叉。

### 命令守卫

`FindFileEditingCommand` 是 plan mode 中使用的 token 感知分类器。在寻找 POSIX、cmd.exe 和 PowerShell 的文件修改器、`git push` 这样的子命令以及非临时输出重定向之前，它会先遮蔽带引号的字符串、反引号替换、heredoc 正文和 shell 注释。它偏保守：只读命令（`ls`、`cat`、`git status`、`go test`、`Get-Content` 等）绝不能被标记，而修改性命令偶尔可能漏过，再由正常的策略/审批路径拦下。

### 威胁模型

**这能防御什么**

- 密钥意外泄露到 transcript 或审计日志（脱敏）。
- 词法层面以及符号链接/junction 形式的出工作区路径逃逸（路径边界）。
- 藏在不可见 Unicode 字符或双向覆盖中的 prompt 注入（审批提示中的标签剥离与可见转义）。
- 用户以为处于只读规划阶段时执行破坏性 shell 命令（可选的 plan-mode 命令守卫）。
- 没有明确决策的无人值守修改（策略默认值、审批、终态错误）。

**这不能防御什么**

- **恶意语言服务器或插件。** MCP 服务器、LSP 服务器、hook 和插件以 agent 的权限运行。它们的信任在别处建立（hook SHA-256 校验、清单审查）；这条链只门禁到达它的工具调用。
- **被攻陷的依赖或被攻陷的 Go 工具链。** 核心只导入标准库，但原生桌面壳依赖第三方 UI 模块及其传递依赖树。供应链攻陷不在这条链的范围之内。
- **操作系统层面的攻击：** root/admin 失陷、内核漏洞、其他本地用户、物理接触，或机器上已有的恶意软件。
- **保持可见的坚定 prompt 注入。** 如果操作员能读懂文本却仍然批准，那么这条链已经尽职；它不判断意图。
- **网络层面的密钥管理。** token 存在配置或环境变量里；审计清洗器只是减少扩散，它不是密钥库。

### 验证与审计

运行可复现的审计（离线；Windows PowerShell）：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/security-audit.ps1
```

它依次运行：`go vet ./...`、`gofmt -l .`（必须为空）、PATH 上有该工具时运行 `govulncheck ./...`、通过 `go test ./internal/security -run Fuzz -count=1` 运行 fuzz 种子语料、`go list -m all` 依赖白名单检查、对 `internal/` import 的只用标准库检查，以及对已跟踪源码中类 token 字面量的隐私扫描。缺少 `govulncheck` 会打印安装提示并跳过；这是唯一的软失败。其余任何问题都会非零退出，脚本会打印简短摘要。

fuzz 本身是可选启用的，使用 Go 标准库的 `testing.F`：

```powershell
go test ./internal/security -fuzz FuzzRedact -fuzztime=30s
go test ./internal/security -fuzz FuzzSanitizeInvisible -fuzztime=30s
```

`internal/security/fuzz_test.go` 里的种子语料也会作为普通测试运行，所以直接跑 `go test ./internal/security` 无需 fuzz 引擎就能检查它们。

#### 关于白名单的说明

- 依赖白名单包含 `github.com/egoist/mygo` **以及**它固定的传递模块，因为 `go list -m all` 会报告整个模块图。它强制的不变量是"没有这个已审查集合之外的模块"；单独的 `go list -deps ./internal/...` 检查则强制"核心只导入标准库"。添加依赖是一个有意的审查步骤，必须更新审计脚本里的白名单。
- 隐私扫描对嵌在单元测试中的两个合成凭据 fixture 开了白名单，按精确文件路径和匹配值限定。新的匹配——即使出现在同样的文件里——也会让审计失败。不要把真实凭据加进源码。

## CI

本仓库有三个触发条件互不重叠的工作流。它们共同构成发布门禁：只有当适用于某个改动的所有工作流都通过时，它才算"绿"。

### 工作流及其触发时机

| 工作流 | 触发条件 | 用途 |
| --- | --- | --- |
| `ci.yml` | push 到 `main`/`master`，以及每个 pull request | 对交付的 Windows 目标做快速门禁：`gofmt`、`go vet`、`go test`、无 cgo 构建，外加一个 Linux `-race` job。 |
| `matrix.yml` | 每个 pull request，以及手动 `workflow_dispatch` | 端到端可移植性证明：无 cgo 交叉编译、不含 cgo 的单元测试、race 门禁和架构守卫。 |
| `release.yml` | 匹配 `v*` 的 tag | 可复现的打包，由自身的 `verify` 和 `race` job 门禁；两者都通过后才发布。 |

`matrix.yml` 有意**不**由 tag 触发：`release.yml` 掌管 tag，在 tag 上重复跑 matrix 只会消耗 runner 时间而不增加门禁。如果你想在分支上拿到完整的可移植性信号，可以从 Actions 页面手动运行它。

三个工作流都使用 `permissions: contents: read`，只有 `release.yml` 中那个负责发布的 job 例外，它需要写权限来创建 Release。本仓库没有任何工作流需要用户提供的认证。

### 每个 job 验证了什么

#### `ci.yml`

- **`go`（windows-latest）** — 源码是 gofmt 干净的、vet 干净的、全部测试通过，交付的 `windows/amd64` 二进制能以 `CGO_ENABLED=0` 构建。它还会交叉编译到 `linux/amd64`，这是对无 cgo 承诺最初的抽查。
- **`race`（ubuntu-latest）** — 除 `internal/pty` 外每个包的 `go test -race` 都通过（原因见下文）。Linux runner 自带 gcc，race 检测器需要它。

#### `matrix.yml`

- **`crossbuild`** — 对下面五个目标全部以 `CGO_ENABLED=0` 运行 `go build ./...`。构建 `./...` 包含 `native/`，所以意外的 cgo 依赖或仅平台可用的 import 都会让 job 失败。不上传产物：构建成功*本身就是*可移植性的证明。
- **`unit`** — 在干净的 Linux runner 上运行 `CGO_ENABLED=0 go test ./...`，证明单元测试套件不需要 C 工具链。
- **`race`** — 复刻 `ci.yml` 的 race 门禁，并给一个宽松的 `-timeout 20m`，这样挂住的测试会失败，而不是卡住 job。
- **`archguard`** — `go test ./internal/archguard -count=1`（见下文）。

#### `release.yml`

- **`verify` / `race`** — 复刻 `ci.yml` 的门禁，这样 tag 不会发布通不过 CI 的代码。
- **`release`** — 只在两个门禁都通过后运行；用 `-trimpath` 和 `CGO_ENABLED=0` 构建，校验产物，然后发布。

### 无 cgo 交叉编译矩阵

| GOOS | GOARCH | 说明 |
| --- | --- | --- |
| `windows` | `amd64` | 唯一分发的桌面目标。 |
| `linux` | `amd64` | 常见 CI 宿主；已在 `ci.yml` 中抽查。 |
| `linux` | `arm64` | 证明纯 Go 核心与架构无关。 |
| `darwin` | `amd64` | Intel macOS 可移植性。 |
| `darwin` | `arm64` | Apple 芯片可移植性。 |

每一项都以 `CGO_ENABLED=0` 并设置 `GOOS` 和 `GOARCH` 运行，且 `fail-fast: false`，这样一个失败不会掩盖另一个。这使得 `AGENTS.md` 的规则"所有代码都必须能用 `GOOS=linux go build ./...` 交叉编译"成为一条被强制执行的多目标契约，而不只是本地的口头承诺。

### race 门禁与 pty 排除

race 检测器需要 cgo，因此两个 race job 都跑在 `ubuntu-latest` 上，那里有 gcc（Windows 自带的工具链不是可靠的 race 宿主）。该命令只排除一个包：

```sh
go test -race $(go list ./... | grep -v '/internal/pty$')
```

`internal/pty` 的测试需要一个具有 ConPTY 语义的真实控制终端；headless 的 CI runner 提供不了。只排除这个包，可以让并发门禁覆盖其余所有跨平台包，而 Windows 的 `ci.yml` job 仍然会运行 `internal/pty`（不带 `-race`）。

### 架构守卫

`internal/archguard` 把 `AGENTS.md` 中的依赖规则编码成一个测试：核心保持只用标准库、禁止的 import 边会被拒绝、不引入新模块。它是分层规则的机器可检查形式。`-count=1` 会禁用 Go 测试缓存，让不变量在每个 PR 上真正被求值，而不是复用缓存结果。

### 用 `scripts/flake-guard.ps1` 在本地检测不稳定测试

对墙钟时间敏感的测试（TUI 的 ctrl+c 路径、hook shell 超时、job 进程测试、agent 计时逻辑）过去时不时不稳，只是被缓解而非被监控。flake guard 会重跑这些包以暴露不稳定性：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Count 10
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Packages internal/hooks
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Race -Count 3
```

参数：

- `-Count` — 每个包的重复次数（默认 `5`）。会直接传给 `go test -count=N`，从而绕过测试缓存。
- `-Packages` — 包列表（默认 `internal/tui`、`internal/hooks`、`internal/jobs`、`internal/agent`）。路径相对于仓库根目录。
- `-Race` — 加上 `-race` 并设置 `CGO_ENABLED=1`。默认关闭以控制运行时长；CI 的 race 门禁已经覆盖并发。

每个包按 `go test <pkg> -count=<Count> -timeout 300s` 运行。任何失败都会打印失败的包及其输出，并非零退出，因此该脚本可用作本地的推送前检查或定时任务。它不创建临时文件，并在结尾打印总运行时长。

该脚本有意不作为每个 PR 的必需 CI 门禁：把完整套件乘以 N 会让每个 PR 都很慢。当计时相关的测试发生变化、排查可疑的 CI 失败，或定期作为定时任务运行时使用它。

### "CI 全绿" 的含义

一个改动在以下情况下可以合并：

1. `ci.yml` 在该 pull request 上通过（格式、vet、测试、Windows 构建、包含所有非 pty 包的 Linux race 门禁）。
2. `matrix.yml` 在同一个 pull request 上通过：全部五个无 cgo 交叉编译、不含 cgo 的单元测试套件、race 门禁和架构守卫。
3. 如果针对触及的计时敏感代码在本地运行过 `scripts/flake-guard.ps1`，它报告 PASS 且没有不稳定的包。

对于发布，`release.yml` 还必须通过它的 `verify` 和 `race` 门禁，并在 tag 被视为已发布之前产出预期的产物。仅仅因为 `ci.yml` 是绿的，就接受一个红的 matrix job 是绝不允许的：matrix 才是客观的可移植性证明。

## Architecture dependency guard

这是 `argentgo` 的架构依赖守卫测试。它把仓库级 `AGENTS.md` 中的依赖规则变成自动检查，让错误的 import 由 `go test` 发现，而不是靠代码评审。

### 它强制什么

该测试（`internal/archguard/arch_test.go`）通过读取 `go.mod` 来发现模块路径（绝不硬编码），然后遍历 `internal/` 和 `native/` 下的每个 `.go` 文件，用 `go/parser`（`parser.ImportsOnly`）解析 import。它断言：

- **核心只用标准库。** `internal/**` 下的任何文件都不得 import 第三方模块——即第一个路径段包含 `.` 的 import（`github.com/...`、`golang.org/...`、`gopkg.in/...`）。对本仓库自身模块（`<module>/...`）的 import 属于仓库内引用，不是第三方，允许。
- **不使用 cgo。** 任何地方都没有文件使用 `import "C"`，因此 `GOOS=linux go build ./...` 能继续工作。
- **分层（内部包是库）。**
  - `internal/tools` 不得 import `internal/agent`。
  - `internal/agent` 不得 import `internal/app`（`app` 是顶层装配器，是唯一允许 import 一切的包）。
  - `internal/llm` 是协议层，不得 import `internal/agent`、`internal/tools` 或 `internal/app`。
- **单一外部例外。** `native/` 是唯一允许使用第三方模块的地方，且该模块是 `github.com/egoist/mygo`（包括它的子包，如 `mygo/ui`）。`native/` 中任何其他外部 import 都会让测试失败。

关于严格性 / 白名单的说明：

- 所有 `.go` 文件都会被检查，包括 `_test.go`、平台/构建标签变体（`_windows.go`、`_linux.go` 等）以及生成的/mock 源码。没有静默例外。
- archguard 测试本身只导入标准库。
- 该测试只用标准库、不用 cgo、不添加模块，也从不编译它检查的包——它只解析 import，因此在任何宿主上都安全，也适用于交叉编译。

### 为什么

`AGENTS.md` 用自然语言陈述了这些规则。一个可执行的守卫能防止漂移：比如不小心把 UUID 或 CLI 库拉进 `internal/`、引入 cgo，或让协议层依赖 agent 循环，都会立刻破坏构建/测试，从而保住一个零依赖、可移植的核心。

### 运行

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"

go test ./internal/archguard -count=1   # architecture guard
go vet ./internal/archguard
gofmt -l internal/archguard             # must print nothing
```

## Release checklist

这是给从事 argentgo 本身工作的人看的内部工程笔记。**面向用户的文档（安装、快速上手、配置、命令、疑难解答）在根目录的 `README.md`**——不要在这里重复。`docs/` 下的文件用于设计决策、对齐跟踪和发布门禁。

### 依赖策略

- 引擎核心（`internal/{agent,session,llm,tools,security,...}`）保持只用标准库。`internal/app` 负责装配所有东西；库包接受接口（`ToolRunner`、`Approver`、`Recorder`、`Chatter`），而不是反向 import。
- `github.com/egoist/mygo` 是唯一显式例外，`native/` 用它实现 GPU 渲染的桌面壳。它通过标准库的 `--rpc` 传输与引擎通信。它的传递需求（`purego`、`golang.org/x/*`）是这一个模块的间接依赖，不是新的核心依赖。
- 唯一的桌面前端是 `native/`（MyGo）。浏览器前端及其 HTTP+SSE 桥已移除；`argent gui` 启动 `argent-native.exe`。`cmd/argent/gui.go` 只负责解析引擎并启动该二进制。
- 不使用 cgo。所有代码都必须能用 `GOOS=linux go build ./...` 交叉编译。
- 不要为了省几行代码而引入依赖。

### CI 门禁

`.github/workflows/ci.yml` 在每个 PR 和 push 到 main 时强制：

| 门禁 | 原因 |
| --- | --- |
| `gofmt -l .` | Windows runner 默认 `core.autocrlf=true`；CI 会先规范化它。 |
| `go vet ./...` | 静态正确性。 |
| `go test ./...` | 所有包的测试。 |
| `go test -race ./...` | 并发是发布门禁（session fd/ID 索引、broker channel、job 注册表）。跑在自带 MinGW 以支持 CGO 的 `windows-latest` 上。本地开发宿主没有 gcc，会跳过。 |
| `build (no cgo)` | 产出我们实际交付的 Windows/amd64 二进制。 |
| `cross-compile (linux/amd64, no cgo)` | 在每个 PR 上验证 `AGENTS.md` 的可移植性承诺，而不只是在本地。 |
| `ui`（ubuntu） | 对 `ui/` 执行 `bun install --frozen-lockfile`、类型检查、测试和构建。 |

race 和交叉编译是后来补上的，因为最初的 CI 只检查交付平台；并发和可移植性当时靠手工验证。把它们留在门禁里。

### 性能基线

目前还没有 `Benchmark*` 函数；在有基线之前加 bench 门禁只会是空操作。添加基准测试时：

```powershell
go test -run=^$ -bench=. -benchmem ./internal/...
```

把数字（连同 Go 版本、OS 和宿主）记录在这个文件里，这样回退才看得见。没有测得的基线，就不要追逐微优化。

### 发布门禁

权威的发布清单是 `docs/STATUS.md`；打包在 `.github/workflows/release.yml`。操作员**只交付 Windows/amd64**。`scripts/build-release.ps1` 是产物和 SHA-256 校验和的唯一事实来源。

### 发布清单步骤

按顺序执行；每个源文件位置的版本号必须保持一致：

1. **升级版本**：在 `internal/cli/cli.go`（`const Version`）、`internal/mcp/mcp.go`（`clientVersion`）和 `internal/lsp/client.go`（`initialize` 中的 `clientInfo.version`）里改。`scripts/build-release.ps1` 只读 `cli.go`，所以另外两处要手工同步。
2. **更新 `CHANGELOG.md`**，加一个 `## v<version>` 章节；一致性检查会拒绝缺少它的发布。
3. **在干净的树上做完整验证**：`gofmt -l .`、`go vet ./...`、`go test ./...`、`go test -race ./...`（Windows 宿主上需要 gcc/MinGW），以及无 cgo 交叉编译 `GOOS=linux GOARCH=amd64 go build ./...`。还要跑 `argent eval evals/core.json`、`argent gui` 端到端 smoke 测试，以及 `scripts/privacy-scan.ps1`（不得有密钥、token、真实邮箱或本地路径）。
4. **构建产物**：运行 `powershell -ExecutionPolicy Bypass -File scripts/build-release.ps1`——写出 `dist/release/argent_<version>_windows_amd64.zip`（两个二进制）、它的 `.sha256` 附带文件和 `checksums.txt`。`scripts/publish-win.ps1 -DryRun` 会重复所有门禁但不推送。
5. **提交、打标签、推送**：聚焦的提交，`git tag -a v<version>`，然后推送提交和标签。通过常规的 git 凭据助手认证（`git credential fill`）；这些文档和脚本里不存储任何凭据或 token。`scripts/publish-win.ps1` 可以自己完成打标签和推送。
6. **发布 Release**——要么手动创建 GitHub Release 并上传 zip 加 `checksums.txt`，要么让 `scripts/publish-win.ps1` 触发工作流并等待它。只有 tag 不算发布：如果发布的 Release 缺少任一资产，发布脚本会失败。
7. **更新本地桌面**：重新下载最新的 zip（或重跑 `scripts/install-win.ps1`），校验，并**同时**替换两个二进制，让桌面客户端保持最新版本。

## 发布验证

`scripts/test-release-workflow.ps1` 是针对 `.github/workflows/release.yml` 的**本地、离线**验证脚本。它通过检查工作流接线正确、且它运行的产物校验步骤确实能在发布形状的产物上成功，来补上"release.yml 从未被验证"的缺口。

> **诚实的范围说明：** 这是*部分*验证，不是端到端的发布测试。不涉及 GitHub Actions 运行、不涉及 `gh` CLI 发布、没有真实 tag、也没有真实 token。真正端到端的发布仍然需要推送一个 `v*` 标签，并让 GitHub Actions 用自动提供的 `GITHUB_TOKEN` 运行 `release.yml`。通过本验证脚本绝不能解读为"发布流水线已被端到端证明"。

### 如何运行

在仓库根目录，使用 `AGENTS.md` 中的工具链：

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # only needed for -FullBuild

# Default: fully offline, no Go build required.
powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1

# Optional: also run the real scripts/build-release.ps1 into a temp dir.
powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1 -FullBuild
```

默认路径的要求：Windows PowerShell 5.1 或 `pwsh`，仅此而已。`-FullBuild` 还需要可用的 Go 工具链以及 `go.mod` 里的模块已缓存（否则就不是离线的）。

脚本会为每条断言打印 `PASS`/`FAIL` 行，并在最后给出带 `RESULT: PASS` / `RESULT: FAIL` 行的总结。成功时退出 `0`，任一断言失败则退出 `1`。所有临时目录都在 `finally` 块中删除，即使断言失败也一样。

### 该脚本验证什么

#### 1. `release.yml` 的静态结构（按文本解析）

| 方面 | 断言 |
| --- | --- |
| job 图 | job `verify`、`race`、`release` 都已声明；`release` 有 `needs: [verify, race]` |
| 触发 | tag push 触发 `tags: ['v*']` |
| 最小权限 | 顶层 `permissions:` 带 `contents: read`；`release` job 覆盖为 `permissions: contents: write` |
| token 接线 | `release` 引用 `${{ secrets.GITHUB_TOKEN }}` 并从中设置 `GH_TOKEN`——只作为**字面占位字符串**断言；值从不被读取、解析或打印 |
| 质量门禁 | `verify` 在 `CGO_ENABLED='0'` 下运行 gofmt / `go vet` / `go test`；`race` 在 `CGO_ENABLED='1'` 下运行 `go test -race` |
| 调用的本地脚本 | `release` 调用 `check-release-consistency.ps1`、`build-release.ps1`、`verify-release.ps1`、`extract-release-notes.ps1` |
| 发布步骤 | `release` 运行带 `--verify-tag` 的 `gh release create`，然后用 `gh release view` 确认两个资产 |

每条针对具体 job 的断言都限定在该 job 的 YAML 块内，因此一个 job 上的设置无法满足本该由另一个 job 满足的检查。

#### 2. 本地产物验证

工作流的 "Verify release artifacts" 步骤会在一个临时目录上复现：

- **默认：** 伪造一个 `argent_<version>_windows_amd64.zip`，包含 `argent.exe` 和 `argent-native.exe`，外加一个匹配的 sha256sum 风格 `checksums.txt`——正是 `build-release.ps1` 产出的形状。
- **`-FullBuild`：** 先把真正的 `scripts/build-release.ps1 -SkipChecks -Tag v<version>` 跑进临时目录。

然后 `scripts/verify-release.ps1 -ArtifactDir <temp> -Version <version>` 在一个独立的 PowerShell 进程中运行（与工作流调用方式完全一致），必须退出 `0`。它会校验 zip 存在、其中两个二进制齐全，以及 `checksums.txt` 中的 SHA-256 与实际字节一致。

#### 3. 可选的 YAML 语法检查

如果 `python` 在 `PATH` 上，验证脚本还会尝试对 `release.yml` 执行 `yaml.safe_load` 并报告结果。缺少 Python 或 PyYAML 会报 `SKIP` 而绝不报 `FAIL`——文本断言才是权威的。真正的解析错误会报失败。

### 只有真实 tag 推送才能验证的内容

以下这些验证脚本有意**不**声称能验证：

- GitHub Actions 实际在 `windows-latest` / `ubuntu-latest` 上调度并运行 `verify` 和 `race` job，包括工具链设置和缓存。
- `release` job 的 `needs: [verify, race]` 门禁被 Actions 遵守。
- `GITHUB_TOKEN` 被签发、限定为 `contents: write` 并被 `gh release create` 接受；本地从不访问任何 token 值。
- `--verify-tag` 针对真实推送的 tag 和检出的 commit 解析。
- 真实的 `gh release create` 调用、网络往返和发布后的 `gh release view` 资产断言。
- 真实 tag 下的版本/tag 一致性（`check-release-consistency.ps1`），以及从该 tag 源码构建的 `native/` GUI 二进制。

### 发布 job 图

```
push tag v*  (on.push.tags: ['v*'])
        |
        +-------------------+-------------------+
        |                                       |
   verify (windows-latest)                race (ubuntu-latest)
   gofmt / go vet / go test               go test -race (no internal/pty)
   build windows + linux, CGO=0           CGO=1
        |                                       |
        +-------------------+-------------------+
                            |
                            v
              release (windows-latest)
              needs: [verify, race]
              permissions: contents: write
                            |
   1. check-release-consistency.ps1 -Tag $RELEASE_TAG
   2. build-release.ps1 -Tag $RELEASE_TAG -SkipChecks
   3. verify-release.ps1 -ArtifactDir dist/release -Version $version
   4. extract-release-notes.ps1 -Tag $RELEASE_TAG -OutFile release-notes.md
   5. GH_TOKEN=secrets.GITHUB_TOKEN gh release create ... --verify-tag
   6. gh release view --json assets  (assert both assets present)
```

步骤 2–3 是本验证脚本复现的本地部分。步骤 1 和 4–6 需要真实的 checkout/tag/token，这里只做结构断言。

### 隐私与安全

- 验证脚本和本文档中都不含任何密钥、token、凭据或私有文件系统路径。token 只作为用于断言的字面 YAML 占位符 `${{ secrets.GITHUB_TOKEN }}` 出现，其值从不被读取或打印。
- 验证脚本从不联网，从不调用 `gh`。
- 它只写入一个唯一命名的临时目录，并在 `finally` 中删除；不触碰 `dist/` 或工作树。

### 故障排查

- 静态断言失败：`release.yml` 偏离了带门禁的布局。修工作流，而不是修验证脚本（脚本有意很严格）。
- `verify-release.ps1 exited 0` 失败：伪造/真实产物不是发布形状的；检查打印出来的校验器输出，以及 `internal/cli/cli.go` 中的 `Version` 常量。
- `-FullBuild` 构建步骤失败：检查 Go 工具链、缓存模块和 `native/` 依赖；默认的离线运行仍是接线是否正确的权威检查。
- `python yaml.safe_load` 报告失败（不是 `SKIP`）：`release.yml` 有文本检查抓不到的真实 YAML 语法错误。
