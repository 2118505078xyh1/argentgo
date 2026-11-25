package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/proc"
)

// 步骤与运行状态。
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// defaultBackoff 是第一次重试前的延迟；每次尝试翻倍。
const defaultBackoff = 100 * time.Millisecond

// defaultCheckTimeout 在定义未给定时限制客观成功检查或清理命令。
// 定义未给定时。
const defaultCheckTimeout = 30 * time.Second

// maxCheckDiagnostic 限制复制到失败消息中的命令输出。
const maxCheckDiagnostic = 8 << 10

// maxCheckAnchorOutput 限制复制到重试提示中的脱敏检查输出。它刻意小于原始诊断：提示只是轻推，
// 提示。它刻意小于原始诊断：提示只是轻推，
// 而不是命令输出的第二份副本。
const maxCheckAnchorOutput = 512

// Prompter 向模型发送一个提示并返回其答案。
type Prompter interface {
	Prompt(ctx context.Context, text string) (string, error)
}

// ToolRunner 按名称和原始 JSON 参数调用一个工具。
type ToolRunner interface {
	Run(ctx context.Context, name, args string) (string, error)
}

// Executor 用注入的模型和工具 seam 运行一个定义。
type Executor struct {
	Prompter Prompter
	Tools    ToolRunner
	// Backoff 是基础重试延迟（默认 100ms，每次尝试翻倍）。
	Backoff time.Duration
}

// StepResult 是一个步骤的结果。
type StepResult struct {
	Name     string
	Status   string
	Output   string
	Error    string
	Attempts int
	Duration time.Duration
}

// Run 是一次工作流的结果。
type Run struct {
	Name   string
	Status string
	Steps  []StepResult
}

// Succeeded 报告是否每个步骤都成功或被跳过。
func (r Run) Succeeded() bool { return r.Status == StatusOK }

// Output 按名称返回某步骤的输出。
func (r Run) Output(name string) (string, bool) {
	for _, step := range r.Steps {
		if step.Name == name {
			return step.Output, step.Status == StatusOK
		}
	}
	return "", false
}

// Run 按依赖顺序执行步骤并返回所有结果。
// 带声明参数的定义需要通过以下方式提供值
// RunWithParams.
func (e *Executor) Run(ctx context.Context, def *Definition, input string) (Run, error) {
	return e.RunWithParams(ctx, def, input, nil)
}

// 带声明参数的定义需要通过以下方式提供值
// RunWithParams 按依赖顺序执行步骤（读取 {{steps.NAME.output}} 的步骤在其生产者之后运行），并额外从定义声明的参数中解析 {{key}}
// 占位符。值在此处和 CLI 都会被解析，
// 占位符。值在此处和 CLI 都会被解析，使调用方无法通过传原始值绕过必需/默认检查；对已解析的映射重复解析是幂等的。
// 通过传原始值绕过检查；对已解析映射重复解析是幂等的。
//
// 失败的步骤会额外重试最多 step.Retries 次并采用指数
// 退避；尝试耗尽后步骤的 on_failure 策略决定运行是停止还是继续。错误只在定义无效或参数未解析时返回 —— 步骤失败属于结果的一部分。
// 运行是停止还是继续。错误只在定义无效时返回，
// 参数未解析时返回 —— 步骤失败属于结果的一部分。
func (e *Executor) RunWithParams(ctx context.Context, def *Definition, input string, params map[string]string) (Run, error) {
	if def == nil {
		return Run{}, errors.New("workflow: the definition is nil")
	}
	local := *def
	local.Steps = append([]Step(nil), def.Steps...)
	local.normalise()
	if err := local.Validate(); err != nil {
		return Run{}, err
	}
	resolved, err := ResolveParameters(local, params)
	if err != nil {
		return Run{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate() 已经拒绝了环和悬空步骤引用；
	// 它计算出的顺序就是执行必须遵循的顺序。读取以下内容的步骤
	// Validate() 已经拒绝了环和悬空步骤引用；它计算出的顺序就是执行必须遵循的顺序。读取 {{steps.NAME.output}} 的步骤
	// 必须在其生产者之后运行，否则标记会原样留在提示或工具参数中。
	order, err := ValidateDAG(local.Steps)
	if err != nil {
		return Run{}, err
	}

	backoff := e.Backoff
	if backoff <= 0 {
		backoff = defaultBackoff
	}
	for _, step := range local.Steps {
		if step.Tool != "" && e.Tools == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		if step.Tool == "" && e.Prompter == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
	}

	run := Run{Name: local.Name, Status: StatusOK}
	outputs := make(map[string]string, len(local.Steps))
	var previous string
	havePrevious := false
	stopped := false

	for _, index := range order {
		step := local.Steps[index]
		if stopped {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "an earlier step stopped the workflow",
			})
			continue
		}
		// {{steps.NAME.output}} 引用是真实依赖，而不只是模板提示：生产者没有产生输出时消费者不得运行。全局停止标志在这里不够，因为生产者
		// 模板提示：生产者没有产生输出时消费者不得运行。全局停止标志这里不够，因为生产者
		// 可能失败，或被跳过，
		// 可能在 on_failure:continue 下失败，或被其自身 When 条件跳过，使消费者留下字面量未解析标记。
		// 条件跳过，使消费者留下字面量未解析标记。
		missing := ""
		for _, dep := range referencedSteps(step) {
			if _, ok := outputs[dep]; !ok {
				missing = dep
				break
			}
		}
		if missing != "" {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  fmt.Sprintf("depends on step %q which did not produce an output", missing),
			})
			continue
		}

		if !step.When.matches(previous, havePrevious) {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "condition not met",
			})
			continue
		}

		result := e.runStep(ctx, step, input, outputs, resolved, backoff)
		run.Steps = append(run.Steps, result)
		if result.Status == StatusOK {
			outputs[step.Name] = result.Output
			previous = result.Output
			havePrevious = true
			continue
		}

		run.Status = StatusFailed
		if step.OnFailure != OnFailureContinue {
			stopped = true
		}
	}
	return run, nil
}

