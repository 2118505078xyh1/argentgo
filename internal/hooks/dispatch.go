package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// maxCapture 限制从钩子捕获的单个流。
	maxCapture = 64 * 1024
	// summaryClip 限制 Result.Message 中引用的输出。
	summaryClip = 400
	// waitDelay 限制 Wait 在钩子进程退出后保持被捕获的 stdout/stderr 管道
	// 打开的时间。若钩子生成一个后台
	// 打开的时间。若钩子生成一个继承这些管道的后台
	// 进程，否则它们会保持打开越过命令超时，拖慢整个（并行）分发。该延迟
	// 仍足够长，让普通输出完成刷新。
	waitDelay = 500 * time.Millisecond
)

// Payload 是写入钩子 stdin 的 JSON 文档。
type Payload struct {
	Event     string          `json:"event"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	// CallID 可选地标识触发此钩子的工具调用。它
	// 被带入 PanicError，使错误的回调能与正在分发的
	// 调用关联起来。
	CallID string `json:"callId,omitempty"`
	// Prompt 是 UserPromptSubmit 和 Stop 的用户消息。
	Prompt string `json:"prompt,omitempty"`
	// Output 是 PostToolUse 的工具结果。
	Output string `json:"output,omitempty"`
	// Error 是 PostToolUseFailure 的工具失败。
	Error string `json:"error,omitempty"`
	// Path 是 FileChanged 事件影响的文件。其他事件为空；修改多个文件的调用方
	// 每个路径触发一个事件。
	Path string `json:"path,omitempty"`
	// HookPayload 是回合结束事件的可选事件专属细节
	// （EventInterrupt、EventStopFailure、EventTurnEnd）：分类
	// 原因、最后一条助手消息或错误文本。其他所有事件为空，因此
	// 现有 payload JSON 不变。
	HookPayload string `json:"hookPayload,omitempty"`
}

// Result 是一次分发的聚合决策。
//
// Blocked 仅当 PreToolUse 钩子以状态 2 退出时为 true。Message
// 携带拦截钩子的输出，或未拦截但失败的钩子的警告。
// 携带拦截钩子的输出，或未拦截但失败的钩子的警告。
type Result struct {
	Blocked bool
	Message string
}

// Outcome 表示某个钩子命令做了什么。
type Outcome struct {
	Event    Event
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Blocked  bool
	Elapsed  time.Duration
	Err      error
}

// Message 是对结果的一行摘要。
func (o Outcome) Message() string {
	detail := strings.TrimSpace(o.Stdout)
	if detail == "" {
		detail = strings.TrimSpace(o.Stderr)
	}
	detail = clip(detail)

	var prefix string
	switch {
	case o.TimedOut:
		prefix = fmt.Sprintf("%s hook %q timed out after %s", o.Event, clipCommand(o.Command), o.Elapsed.Round(time.Millisecond))
	case o.Err != nil:
		prefix = fmt.Sprintf("%s hook %q could not run: %v", o.Event, clipCommand(o.Command), o.Err)
	case o.Blocked:
		prefix = fmt.Sprintf("%s hook %q blocked the call (exit 2)", o.Event, clipCommand(o.Command))
	case o.ExitCode != 0:
		prefix = fmt.Sprintf("%s hook %q failed (exit %d)", o.Event, clipCommand(o.Command), o.ExitCode)
	default:
		prefix = fmt.Sprintf("%s hook %q succeeded", o.Event, clipCommand(o.Command))
	}
	if detail == "" {
		return prefix
	}
	return prefix + ": " + detail
}

// CommandRunner 执行一条钩子命令。零值 Dispatcher 使用
// RunCommand，它会启动 shell。测试注入 CommandRunner 以观察
// 执行而不触碰进程表。runner 必须遵守 ctx 的
// 取消和截止时间，使 dispatcher 的超时仍能限制钩子。
type CommandRunner func(ctx context.Context, command Command, payload Payload) Outcome

// Dispatcher runs hook commands.
type Dispatcher struct {
	// Timeout bounds one command when the command itself sets none; zero
	// means 30 seconds.
	Timeout time.Duration
	// Shell overrides the command interpreter, for tests and odd platforms.
	// The default is bash -c (cmd /C on Windows when bash is absent).
	Shell func(command string) (name string, args []string)

	// Telemetry, when non-nil, is invoked once per hook executed by
	// Dispatch/Run with its elapsed time, exit status and failure reason. It
	// runs from the hook's own goroutine while hooks are parallel, so the
	// callback must be safe for concurrent use. Leaving it nil (the default)
	// adds no telemetry work to a run.
	Telemetry TelemetryFunc
	// SlowThreshold marks HookTelemetry.Slow true once a hook's elapsed time
	// reaches it. Zero (the default) disables the mark. It never changes
	// dispatch or timing; callers decide how to warn on slow records.
	SlowThreshold time.Duration

	// Runner, when non-nil, replaces the built-in process runner for every
	// hook, synchronous or asynchronous. It is the injection seam used by
	// tests to observe hooks without spawning a real shell.
	Runner CommandRunner

	// asyncWG tracks background hooks launched by Run. Run never waits on it:
	// an async hook must not delay the decision that triggered it. WaitAsync
	// lets callers drain them (tests, graceful shutdown).
	asyncWG sync.WaitGroup
}

// Dispatch runs every matching hook for payload.Event and returns the
// aggregate decision.
func (d *Dispatcher) Dispatch(ctx context.Context, config *Config, payload Payload) (Result, error) {
	outcomes, err := d.Run(ctx, config, payload)
	if err != nil {
		return Result{}, err
	}
	return Summarize(outcomes), nil
}

// DispatchDocument runs the hooks of a trusted document.
func (d *Dispatcher) DispatchDocument(ctx context.Context, document *Document, payload Payload) (Result, error) {
	if document == nil || document.Config == nil {
		return Result{}, ErrUntrusted
	}
	return d.Dispatch(ctx, document.Config, payload)
}

// FireFileChanged dispatches a FileChanged lifecycle event for one changed
// path. It is a convenience wrapper that fixes the event name and payload
// field, so every caller produces the same hook document shape. Callers that
// need session/cwd context fill those on the app-side bridge, which owns the
// run context, before dispatching.
func (d *Dispatcher) FireFileChanged(ctx context.Context, config *Config, path string) (Result, error) {
	return d.Dispatch(ctx, config, Payload{Event: string(FileChanged), Path: path})
}

// Run executes the synchronous hooks for one event, highest Priority first.
// Hooks with equal Priority keep their declaration order (stable sort), so
// document hooks — all at priority 0 — behave exactly as before. Async hooks
// are launched in the background and never enter the returned decision: a
// deciding hook must answer synchronously. A command with StopPropagation ends
// the run after it executes, shielding later hooks.
//
// Hooks of one priority tier run concurrently; tiers are drained in priority
// order. Aggregate latency drops from O(N×T) to roughly O(T) per tier, while
// the executed set and the order of the returned outcomes stay identical to
// the previous serial loop. StopPropagation is a static flag, so planRun can
// compute the short-circuit boundary before any command starts.
func (d *Dispatcher) Run(ctx context.Context, config *Config, payload Payload) ([]Outcome, error) {
	if config == nil {
		return nil, nil
	}
	event, ok := ParseEvent(payload.Event)
	if !ok {
		return nil, fmt.Errorf("hooks: unknown event %q", payload.Event)
	}

	// FileChanged matchers describe the affected file ("*.go"), not the tool
	// that changed it. FireFileChanged and other direct callers carry only
	// Payload.Path; ForEvent skips matcher-bearing hooks when the match value
	// is empty, so without this the documented file matchers never fire.
	matchValue := payload.Tool
	if event == FileChanged && payload.Path != "" {
		matchValue = payload.Path
	}
	commands := config.ForEvent(event, matchValue)
	sort.SliceStable(commands, func(i, j int) bool {
		return commands[i].Priority > commands[j].Priority
	})

	// Launch background hooks before the synchronous tiers so a slow or
	// cancelled tier cannot stop them from starting. Run never waits for them.
	d.launchAsync(ctx, planAsync(commands), payload)

	tiers := planRun(commands)
	total := 0
	for _, tier := range tiers {
		total += len(tier)
	}
	outcomes := make([]Outcome, 0, total)
	for _, tier := range tiers {
		outcomes = append(outcomes, d.runTier(ctx, tier, payload)...)
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
	}
	return outcomes, nil
}

// claimedCommands walks the sorted command list once and returns the commands
// that actually execute: duplicate command strings are dropped (the first
// occurrence in priority/declaration order wins) and a StopPropagation command
// ends the walk, shielding every later hook. A duplicate still honours its own
// stop boundary. Both planRun and planAsync derive from this single walk so
// they cannot disagree about which commands were claimed: every command goes
// to exactly one of the synchronous or background paths, and a stop on either
// path shields later hooks in both.
func claimedCommands(commands []Command) []Command {
	seen := make(map[string]struct{}, len(commands))
	claimed := make([]Command, 0, len(commands))
	for _, command := range commands {
		if _, duplicate := seen[command.Command]; duplicate {
			if command.StopPropagation {
				break
			}
			continue
		}
		seen[command.Command] = struct{}{}
		claimed = append(claimed, command)
		if command.StopPropagation {
			break
		}
	}
	return claimed
}

// planRun mirrors the old serial loop's control flow using the claimed set:
// synchronous commands are grouped into priority tiers, each holding
// equal-priority commands that may run concurrently, in execution order.
func planRun(commands []Command) [][]Command {
	claimed := claimedCommands(commands)
	tiers := make([][]Command, 0, len(claimed))
	var current []Command
	haveTier := false
	var tierPriority int
	for _, command := range claimed {
		if command.Async {
			continue
		}
		if !haveTier || command.Priority != tierPriority {
			if haveTier && len(current) > 0 {
				tiers = append(tiers, current)
			}
			current = nil
			tierPriority = command.Priority
			haveTier = true
		}
		current = append(current, command)
	}
	if haveTier && len(current) > 0 {
		tiers = append(tiers, current)
	}
	return tiers
}

// resultCollector gathers outcomes from concurrent hook runs. The mutex keeps
// collection race-free; each goroutine records under its original index so
// ordered() can rebuild declaration order regardless of completion order.
type resultCollector struct {
	mu      sync.Mutex
	results map[int]Outcome
}

func (c *resultCollector) record(index int, outcome Outcome) {
	c.mu.Lock()
	c.results[index] = outcome
	c.mu.Unlock()
}

func (c *resultCollector) ordered(n int) []Outcome {
	outcomes := make([]Outcome, n)
	for index, outcome := range c.results {
		outcomes[index] = outcome
	}
	return outcomes
}

// runTier runs one priority tier concurrently. A cancelled context stops new
// hooks from launching; hooks already started finish under their own timeout
// and are still collected. Outcomes are returned in tier declaration order.
func (d *Dispatcher) runTier(ctx context.Context, tier []Command, payload Payload) []Outcome {
	collector := &resultCollector{results: make(map[int]Outcome, len(tier))}
	var wg sync.WaitGroup
	launched := 0
	for index, command := range tier {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		launched++
		go func(index int, command Command) {
			defer wg.Done()
			collector.record(index, d.guardedRunCommand(ctx, command, payload))
		}(index, command)
	}
	wg.Wait()
	return collector.ordered(launched)
}

// commandRunner returns the runner used for every hook. The zero value keeps
// the original process-spawning behaviour; only tests that need an injection
// seam override it.
func (d *Dispatcher) commandRunner() CommandRunner {
	if d.Runner != nil {
		return d.Runner
	}
	return d.RunCommand
}

// timeoutFor resolves the effective timeout of a command: the command's own
// value, then the dispatcher's, then defaultTimeout, clamped to minTimeout.
// Both the synchronous process path and the background path use it so a hook
// is bounded the same way regardless of how it was launched.
func (d *Dispatcher) timeoutFor(command Command) time.Duration {
	timeout := command.Timeout
	if timeout <= 0 {
		timeout = d.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout < minTimeout {
		timeout = minTimeout
	}
	return timeout
}

// guardedRunCommand runs one hook with panic containment. The only in-process
// callbacks a hook can panic through are Dispatcher.Shell and Runner; a command
// that panics becomes an Outcome with a PanicError, and the loop still reaches
// the next command. The command string is the fallback identity when the caller
// did not supply a tool call ID.
func (d *Dispatcher) guardedRunCommand(ctx context.Context, command Command, payload Payload) Outcome {
	// Take the clock only when telemetry is on, so the default path pays no
	// extra timing cost beyond what RunCommand already does.
	var started time.Time
	if d.Telemetry != nil {
		started = time.Now()
	}
	outcome := Outcome{Event: command.Event, Command: command.Command, ExitCode: -1}
	callID := payload.CallID
	if callID == "" {
		callID = command.Command
	}
	runner := d.commandRunner()
	err := guard(callID, func() error {
		outcome = runner(ctx, command, payload)
		return outcome.Err
	})
	if err != nil && outcome.Err == nil {
		outcome.Err = err
	}
	if d.Telemetry != nil {
		elapsed := time.Since(started)
		d.reportTelemetry(HookTelemetry{
			Name:     command.Command,
			Event:    command.Event,
			Elapsed:  elapsed,
			Err:      outcome.Err,
			ExitCode: outcome.ExitCode,
			TimedOut: outcome.TimedOut,
			Blocked:  outcome.Blocked,
			Slow:     d.SlowThreshold > 0 && elapsed >= d.SlowThreshold,
		})
	}
	return outcome
}

// RunCommand runs one hook command with the payload on stdin.
func (d *Dispatcher) RunCommand(ctx context.Context, command Command, payload Payload) Outcome {
	started := time.Now()
	outcome := Outcome{Event: command.Event, Command: command.Command, ExitCode: -1}

	if len(bytes.TrimSpace(payload.Args)) == 0 {
		payload.Args = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		outcome.Err = err
		outcome.Elapsed = time.Since(started)
		return outcome
	}

	timeout := d.timeoutFor(command)

	// A caller-supplied Shell has already chosen the interpreter. Resolving
	// the default one first would run findBash's probes (up to five processes
	// with a two-second deadline each on Windows) even though the result is
	// discarded — and it happens before runCtx exists, so that cost is not
	// covered by the hook's timeout.
	var name string
	var args []string
	if d.Shell != nil {
		name, args = d.Shell(command.Command)
	} else {
		name, args = shellCommand(command.Command)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runCtx, name, args...)
	// A timed-out or killed process may leave a grandchild holding the pipe
	// write ends; bound how long Wait waits for those pipes so the timeout
	// above really bounds wall-clock time.
	process.WaitDelay = waitDelay
	// The context can expire while Wait is still draining the captured pipes
	// after the process itself has exited — a hook that leaves a background
	// child holding stdout keeps them open past the deadline. os/exec still
	// calls Cancel in that case, but Kill returns ErrProcessDone: the hook did
	// not time out. Track only a Kill of a live process so a completed hook is
	// not mislabelled as timed out (which would also drop its exit-2 block).
	var killedByDeadline atomic.Bool
	process.Cancel = func() error {
		err := process.Process.Kill()
		if err == nil && runCtx.Err() == context.DeadlineExceeded {
			killedByDeadline.Store(true)
		}
		return err
	}
	if payload.CWD != "" {
		process.Dir = payload.CWD
	}
	process.Env = append(os.Environ(), "ARGENT_HOOK_EVENT="+string(command.Event))
	if payload.CWD != "" {
		process.Env = append(process.Env,
			"ARGENT_PROJECT_DIR="+payload.CWD,
			"CLAUDE_PROJECT_DIR="+payload.CWD,
		)
	}
	process.Stdin = bytes.NewReader(body)
	stdout := &limitedBuffer{}
	stderr := &limitedBuffer{}
	process.Stdout = stdout
	process.Stderr = stderr

	runErr := process.Run()
	outcome.Elapsed = time.Since(started)
	outcome.Stdout = stdout.String()
	outcome.Stderr = stderr.String()

	outcome.ExitCode, outcome.TimedOut, outcome.Err = classifyRunResult(runErr, process.ProcessState, killedByDeadline.Load())

	// Exit status 2 is the hook protocol's "block" signal, and only PreToolUse
	// may block an action.
	if command.Event == PreToolUse && outcome.ExitCode == 2 && !outcome.TimedOut && command.BlocksOnFailure() {
		outcome.Blocked = true
	}
	return outcome
}

// classifyRunResult turns a finished process wait into the status fields of an
// Outcome. killedByDeadline is true only when the timeout actually killed a
// live hook. Without it, a context that expired while Wait was draining pipes
// left over by a background child must not rewrite the process's real exit
// status: the hook already finished, and its exit-2 block must survive.
func classifyRunResult(runErr error, state *os.ProcessState, killedByDeadline bool) (exitCode int, timedOut bool, err error) {
	switch {
	case killedByDeadline:
		return -1, true, nil
	case runErr == nil:
		return 0, false, nil
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitCode(), false, nil
		}
		if state != nil {
			code := state.ExitCode()
			// ErrWaitDelay and a late context error only mean the captured
			// pipes outlived the process; they are not hook failures.
			if errors.Is(runErr, exec.ErrWaitDelay) ||
				errors.Is(runErr, context.Canceled) ||
				errors.Is(runErr, context.DeadlineExceeded) {
				return code, false, nil
			}
			return code, false, runErr
		}
		return -1, false, runErr
	}
}

// Summarize turns outcomes into the block/warn decision.
func Summarize(outcomes []Outcome) Result {
	var result Result
	var messages []string
	for _, outcome := range outcomes {
		if outcome.Blocked {
			result.Blocked = true
			messages = append(messages, outcome.Message())
			continue
		}
		if outcome.ExitCode != 0 || outcome.TimedOut || outcome.Err != nil {
			messages = append(messages, outcome.Message())
		}
	}
	result.Message = strings.Join(messages, "\n")
	return result
}

// shellCommand picks the interpreter for a hook command. Hooks are shell
// scripts, so bash is preferred when a working one is installed.
func shellCommand(command string) (string, []string) {
	if bash := findBash(); bash != "" {
		return bash, []string{"-c", command}
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "/bin/sh", []string{"-c", command}
}

var (
	bashOnce sync.Once
	bashPath string
)

// findBash returns a bash that actually runs. On Windows the first "bash" on
// PATH is often the WSL alias in System32, which fails when no distro is
// installed, so each candidate is probed once.
func findBash() string {
	bashOnce.Do(func() {
		var candidates []string
		if found, err := exec.LookPath("bash"); err == nil {
			candidates = append(candidates, found)
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				`C:\Program Files\Git\bin\bash.exe`,
				`C:\Program Files\Git\usr\bin\bash.exe`,
				`C:\Program Files (x86)\Git\bin\bash.exe`,
				`C:\msys64\usr\bin\bash.exe`,
			)
		}
		for _, candidate := range candidates {
			if bashRuns(candidate) {
				bashPath = candidate
				return
			}
		}
	})
	return bashPath
}

// bashRuns probes one candidate interpreter. It is a variable so tests can
// count probes without spawning processes.
var bashRuns = func(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "-c", "exit 0").Run() == nil
}

// limitedBuffer captures at most maxCapture bytes.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxCapture - b.buf.Len(); room > 0 {
		if len(p) > room {
			_, _ = b.buf.Write(p[:room])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n…(truncated)"
	}
	return b.buf.String()
}

// clipRunes bounds text to limit bytes without cutting a UTF-8 rune in half.
// A trailing ellipsis takes the place of the dropped tail. The backoff walks
// from limit-1 to the preceding RuneStart, so the cut always lands on a rune
// boundary (and at worst yields just the ellipsis).
func clipRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit - 1 // leave room for the ellipsis
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func clip(text string) string {
	return clipRunes(text, summaryClip)
}

func clipCommand(command string) string {
	return clipRunes(command, 120)
}
