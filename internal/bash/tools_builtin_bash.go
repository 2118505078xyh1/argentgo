package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/proc"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

// ---- bash ----

// toolProgressKey 镜像 agent.toolProgressKey。两个包不能共享
// 带类型的 context key，除非 tools 导入 agent，而分层规则
// 禁止这样做，因此双方约定使用这个字面量和普通的 func(string) 值。
const toolProgressKey = "argent.tool.progress"

// emitProgress 通过 agent 循环安装在 ctx 中的发射器发布一条
// 中间进度行。没有发射器时它是静默的空操作，
// 且循环的门控会在工具调用返回后丢弃该行。因此，一个存活期超过 bashTool.Run 的
// shell 作业无法追加陈旧的进度行。
func emitProgress(ctx context.Context, text string) {
	emitter, _ := ctx.Value(toolProgressKey).(func(string))
	if emitter != nil {
		emitter(text)
	}
}

// bashOutputTokens 估算原始命令输出的 token 成本。它是
// 共享的 internal/tokenize 估算器，因此体量提示使用的单位与
// agent 的预算循环一致，且无需导入 internal/agent。
func bashOutputTokens(s string) int {
	return tokenize.Estimate(s)
}

// clipBashOutput 清洗并裁剪原始命令输出，且仅在内容
// 确实被丢弃时，加上简短的体量提示，使用户和模型
// 能看到命令产生内容的上下文成本。它是纯文本
// 改动：绝不触碰退出状态、错误路径或裁剪阈值。
//
// 它有意接收原始输出。先清洗会折叠连续
// 相同的行，基于折叠后的文本判断既低估了
// 体量，也恰恰跳过了最需要提示的重复输出的提示
// （3000 行相同内容压缩成一行，于是预算检查不再
// 触发）。因此大小和估算都来自原始输出；只有送达
// 模型的正文被清洗。
func clipBashOutput(ctx context.Context, raw string) string {
	limit := outputLimit(ctx)
	if limit <= 0 {
		limit = maxToolOutput
	}
	clean := sanitizeShellOutput(raw)
	var clipped string
	if outputCollapseEnabled() {
		// opt-in 分支：用行折叠原语替代 clip。字符预算取当前输出上限，
		// 行预算交给原语默认值（见 collapseOutput）。这里有意跳过 clip
		// （含 spill 回调），因为折叠帧本身就是输出的处理结果。
		clipped = collapseOutput(clean, 0, limit).Text
	} else {
		clipped = clip(ctx, clean)
	}
	if len(raw) <= limit {
		return clipped
	}
	return fmt.Sprintf("[output: ~%.1fk tokens, clipped]\n%s",
		float64(bashOutputTokens(raw))/1000, clipped)
}

// outputCollapseEnabled 报告是否启用新的行折叠输出帧。
// 默认关闭：只有 ARGENT_OUTPUT_COLLAPSE=1 才启用，确保关闭时
// clipBashOutput 仍走原有的 clip 路径，输出逐字节不变。
func outputCollapseEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_OUTPUT_COLLAPSE")) == "1"
}

// ---- 行折叠原语（薄转发到 internal/textutil）----
//
// 过去本文件内嵌了一份与 internal/tools.CollapseOutput 逐字相同的镜像实现：
// 因为 tools 通过 builtin_register.go import 了 shell，shell 反向 import tools
// 会形成 import 环。该镜像违反了"依赖解环：类型/原语下沉低层包"的约束，且两份
// 实现必须人工同步。现把算法下沉到零依赖的 internal/textutil，shell 与 tools
// 各自做薄转发：算法只有一个定义，行为逐字节不变，也未引入 import 环。

// collapseResult 是 textutil.CollapseResult 的别名，保留 shell 侧既有 API。
type collapseResult = textutil.CollapseResult

// collapseOutput 按行数与字符数双重预算折叠 s，语义见 textutil.CollapseOutput：
// 双约束、保留尾部错误行、插入 [N earlier lines] 标记、rune 安全、参数钳制与幂等。
func collapseOutput(s string, maxLines, maxChars int) collapseResult {
	return textutil.CollapseOutput(s, maxLines, maxChars)
}

type bashTool struct{}