// runStep 执行一个步骤，带退避重试。
func (e *Executor) runStep(ctx context.Context, step Step, input string, outputs, params map[string]string, backoff time.Duration) StepResult {
	attempts := 1 + step.Retries
	if step.OnFailure == OnFailureRetry && attempts < 2 {
		attempts = 2
	}
	result := StepResult{Name: step.Name}
	started := time.Now()

	var output string
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		// 已结束的 context 无法通过另一次尝试恢复。在调用 seam 之前
		// 已结束的 context 无法通过另一次尝试恢复。在调用 seam 之前检查：prompter 或 tool runner 可能忽略其
		// context，而被取消的运行仍必须停止而不启动
		// context，而被取消的运行仍必须停止而不启动新工作。若实际未尝试任何内容，Attempts 保持为零。
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		if attempt > 1 {
			if err := sleep(ctx, backoffAt(backoff, attempt-1)); err != nil {
				lastErr = err
				break
			}
		}
		// Attempts 统计真实的 seam 调用。仅在退避成功结束后才增加它，使等待期间的取消不会用一个从未真正运行的尝试抬高计数。
		// 退避成功结束后才增加，使等待期间的取消
		// 不会用一个从未真正运行的尝试抬高计数。
		result.Attempts = attempt
		output, lastErr = e.attempt(ctx, step, input, outputs, params)
		if err := ctx.Err(); err != nil {
			// 已取消或超时：另一次尝试无济于事。若 seam
			// 已取消或超时：另一次尝试无济于事。若 seam 忽略取消并返回了值，应暴露
			// context 错误，使被取消的步骤不被报告为成功。
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		// 失败的成功检查会被该循环重新运行，因此告诉模型不要
		// 原样重复它。仅当配置了重试时才添加提示；
		// 检查自身的诊断保持逐字节不变。
		var failedCheck *checkFailure
		if attempts > 1 && errors.As(lastErr, &failedCheck) {
			lastErr = fmt.Errorf("%w\n%s", lastErr, checkRetryHint(step, failedCheck.output))
		}
		result.Status = StatusFailed
		result.Error = lastErr.Error()
		// 失败的成功检查会被该循环重新运行，因此告诉模型不要原样重复它。仅当配置了重试时才添加提示；检查自身的诊断保持逐字节不变。
		// 清理在所有尝试耗尽之后、调用方应用步骤的 on_failure 策略之前运行一次。其输出被加入
		// 诊断，但其退出码不能改变决策。
		if cleanup := runCleanupCommand(ctx, step); cleanup != "" {
			result.Error += "\n" + cleanup
		}
		result.Duration = time.Since(started)
		return result
	}
	result.Duration = time.Since(started)
	result.Status = StatusOK
	result.Output = output
	return result
}

