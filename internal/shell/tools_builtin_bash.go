package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// ---- 行折叠原语（internal/tools.CollapseOutput 的 shell 侧镜像）----
//
// 为什么在本文件复制而不是直接 import：internal/tools 通过 builtin_register.go
// import 了 internal/shell，shell 反向 import tools 会形成 import 环（go build 会失败）。
// 因此这里放一份逐字对齐 internal/tools/output_collapse.go 的实现：当工具输出同时
// 超过行数与字符预算时，保留尾部行（错误常在最后几行）与头部少量上下文，中间用
// [N earlier lines] 标记被省略的行数。修改本段时必须同步该源文件。
//
// 纯函数、纯标准库：不读写文件、不读取进程级配置、不改动入参、结果确定，可并发调用。

const (
	// collapseDefaultLines 是 maxLines<=0 时使用的默认行数预算。
	collapseDefaultLines = 200
	// collapseDefaultChars 是 maxChars<=0 时使用的默认字符（rune）预算。
	collapseDefaultChars = maxToolOutput

	// 标记文本为 "[" + N + " earlier lines]"，全部为 ASCII。
	collapseMarkerHead = "["
	collapseMarkerTail = " earlier lines]"
)

// collapseResult 是 collapseOutput 的结果。FullPath 预留给全文落盘接线点，
// 本实现恒返回空字符串。
type collapseResult struct {
	Text       string
	EarlyLines int
	FullPath   string
}

// collapseOutput 按行数与字符数双重预算折叠 s（与 tools.CollapseOutput 语义一致）。
//
//   - maxLines<=0 / maxChars<=0 分别用 collapseDefaultLines / collapseDefaultChars；
//   - "chars" 按 rune 计，所有切点落在 rune 边界，绝不切坏 UTF-8；
//   - 未超限时原样返回（Text==s, EarlyLines==0）；
//   - 超限时优先保留尾部行，其次保留头部少量上下文，中间插入 [N earlier lines]；
//   - 不改动入参、不产生 I/O、结果确定，可并发调用。
func collapseOutput(s string, maxLines, maxChars int) collapseResult {
	if maxLines <= 0 {
		maxLines = collapseDefaultLines
	}
	if maxChars <= 0 {
		maxChars = collapseDefaultChars
	}
	if s == "" {
		return collapseResult{Text: s}
	}

	lines, trailingNL := collapseSplitLines(s)
	totalLines := len(lines)
	if totalLines == 0 {
		return collapseResult{Text: s}
	}
	if totalLines <= maxLines && utf8.RuneCountInString(s) <= maxChars {
		return collapseResult{Text: s}
	}

	// 标记独占一行：行预算减 1；字符预算为标记宽度与尾随换行做保守预留。
	lineBudget := maxLines - 1
	if lineBudget < 0 {
		lineBudget = 0
	}
	markerReserve := len(collapseMarkerHead) + collapseDigits(totalLines) + len(collapseMarkerTail)
	charBudget := maxChars - markerReserve
	if trailingNL {
		charBudget--
	}
	if charBudget < 0 {
		charBudget = 0
	}

	// 尾部约占行预算的 3/4，头部约占 1/4；字符预算由尾部优先使用。
	tailTarget := (lineBudget*3 + 3) / 4
	headTarget := lineBudget - tailTarget

	// 先挑尾部：每个被选行贡献"自身 rune 数 + 1 个行分隔符"。
	tailStart := totalLines
	tailCost := 0
	for totalLines-tailStart < tailTarget && tailStart > 0 {
		c := utf8.RuneCountInString(lines[tailStart-1]) + 1
		if tailCost+c > charBudget {
			break
		}
		tailCost += c
		tailStart--
	}
	tailCount := totalLines - tailStart

	// 极端情况：单行就超过字符预算时，仍保留最后一行末尾（错误常在行尾），
	// 做 rune 安全截断，避免输出只剩标记而丢失错误信息。
	if tailCount == 0 && lineBudget > 0 && charBudget > 1 {
		last := collapseKeepTailRunes(lines[totalLines-1], charBudget-1)
		lines[totalLines-1] = last
		tailStart = totalLines - 1
		tailCost = utf8.RuneCountInString(last) + 1
		tailCount = 1
	}

	// 再挑头部少量上下文：不与尾部重叠，只能用剩余字符预算。
	headEnd := 0
	headCost := 0
	for headEnd < headTarget && headEnd < tailStart {
		c := utf8.RuneCountInString(lines[headEnd]) + 1
		if headCost+tailCost+c > charBudget {
			break
		}
		headCost += c
		headEnd++
	}

	// 顶部补足：头部未占满的行槽位让给尾部，但 head+tail 不得超过行预算，
	// 且总字符数不得超过字符预算。
	for tailStart > headEnd &&
		headEnd+(totalLines-tailStart) < lineBudget &&
		headCost+tailCost+utf8.RuneCountInString(lines[tailStart-1])+1 <= charBudget {
		tailCost += utf8.RuneCountInString(lines[tailStart-1]) + 1
		tailStart--
	}
	tailCount = totalLines - tailStart

	earlier := totalLines - headEnd - tailCount
	if earlier < 0 {
		earlier = 0
	}

	var b strings.Builder
	for i := 0; i < headEnd; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
	}
	if headEnd > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(collapseMarkerHead)
	b.WriteString(strconv.Itoa(earlier))
	b.WriteString(collapseMarkerTail)
	for i := tailStart; i < totalLines; i++ {
		b.WriteByte('\n')
		b.WriteString(lines[i])
	}
	if trailingNL {
		b.WriteByte('\n')
	}

	return collapseResult{Text: b.String(), EarlyLines: earlier}
}

// collapseSplitLines 按 '\n' 切分（'\n' 不会出现在多字节 rune 内部，故 rune 安全）。
// 末尾换行被视为终止符，而非一个空行。
func collapseSplitLines(s string) (lines []string, trailingNL bool) {
	if s == "" {
		return nil, false
	}
	lines = strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		trailingNL = true
		lines = lines[:len(lines)-1]
	}
	return lines, trailingNL
}

// collapseDigits 返回 n 的十进制位数；n<=0 记为 1 位。
func collapseDigits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

// collapseKeepTailRunes 取 s 末尾至多 n 个 rune（rune 安全，不切坏 UTF-8）。
func collapseKeepTailRunes(s string, n int) string {
	r := []rune(s)
	if n <= 0 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
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

	// 默认 bash 路径的进程硬化是 opt-in 的：开启后走 proc.Run，
	// 获得有界捕获、进程组杀树、环境净化与跨分块 UTF-8 重组；
	// 关闭时保持下方的 CombinedOutput 路径逐字节不变。
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

// builtinShellEnabled 报告是否启用自研内建 shell 兜底。
// 默认关闭：只有 ARGENT_BUILTIN_SHELL=1 才启用，确保现有系统 shell 路径逐字节不变。
func builtinShellEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_BUILTIN_SHELL")) == "1"
}

// bashUseProcEnabled 报告是否让默认 bash 主路径改走 proc.Run。
// 默认关闭：只有 ARGENT_BASH_USE_PROC=1 才启用，确保关闭时仍走
// 原有的 exec.Cmd.CombinedOutput 路径、输出逐字节不变。
func bashUseProcEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_BASH_USE_PROC")) == "1"
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
