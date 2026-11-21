package shell

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
	clipped := clip(ctx, sanitizeShellOutput(raw))
	if len(raw) <= limit {
		return clipped
	}
	return fmt.Sprintf("[output: ~%.1fk tokens, clipped]\n%s",
		float64(bashOutputTokens(raw))/1000, clipped)
}

type bashTool struct{}

func (bashTool) Name() string        { return "bash" }
func (bashTool) Description() string { return "Run a shell command in the workspace root." }
func (bashTool) Mutating() bool      { return true }
func (bashTool) Schema() string {
	return `{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"},"background_after_ms":{"type":"integer","description":"If the command is still running after this many milliseconds, promote it to a background job instead of killing it on timeout."}},"required":["command"]}`
}

func (bashTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Command           string `json:"command"`
		TimeoutSeconds    int    `json:"timeout_seconds"`
		BackgroundAfterMS int    `json:"background_after_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("bash: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("bash: command must not be empty")
	}
	var requested time.Duration
	if args.TimeoutSeconds > 0 {
		requested = time.Duration(args.TimeoutSeconds) * time.Second
	}
	timeout := clampTimeout("bash", requested, maxTimeout(ctx))

	// 自研内建 shell：仅当显式 opt-in 且命令在受支持子集内时接管。
	// 默认（开关关闭 / 命令含不支持语法）走原有系统 shell 路径，逐字节不变。
	if builtinShellEnabled() && Supported(args.Command) {
		return runBuiltinShell(ctx, root, args.Command, timeout)
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
const cmdChcpPrefix = "chcp 65001>nul&&"

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

// builtinShellEnabled 报告是否启用自研内建 shell 兜底。
// 默认关闭：只有 ARGENT_BUILTIN_SHELL=1 才启用，确保现有系统 shell 路径逐字节不变。
func builtinShellEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_BUILTIN_SHELL")) == "1"
}

// runBuiltinShell 用自研解释器执行命令。输出仍走既有的清洗/裁剪管线
// （clipBashOutput），非零退出转为含退出码的 error，保持与系统 shell 路径一致的可观察语义。
func runBuiltinShell(ctx context.Context, root, command string, timeout time.Duration) (string, error) {
	opts := Options{
		Cwd:     root,
		Vars:    builtinShellEnv(),
		Timeout: timeout,
		Ctx:     ctx,
	}
	result, err := Run(command, opts)
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