// 诊断，但其退出码不能改变决策。
// attempt 运行一个步骤一次，遵守步骤局部超时。配置的成功检查是 attempt 的一部分，因此失败的检查由 runStep 像工具或 prompter 错误一样重试。
// 像工具或 prompter 错误一样被重试。
func (e *Executor) attempt(ctx context.Context, step Step, input string, outputs, params map[string]string) (string, error) {
	attemptCtx := ctx
	if timeout := step.Timeout.Std(); timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var (
		out string
		err error
	)
	if step.Tool != "" {
		if e.Tools == nil {
			return "", fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		args := Substitute(step.Args, input, outputs, params)
		out, err = e.Tools.Run(attemptCtx, step.Tool, args)
	} else {
		if e.Prompter == nil {
			return "", fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
		out, err = e.Prompter.Prompt(attemptCtx, Substitute(step.Prompt, input, outputs, params))
	}
	err = timedOut(step, err, step.Timeout.Std())
	if err == nil {
		// seam 可能忽略其 context 并在 attempt 截止时间已过后返回值。步骤局部超时不会取消父
		// 截止时间已过后返回值。步骤局部超时不会取消父
		// 运行 context，因此 runStep 无法通过 ctx.Err() 察觉；executor 必须自身强制执行绑定，否则迟到的结果会被记为
		// executor 必须自身强制执行绑定，否则迟到的结果会被记为
		// 成功。runStep 的取消检查仍适用于父取消，
		// 成功。runStep 的取消检查仍适用于父取消，attemptCtx 继承了它。
		if ctxErr := attemptCtx.Err(); ctxErr != nil {
			err = timedOut(step, ctxErr, step.Timeout.Std())
		}
	}
	if err != nil {
		return out, err
	}
	if step.SuccessCheck != nil && strings.TrimSpace(step.SuccessCheck.Command) != "" {
		if err := runSuccessCheck(attemptCtx, step); err != nil {
			return out, err
		}
	}
	return out, nil
}

// timedOut 重新标记截止时间错误，使结果说明是哪个步骤以及
// 它允许运行多久。
func timedOut(step Step, err error, limit time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || limit <= 0 {
		return err
	}
	return fmt.Errorf("workflow: step %q timed out after %s: %w", step.Name, limit, err)
}

// checkTimeout 解析 TimeoutSeconds 值；零或负数选择保守默认值。
// 保守默认值。
func checkTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultCheckTimeout
	}
	return time.Duration(seconds) * time.Second
}

// diagnosticTail 最多保留 s 末尾的 n 字节。切点移到 rune 边界，使多字节字符从不被切掉一半。
// rune 边界，使多字节字符从不被切掉一半。
func diagnosticTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// commandDiagnostic 为失败消息挑选有界的命令输出，
// commandDiagnostic 为失败消息挑选有界的命令输出，优先 stderr，因为检查在那里报告错误。
func commandDiagnostic(res proc.Result) string {
	if text := strings.TrimSpace(res.Stderr); text != "" {
		return diagnosticTail(text, maxCheckDiagnostic)
	}
	return diagnosticTail(strings.TrimSpace(res.Stdout), maxCheckDiagnostic)
}

func diagnosticSuffix(text string) string {
	if text == "" {
		return ""
	}
	return ": " + text
}

// checkFailure 包装失败的成功检查，使 runStep 能识别并添加
// 有界的重试提示。原始消息和错误链被保留：
// checkFailure 包装失败的成功检查，使 runStep 能识别它并添加有界的重试提示。原始消息和错误链被保留：Error 文本不变，Unwrap 保持 DeadlineExceeded 可匹配。
type checkFailure struct {
	output string
	cause  error
}

func (c *checkFailure) Error() string { return c.cause.Error() }
func (c *checkFailure) Unwrap() error { return c.cause }

// checkRetryHint 在工作流层镜像 agent.VerificationFeedback，该层不允许导入 agent。它在裁剪到 rune 安全的尾部之前对检查输出脱敏，然后添加下一步指令，使重试
// 该层不允许导入 agent。它在裁剪之前对检查输出脱敏，
// 裁剪到 rune 安全的尾部，然后添加下一步指令，使重试
// 不会在代码未变的情况下重跑同一个检查。
func checkRetryHint(step Step, output string) string {
	label := strings.ToUpper(strings.TrimSpace(step.Name))
	if label == "" {
		label = "WORKFLOW"
	}
	var b strings.Builder
	b.WriteString("[verify: " + label + "] FAIL")
	if clean := llm.RedactText(strings.TrimSpace(output)); clean != "" {
		b.WriteString("\n" + diagnosticTail(clean, maxCheckAnchorOutput))
	}
	b.WriteString("\ndo not re-run the same check unchanged; adjust the code first.")
	return b.String()
}