func (bashTool) Name() string        { return "bash" }
func (bashTool) Description() string { return "Run a shell command in the workspace root." }
func (bashTool) Mutating() bool      { return true }
func (bashTool) Schema() string {
	// 统一 exec 会话关闭时返回原始 schema，逐字节保持既有工具契约。
	if !unifiedExecEnabled() {
		return `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."}},"required":["command"]}`
	}
	return `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."},"session_id":{"type":"string","description":"Existing unified exec session to write stdin to; omit to start a new session."},"stdin":{"type":"string","description":"Initial input for a new session, or input for an existing session_id."},"tty":{"type":"boolean","description":"Attach a pseudoterminal so interactive programs see a TTY."},"yield_time_ms":{"type":"integer","description":"Maximum time to wait for output before returning a session_id."}},"required":["command"]}`
}

func (bashTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Command           string `json:"command"`
		TimeoutSeconds    int    `json:"timeout_seconds"`
		BackgroundAfterMS int    `json:"background_after_ms"`
		// 以下四个字段仅在 ARGENT_EXEC_SESSION=1 时生效；关闭时被忽略，
		// 原有解析与执行路径逐字节不变。
		SessionID   string `json:"session_id"`
		Stdin       string `json:"stdin"`
		TTY         bool   `json:"tty"`
		YieldTimeMS int    `json:"yield_time_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("bash: bad arguments: %w", err)
	}
	// 写入已有会话时允许省略 command；仅在 opt-in 开启时放宽，
	// 确保默认路径的空命令校验逐字节不变。
	allowEmptyCommand := unifiedExecEnabled() && args.SessionID != ""
	if strings.TrimSpace(args.Command) == "" && !allowEmptyCommand {
		return "", fmt.Errorf("bash: command must not be empty")
	}

	// 统一 exec 会话（opt-in ARGENT_EXEC_SESSION=1）：只有显式带了
	// session_id / stdin / tty / yield_time_ms 之一才接管，避免开启开关
	// 后悄悄改变普通一次性命令的既有语义。
	if unifiedExecEnabled() && (args.SessionID != "" || args.Stdin != "" || args.TTY || args.YieldTimeMS > 0) {
		return runBashViaExecSession(ctx, root, unifiedExecRegistry(), args.Command,
			args.SessionID, args.Stdin, args.TTY, args.YieldTimeMS)
	}

	var requested time.Duration
	if args.TimeoutSeconds > 0 {
		requested = time.Duration(args.TimeoutSeconds) * time.Second
	}
	timeout := clampTimeout("bash", requested, maxTimeout(ctx))

	// 持久化 shell 会话（opt-in ARGENT_PERSISTENT_SHELL=1）：当注册表注入了
	// 会话管理器时，命令在长驻 shell 中执行以复用 cd / export / venv 等状态。
	// 默认（开关关闭或未注入）走下方原路径逐字节不变；workspace 用工具的
	// root 参数（即工作区根目录）。
	if persistentShellEnabled() {
		if sessions := persistentSessions(ctx); sessions != nil {
			return runBashViaPersistent(ctx, root, sessions, args.Command, timeout)
		}
	}

	path, argv := shellInvocation(args.Command)

	// 提升是 opt-in 的，且需要有地方记录作业。没有
	// 注册表（或参数）时，保持原有的硬杀死行为。
	if args.BackgroundAfterMS > 0 {
		if registry := jobRegistry(ctx); registry != nil {
			return runPromotableBash(ctx, root, registry, args.Command, path, argv,
				timeout, time.Duration(args.BackgroundAfterMS)*time.Millisecond)
		}
	}

	// 自研内建 shell：默认启用（ARGENT_BUILTIN_SHELL=0 为紧急关闭），且仅在
	// 命令处于受支持子集时接管。它排在持久化会话与后台提升之后，但必须先于
	// proc 硬化路径——否则硬化作为默认值会抢占内建 shell 的安全咨询（oracle）。
	// 含不支持语法的命令则继续走下方硬化/系统 shell 路径。
	// 含不支持语法的命令则继续走下方系统 shell 路径。
	//
	// 运行期安全网：若解释器冒出 ErrUnsupported（静态预检无法判定的动态构造，
	// 例如命令替换展开出保留字当程序名），ShouldFallback 判定为真，此时用
	// **原始命令行整条回退**下方系统 shell，绝不在部分执行后再回退造成
	// 重定向/写入等副作用重复。其余错误（非零退出、命令未找到等）保持内建路径语义。
	if builtinShellEnabled() {
		if parsed, ok := ParseForRun(args.Command); ok {
			out, err := runBuiltinParsed(ctx, root, args.Command, timeout, parsed)
			if err == nil || !ShouldFallback(err) {
				return out, err
			}
		}
		// 整条回退：继续执行下方硬化/系统 shell 路径，args.Command 逐字节不变。
	}

	// 默认 bash 路径的进程硬化是安全默认值（TODO #27）：走 proc.Run，
	// 获得有界捕获、进程组杀树、环境净化与跨分块 UTF-8 重组；
	// 仅显式 ARGENT_BASH_USE_PROC=0 时保持下方的 CombinedOutput 兼容路径。
	if bashUseProcEnabled() {
		return runBashViaProc(ctx, root, path, argv, timeout)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, argv...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	text := clipBashOutput(ctx, string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("bash: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(string(output), text), nil
	}
	return text, nil
}

// cmdChcpPrefix 在 cmd 执行命令前把控制台代码页固定为 65001（UTF-8）。
// chcp 的回显被重定向到 nul，因此不会在命令输出里多出一行。
// 用单个 &（而非 &&）串联：&& 会在 chcp 失败（无控制台、权限不足、
// 异常代码页等）时静默跳过原命令；& 则无论 chcp 成功与否都执行原命令，
// 从而解耦代码页切换与命令执行，同时保持原命令字节逐字不变。
const cmdChcpPrefix = "chcp 65001>nul&"

// shellInvocation 返回执行某条命令行所用的程序和前置参数。
// 它由硬超时和可提升路径共用，使同一命令在两处的
// 拼写方式一致。
func shellInvocation(command string) (string, []string) {
	return shellInvocationFor(runtime.GOOS, exec.LookPath, command)
}

// shellInvocationFor 是 shellInvocation 的可注入版本：把平台与程序探测
// 作为参数，测试可用假的 lookPath 覆盖 Windows 下的 bash/cmd 分支，
// 无需真实 Windows 主机即可捕获各分支的 argv。生产路径与直接调用
// shellInvocation 完全等价。
func shellInvocationFor(goos string, lookPath func(string) (string, error), command string) (string, []string) {
	if goos == "windows" {
		if _, err := lookPath("bash"); err == nil {
			return "bash", []string{"-lc", command}
		}
		// cmd 默认使用 OEM 代码页（简体中文 936/GBK），外部程序按它写出的
		// 中文会在模型侧变成乱码。先切到 65001 再执行原命令：UTF-8 与
		// ASCII 逐字节兼容，所以 ASCII 输出保持不变。
		//
		// 用 /s /c 强制 cmd 走确定性的"去掉首尾引号"剥离规则；且不在
		// 命令外添加任何包裹字符（如括号或额外引号），原命令——包括
		// 以引号开头、含引号、含 ')' 的情形——逐字节原样保留，从而
		// 不会因为前缀而改变 /c 对引号开头命令的剥离语义。
		return "cmd", []string{"/s", "/c", cmdChcpPrefix + command}
	}
	return "sh", []string{"-lc", command}
}

// builtinShellEnabled 报告是否启用自研内建 shell。
// 默认启用：只有 ARGENT_BUILTIN_SHELL=0（首尾空白修剪后）才紧急关闭，
// 其它值（含未设置、空串、1、true）均视为开启，回到默认内建解释器路径。
func builtinShellEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_BUILTIN_SHELL")) != "0"
}

// persistentShellEnabled 报告是否启用持久化 shell 会话。
// 默认关闭：只有 ARGENT_PERSISTENT_SHELL=1 才接管，确保关闭时命令走
// 原有系统 shell 路径逐字节不变。
func persistentShellEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_PERSISTENT_SHELL")) == "1"
}

// persistentShellKind 返回持久化会话使用的 shell 种类，与 shellInvocation
// 的平台选择保持一致：POSIX 用 bash；Windows 优先 bash，找不到时退到
// powershell（持久化内核阶段1不支持 cmd）。goos 与 lookPath 可注入以便测试。
func persistentShellKind(goos string, lookPath func(string) (string, error)) string {
	if goos == "windows" {
		if _, err := lookPath("bash"); err == nil {
			return "bash"
		}
		return "powershell"
	}
	return "bash"
}

// runBashViaPersistent 在长驻 shell 会话中执行命令。语义对齐默认路径：
// 输出走既有清洗/裁剪管线，超时优先返回专用错误，非零退出转成含退出码的
// error，空输出回落 bashNoOutput。err 仅表示传输/协议错误，与退出码分开。
func runBashViaPersistent(ctx context.Context, root string, sessions shellSessionExec,
	command string, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	kind := persistentShellKind(runtime.GOOS, exec.LookPath)
	stdout, code, err := sessions.Exec(runCtx, root, kind, command)
	text := clipBashOutput(ctx, stdout)
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("bash: %w", err)
	}
	if code != 0 {
		return text, fmt.Errorf("bash: exit status %d", code)
	}
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(stdout, text), nil
	}
	return text, nil
}

// bashUseProcEnabled 报告默认 bash 主路径是否走 proc.Run 硬化实现。
// TODO #27：硬化是安全默认值（有界捕获、进程树终止、环境净化、UTF-8 重组），
// 因此默认开启；只有显式 ARGENT_BASH_USE_PROC=0 才回退到旧的
// exec.Cmd.CombinedOutput 路径（用于需要 stdout/stderr 实时交错的兼容场景）。
func bashUseProcEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_BASH_USE_PROC")) != "0"
}

// runBuiltinShell 用自研解释器执行命令。输出仍走既有的清洗/裁剪管线
// （clipBashOutput），非零退出转为含退出码的 error，保持与系统 shell 路径一致的可观察语义。
func runBuiltinShell(ctx context.Context, root, command string, timeout time.Duration) (string, error) {
	f, ok := ParseForRun(command)
	if !ok {
		return "", fmt.Errorf("bash: unsupported command")
	}
	return runBuiltinParsed(ctx, root, command, timeout, f)
}

// runBuiltinParsed 复用已解析的 AST，避免与 Supported 探测重复 lex+parse。
func runBuiltinParsed(ctx context.Context, root, command string, timeout time.Duration, f *File) (string, error) {
	opts := Options{
		Cwd:     root,
		Vars:    builtinShellEnv(),
		Timeout: timeout,
		Ctx:     ctx,
		// 外层工具守卫已用原始命令行字符串审过一次。只有当展开后的真实 argv
		// 与原字符串不同时，才需要再咨询 oracle——这正是旁路咨询的价值
		// （如 `echo "danger"` 展开为 `echo danger`）。字符串相同则跳过，
		// 避免默认开启内建 shell 后同一个审批器被重复询问。
		Safety: func(expanded string) bool {
			if expanded == command {
				return true
			}
			return CurrentSafety(nil, expanded)
		},
	}
	it := &Interp{
		Vars:    opts.Vars,
		Cwd:     opts.Cwd,
		Safety:  opts.Safety,
		Timeout: opts.Timeout,
		Ctx:     opts.Ctx,
		History: []string{command},
	}
	out, err := it.Run(f)
	result := Result{Output: out, ExitStatus: it.LastStatus}
	if err != nil {
		// 不支持的语法等结构错误：交给模型可读的错误，便于自我修正。
		return "", fmt.Errorf("bash: %w", err)
	}
	text := clipBashOutput(ctx, result.Output)
	if result.ExitStatus != 0 {
		return text, fmt.Errorf("bash: exit status %d", result.ExitStatus)
	}
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(result.Output, text), nil
	}
	return text, nil
}

// builtinShellEnv 为内建 shell 构造变量表（当前进程环境 + 面向模型的稳定输出覆盖）。
func builtinShellEnv() map[string]string {
	vars := map[string]string{}
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i > 0 {
			vars[entry[:i]] = entry[i+1:]
		}
	}
	// 面向模型的纯文本输出（抑制彩色/分页），与现有工具约定一致。
	vars["NO_COLOR"] = "1"
	vars["TERM"] = "dumb"
	vars["PAGER"] = "cat"
	vars["GIT_PAGER"] = "cat"
	return vars
}

// bashProcCommand 构建硬化路径执行的 proc.Command。它与 Run 分开保留，
// 使进程组杀树与隐藏窗口的接线可以在单元测试中直接验证。
func bashProcCommand(path string, argv []string, root string) proc.Command {
	return proc.Command{
		Path:     path,
		Args:     argv,
		Dir:      root,
		KillTree: true,
		NoWindow: runtime.GOOS == "windows",
	}
}

// runBashViaProc 是默认 bash 路径的 opt-in 硬化实现：用 proc.Run 取代
// exec.Cmd.CombinedOutput，获得每流 64KB 有界捕获、整棵进程树终止、
// 宿主私有环境变量净化，以及跨读取分块的 UTF-8 重组。
//
// 已知差异：exec.Cmd.CombinedOutput 把 stdout/stderr 实时交错写入同一个
// 缓冲区，而 proc.Result.Combined() 是 stdout 段后接 stderr 段的拼接。
// 启用该开关即接受此顺序差异，并由测试固定这一契约。
// 退出/超时语义与下方原路径保持一致：超时优先返回专用错误，
// 非零退出还原 exec.ExitError 提供的 "bash: exit status N"。
func runBashViaProc(ctx context.Context, root, path string, argv []string, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := proc.Run(runCtx, bashProcCommand(path, argv, root))

	text := clipBashOutput(ctx, res.Combined())
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("bash: %w", err)
	}
	if res.ExitCode != 0 {
		// proc.Run 把非零退出记在 Result.ExitCode 而非 error 中，
		// 这里显式还原原 CombinedOutput 路径的退出码错误语义。
		return text, fmt.Errorf("bash: exit status %d", res.ExitCode)
	}
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(res.Combined(), text), nil
	}
	return text, nil
}

// runPromotableBash 最多等待 backgroundAfter 时间。到该
// 截止时间时，仍在运行的子进程被交给作业注册表；模型
// 得到一个带作业 id 的普通工具结果，因此回合永不被阻塞。
func runPromotableBash(ctx context.Context, root string, registry *jobs.Registry,
	command, path string, argv []string, hardTimeout, backgroundAfter time.Duration) (string, error) {
	// 调用方的 context 和硬超时仍然约束整个调用；
	// 软截止时间才是把慢子进程变成作业的原因。
	runCtx, cancel := context.WithTimeout(ctx, hardTimeout)
	defer cancel()

	res, live, err := proc.RunWithTimeout(runCtx, proc.Command{
		Path:          path,
		Args:          argv,
		Dir:           root,
		Timeout:       backgroundAfter,
		KeepOnTimeout: true,
		NoWindow:      runtime.GOOS == "windows",
	})

	var timeoutErr *proc.TimeoutError
	if errors.As(err, &timeoutErr) && live != nil {
		logFile, fileErr := os.CreateTemp("", "argent-job-*.log")
		if fileErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: create job log: %w", fileErr)
		}
		logPath := logFile.Name()
		// 冲刷超时前捕获的内容，并把子进程其余的输出
		// 直接送往作业将要 tail 的日志。
		live.SetOutput(logFile)
		// 作业监视器读取同一个 Done 通道；它一关闭，所有的
		// 日志写入就已完成，文件可以释放。这个
		// goroutine 的存活期超过 bashTool.Run：它的进度行被有意
		// 经由带门控的发射器路由，使迟到的更新无法在工具结果
		// 已被记录后到达记录。
		go func() {
			<-live.Done()
			emitProgress(ctx, "bash job finished: "+command)
			_ = logFile.Close()
		}()

		job, jobErr := registry.PromoteFromTimeout(command, live, logPath)
		if jobErr != nil {
			live.Kill()
			return "", fmt.Errorf("bash: promote job: %w", jobErr)
		}
		out := fmt.Sprintf("started background job %s (pid %d): %s\nstill running; check with `argentgo jobs` (output: `argentgo jobs tail %s`).",
			job.ID, job.PID, command, job.ID)
		if partial := strings.TrimSpace(res.Combined()); partial != "" {
			out += "\nOutput so far:\n" + clipBashOutput(ctx, partial)
		}
		return out, nil
	}

	if err != nil {
		return clipBashOutput(ctx, res.Combined()), fmt.Errorf("bash: %w", err)
	}
	text := clipBashOutput(ctx, res.Combined())
	if strings.TrimSpace(text) == "" {
		return bashNoOutput(res.Combined(), text), nil
	}
	return text, nil
}
