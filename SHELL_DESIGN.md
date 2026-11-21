# 自研内部 Shell 解释器设计（仅思想，零第三方依赖）

> 目标：纯 Go 标准库、无 cgo、单二进制。无系统 bash 时作为 `tools_builtin_bash.go` 的执行能力兜底。
> 参考项目（只学思想，绝不抄源码/引依赖）：crush、dsh、oh-my-pi、opencode/kilocode、mvdan.cc/sh、codex。

## 1. 定位：执行能力兜底，不是 bash 语义替代

- **失败关闭（fail-closed）**：只实现有把握的语法子集；遇到不支持的语法（循环/函数/case/作业控制/进程替换/算术/heredoc/复合命令）直接返回明确错误（退出码 127 或解析错误），绝不"近似解释"——近似解析会产生解析差分，被注入内容利用绕过审批（opencode 血泪教训）。
- **解析与执行用同一套语义**：lexer/parser 只建结构，值展开全在执行期（mvdan 分层铁律）。
- **上层工具零改动**：参考 dsh 的 `child_process shim` 思想——fallback 做在能力/平台边界，`tools_builtin_bash.go` 的安全链（Registry.Run → Guard → Chain）保持不变。
- **能力边界"命名出来、不藏起来"**：工具描述声明"本 shell 不是 bash"；不支持的能力用稳定契约表达（command not found / not available），不可用的参数（如沙箱权限）报错而非假装支持。

## 2. 包结构与依赖方向

```
internal/shell/           新建，纯 stdlib，与 internal/proc 同级
  lexer.go       rune 级词法；三态 normal/sgl/dbl + 嵌套栈(param/cmdsubst)
  ast.go         File/Stmt/CallExpr/Word/WordPart/Redirect/BinaryCmd + Pos
  parser.go      递归下降；只产结构不做替换
  expand.go      执行期展开：$VAR/${VAR:-def}/$?/$(cmd) + IFS 分词 + glob + quote removal
  interp.go      执行引擎：BinaryCmd 短路/管道；维护 cwd/env/最后退出码
  builtins.go    map 注册表：cd/pwd/echo/export + 可选 unset/true/false
  coreutils/     ls/cat/grep/wc/head/tail/mkdir/rm（stdlib 自写简化版）
  safety.go      消费方定义 SafetyOracle func(string) bool，由 app 注入（不 import security）
  doc.go         能力边界文档：支持/不支持语法清单
```

- **允许**：`internal/shell` → stdlib；`internal/shell` → `internal/proc`（进程执行）。
- **禁止**：`internal/shell` → `internal/tools`/`internal/security`/`internal/agent`（会成环/违反分层）。
- 安全咨询用函数值注入：`type SafetyOracle func(command string) bool`，app 装配时传 `security.FindFileEditingCommand != ""`（消费方定义类型、依赖在调用点注入，符合 AGENTS.md）；nil 时不咨询。

## 3. 语法子集（严格限定）

### 支持
- 简单命令 + 参数；引号：`'...'`（不展开）、`"..."`（展开）、`\` 转义
- 列表/逻辑：`;` `&&` `||`；管道 `|`
- 重定向：`>` `>>` `<` `2>&1`（fd 仅 0/1/2）
- 展开：`$VAR` `${VAR:-default}` `$?` `$(cmd)`（限深）；glob `* ? [...]`
- 内建：cd/pwd/echo/export；coreutils：ls/cat/grep/wc/head/tail/mkdir/rm

### 不支持（遇到即明确拒绝，不猜测）
循环（for/while）、函数、case、if、子 shell `( )`、算术 `$(( ))`、进程替换、heredoc、后台 `&`、作业控制、`${VAR}` 高级修饰符、数组、别名、trap。

## 4. 展开顺序（颠倒即 bug，mvdan 铁律）
1. 解析期：只建 AST，`$VAR`/`$(cmd)`/`${...}` 记为 WordPart
2. 执行期：参数/命令替换 → IFS 分词 → glob → quote removal
3. 上下文矩阵：同一 `$f` 在命令参数（分词+glob）、重定向目标（不分词不 glob）、双引号内（不分词不 glob）、单引号内（完全不展开）走不同入口
4. 命令替换：收集输出后 `TrimRight("\n")`，剔除 NUL

## 5. 执行引擎
- `BinaryCmd{Op: And|Or|Pipe, X, Y *Stmt}`：`&&/||` 短路；管道用 `os.Pipe` + 左段 goroutine + 右段同步 + WaitGroup 汇合（mvdan 模式，非全 goroutine）；关闭写端防 hang。
- 外部命令：复用 `internal/proc` 的 `Command/Result`（进程组 kill、超时、UTF-8 carry writer、输出上限）；非零退出是 Result.ExitCode 不是 error。
- 内建命令：命中即短路，I/O 全走参数（io.Reader/Writer）以支持管道。
- 超时/取消：全程 context；首因分类 spawn-error > aborted > timeout > signaled > exit（codex/proc 思想）。

## 6. 输出治理
- 标记语言（dsh/codex）：`[exit code: N]` / `[timed out]` / `[killed by signal: X]` / `[truncated]` / `[full output: path]`，各事实正交独立报告。
- 有界：保留头+尾 + UTF-8 边界裁剪；超限落盘给恢复路径。
- 环境：注入 `NO_COLOR=1/TERM=dumb/PAGER=cat/GIT_PAGER=cat`（面向模型稳定纯文本）；剥离凭据。

## 7. 安全分工
- shell 层：语法解析 fail-closed + cwd 设为 root + 管道/重定向正确建模 + 安全咨询谓词（可选）。
- 安全层（不碰）：`Registry.Run → checkGuard → Chain.Check`（策略→审批→审计）保持原样；`bashTool.Mutating()==true` 不变；工具名 `bash` 不变。
- 绝不：新建绕过 Guard 的执行入口；把命令从 Guard 可见字段隐藏；自称有工作区路径沙箱（bash 无 path 字段，cwd 不是边界）。

## 8. 接线点（精确）
`internal/tools/tools_builtin_bash.go:126 shellInvocation(command) (path, argv)` 是唯一接线点：
- 系统 shell 可用时：保持现有 `bash -lc`/`sh -lc`/`cmd /c` 行为（逐字节不变）。
- 系统 shell 不可用时：返回内建解释器的执行形态（go 进程内运行），命令原文仍来自 Guard 可见的 `command` 字段。
- 注意 split-brain：`proc.Shell`（proc.go:442）另有检测（workflow 用），两处 fallback 需一致。