// runSuccessCheck 通过 proc.Shell/proc.Run 客观验证步骤，这是代码库中所有其他命令使用的同一 shell 路径。非零退出、spawn 错误或超时都会变成错误，使 runStep 通过
// 代码库中所有其他命令使用的同一 shell 路径。非零退出、
// spawn 错误或超时都会变成错误，使 runStep 通过
// 正常的失败路径重试。
func runSuccessCheck(ctx context.Context, step Step) error {
	check := step.SuccessCheck
	command := strings.TrimSpace(check.Command)
	limit := checkTimeout(check.TimeoutSeconds)
	// 正常的失败路径重试它。
	// 检查是 attempt 的一部分，因此它绝不能比 attempt 的剩余预算活得更久。当 context 带有截止时间（步骤超时）时，
	// 把配置的检查超时限制到该预算剩余的时间。
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < limit {
			limit = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	cmd := proc.Shell(command)
	cmd.Timeout = limit
	// 检查可能产生自己的子进程（sleep、构建工具）；超时时杀掉整棵树，使步骤不会超出其预算。
	// 超时时杀掉整棵树，使步骤不会超出其预算。
	cmd.KillTree = true
	res, err := proc.Run(runCtx, cmd)
	diagnostic := diagnosticSuffix(commandDiagnostic(res))
	var failure error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		failure = fmt.Errorf("workflow: step %q success check timed out after %s: %w%s", step.Name, limit, err, diagnostic)
	case err != nil:
		failure = fmt.Errorf("workflow: step %q success check %q failed: %w%s", step.Name, command, err, diagnostic)
	case !res.Success():
		failure = fmt.Errorf("workflow: step %q success check %q exited %d%s", step.Name, command, res.ExitCode, diagnostic)
	default:
		return nil
	}
	return &checkFailure{output: commandDiagnostic(res), cause: failure}
}

// runCleanupCommand 运行步骤的清理命令一次并返回被追加到失败诊断的文本。其退出码被报告但从不
// 被追加到失败诊断。其退出码被报告但从不
// 改变失败决策。
func runCleanupCommand(ctx context.Context, step Step) string {
	command := strings.TrimSpace(step.CleanupCommand)
	if command == "" {
		return ""
	}
	limit := checkTimeout(0)
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	cmd := proc.Shell(command)
	cmd.Timeout = limit
	// 清理可能产生自己的子进程；超时时杀掉整棵树，使它
	// 改变失败决策。
	cmd.KillTree = true
	res, err := proc.Run(runCtx, cmd)
	var b strings.Builder
	fmt.Fprintf(&b, "cleanup %q: ", command)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(&b, "timed out after %s", limit)
	case err != nil:
		fmt.Fprintf(&b, "%v", err)
	default:
		fmt.Fprintf(&b, "exit %d", res.ExitCode)
	}
	b.WriteString(diagnosticSuffix(commandDiagnostic(res)))
	return b.String()
}

// 清理可能产生自己的子进程；超时时杀掉整棵树，使它不能在步骤失败后仍让运行挂住。
// backoffAt 将基础延迟翻倍，并设上限使大的重试预算不会导致 duration 溢出。
func backoffAt(base time.Duration, attempt int) time.Duration {
	if attempt > 20 {
		attempt = 20
	}
	return base << uint(attempt-1)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Substitute 替换 {{input}}、{{steps.NAME.output}}，并在提供 params 时替换来自已解析工作流参数的 {{key}}
// 提供 params 时来自已解析工作流参数的 {{key}} 占位符。内置
// 标记优先，无值的键保持原样，使面向模型的模板得以
// 占位符。内置标记优先，无值的键保持原样，使面向模型的模板得以保留。插入的值被原样写入
// 且不再重扫，因此包含 "{{...}}" 的值无法注入标记。
func Substitute(text, input string, outputs map[string]string, params ...map[string]string) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var out strings.Builder
	rest := text
	for {
		start, tokenEnd, key, ok := nextPlaceholder(rest)
		if !ok {
			out.WriteString(rest)
			break
		}
		end := tokenEnd - 2 // index of the first '}' of the closing "}}"
		out.WriteString(rest[:start])
		switch {
		case key == "input":
			out.WriteString(input)
		case strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, ".output"):
			name := strings.TrimSuffix(strings.TrimPrefix(key, "steps."), ".output")
			if value, ok := outputs[name]; ok {
				out.WriteString(value)
			} else {
				out.WriteString(rest[start : end+2])
			}
		default:
			if value, ok := lookupParam(params, key); ok {
				out.WriteString(value)
			} else {
				out.WriteString(rest[start : end+2])
			}
		}
		rest = rest[end+2:]
	}
	return out.String()
}

// lookupParam 找到第一个持有 key 的 map。Substitute 的可变参数通常只携带一个 map；接受更多使调用方
// Substitute 的可变参数通常只携带一个 map；接受更多使调用方
// 组合参数来源时调用点保持简单。
func lookupParam(params []map[string]string, key string) (string, bool) {
	for _, p := range params {
		if value, ok := p[key]; ok {
			return value, true
		}
	}
	return "", false
}
