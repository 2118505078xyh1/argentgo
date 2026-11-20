package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// TestMain 同时充当被测的钩子命令：当辅助变量被
// 设置时，本进程表现得像一个很小的钩子脚本，而非测试二进制文件。
func TestMain(m *testing.M) {
	if mode := os.Getenv("ARGENT_HOOK_HELPER"); mode != "" {
		hookHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func hookHelper(mode string) {
	switch mode {
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "block":
		os.Exit(2)
	case "fail":
		fmt.Fprintln(os.Stderr, "hook exploded")
		os.Exit(1)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "sleepms":
		// 并行执行测试使用的有界 sleep。在宽松的钩子超时下，N 个并发钩子耗时约
		// 一次 sleep，而非 N 次。
		if ms, err := strconv.Atoi(os.Getenv("ARGENT_HOOK_SLEEP_MS")); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	case "append":
		// 每次调用记录一行，使测试能统计执行次数。一次
		// 小型追加可避免并发写入者交错。
		if path := os.Getenv("ARGENT_HOOK_LOG"); path != "" {
			if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = file.WriteString("x\n")
				_ = file.Close()
			}
		}
	case "lingering":
		// 立即退出，但留下一个持有本进程 stdout
		// 和 stderr 的子进程 —— dispatcher 捕获的管道。没有 WaitDelay
		// 时，dispatcher 的 Wait() 会阻塞直到子进程释放它们。
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "ARGENT_HOOK_HELPER=lingering-child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "lingering-signal":
		// 与 "lingering" 类似，但只在 ARGENT_HOOK_GO_FILE 出现后才退出，使
		// 测试能把进程退出安排在钩子截止时间之前。
		// ARGENT_HOOK_READY_FILE 告诉测试子进程已经持有
		// 被捕获的管道。
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "ARGENT_HOOK_HELPER=lingering-child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if ready := os.Getenv("ARGENT_HOOK_READY_FILE"); ready != "" {
			_ = os.WriteFile(ready, nil, 0o600)
		}
		goFile := os.Getenv("ARGENT_HOOK_GO_FILE")
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if goFile != "" {
				if _, err := os.Stat(goFile); err == nil {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		code, _ := strconv.Atoi(os.Getenv("ARGENT_HOOK_EXIT_CODE"))
		os.Exit(code)
	case "exitcode":
		// 用于获取具有已知退出状态的真实 *os.ProcessState，
		// 供分类单元测试使用。
		code, _ := strconv.Atoi(os.Getenv("ARGENT_HOOK_EXIT_CODE"))
		os.Exit(code)
	case "lingering-child":
		// 持有继承的管道直到测试发出完成信号（带有
		// 回退上限，使失败的测试不会永远泄漏一个进程）。
		exitFile := os.Getenv("ARGENT_HOOK_EXIT_FILE")
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if exitFile != "" {
				if _, err := os.Stat(exitFile); err == nil {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	os.Exit(0)
}

// helperDispatcher 运行测试二进制文件而不是 shell。
func helperDispatcher(timeout time.Duration) *Dispatcher {
	return &Dispatcher{
		Timeout: timeout,
		Shell:   func(string) (string, []string) { return os.Args[0], nil },
	}
}

func configFor(t *testing.T, event Event, matcher string, extra string) *Config {
	t.Helper()
	source := fmt.Sprintf(
		`{"hooks":{%q:[{"matcher":%q,"hooks":[{"type":"command","command":"run-hook"%s}]}]}}`,
		string(event), matcher, extra)
	config, err := ParseConfig([]byte(source))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return config
}

func TestDispatchBlocksWhenPreToolUseExitsTwo(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "block")
	result, err := helperDispatcher(5*time.Second).Dispatch(
		context.Background(),
		configFor(t, PreToolUse, "bash", ""),
		Payload{Event: "PreToolUse", Tool: "bash"},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("exit 2 must block PreToolUse: %+v", result)
	}
	if !strings.Contains(result.Message, "blocked") {
		t.Fatalf("block reason lost: %q", result.Message)
	}
}

func TestDispatchWarnsOnOtherFailures(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "fail")
	result, err := helperDispatcher(5*time.Second).Dispatch(
		context.Background(),
		configFor(t, PreToolUse, "", ""),
		Payload{Event: "PreToolUse"},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked {
		t.Fatal("exit 1 must warn, not block")
	}
	if !strings.Contains(result.Message, "exit 1") || !strings.Contains(result.Message, "hook exploded") {
		t.Fatalf("warning lost its details: %q", result.Message)
	}
}

func TestExitTwoOnlyBlocksPreToolUse(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "block")
	result, err := helperDispatcher(5*time.Second).Dispatch(
		context.Background(),
		configFor(t, PostToolUse, "", ""),
		Payload{Event: "PostToolUse"},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked {
		t.Fatal("only PreToolUse may block")
	}
	if !strings.Contains(result.Message, "exit 2") {
		t.Fatalf("expected a warning: %q", result.Message)
	}
}

func TestOnErrorAllowSuppressesTheBlock(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "block")
	result, err := helperDispatcher(5*time.Second).Dispatch(
		context.Background(),
		configFor(t, PreToolUse, "", `,"onError":"allow"`),
		Payload{Event: "PreToolUse"},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked {
		t.Fatal("onError=allow must not block")
	}
}

func TestDispatchTimesOut(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "sleep")
	dispatcher := helperDispatcher(200 * time.Millisecond)

	started := time.Now()
	outcomes, err := dispatcher.Run(
		context.Background(),
		configFor(t, PreToolUse, "", `,"timeoutMs":200`),
		Payload{Event: "PreToolUse"},
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("unexpected outcomes: %+v", outcomes)
	}
	if !outcomes[0].TimedOut || outcomes[0].ExitCode != -1 {
		t.Fatalf("timeout not reported: %+v", outcomes[0])
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
	result := Summarize(outcomes)
	if result.Blocked || !strings.Contains(result.Message, "timed out") {
		t.Fatalf("unexpected summary: %+v", result)
	}
}

func TestDispatchWritesThePayloadToStdin(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	payload := Payload{
		Event:     "PreToolUse",
		Tool:      "bash",
		Args:      json.RawMessage(`{"command":"ls"}`),
		SessionID: "sess-1",
		CWD:       t.TempDir(),
	}
	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), configFor(t, PreToolUse, "bash", ""), payload)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].ExitCode != 0 {
		t.Fatalf("unexpected outcome: %+v", outcomes)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(outcomes[0].Stdout), &got); err != nil {
		t.Fatalf("stdin payload was not JSON: %v (%q)", err, outcomes[0].Stdout)
	}
	if got["event"] != "PreToolUse" || got["tool"] != "bash" || got["sessionId"] != "sess-1" {
		t.Fatalf("payload fields lost: %v", got)
	}
	if got["cwd"] != payload.CWD {
		t.Fatalf("cwd lost: %v want %q", got["cwd"], payload.CWD)
	}
	args, ok := got["args"].(map[string]any)
	if !ok || args["command"] != "ls" {
		t.Fatalf("args lost: %v", got["args"])
	}
}

func TestDispatchFiltersCommandsByMatcher(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config, err := ParseConfig([]byte(`{"hooks":{"PreToolUse":[
		{"matcher":"bash","hooks":[{"command":"run-hook"}]},
		{"matcher":"read*","hooks":[{"command":"run-hook"}]}
	]}}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	dispatcher := helperDispatcher(5 * time.Second)

	bash, err := dispatcher.Run(context.Background(), config, Payload{Event: "PreToolUse", Tool: "bash"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(bash) != 1 || bash[0].Command != "run-hook" {
		t.Fatalf("matcher did not select exactly one command: %+v", bash)
	}

	write, err := dispatcher.Run(context.Background(), config, Payload{Event: "PreToolUse", Tool: "write"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(write) != 0 {
		t.Fatalf("a non-matching tool ran a hook: %+v", write)
	}
}

func TestDispatchSkipsAsyncHooks(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config, err := ParseConfig([]byte(`{"hooks":{"PostToolUse":[
		{"hooks":[{"command":"run-hook","async":true}]}
	]}}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	outcomes, err := helperDispatcher(5*time.Second).Run(context.Background(), config, Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("async hooks must not delay the decision: %+v", outcomes)
	}
}

func outcomeCommands(outcomes []Outcome) string {
	commands := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		commands = append(commands, outcome.Command)
	}
	return strings.Join(commands, ",")
}

func TestDispatchRunsHighestPriorityFirst(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "low", Timeout: time.Second},
		{Event: PreToolUse, Command: "high", Priority: 10, Timeout: time.Second},
		{Event: PreToolUse, Command: "mid", Priority: 5, Timeout: time.Second},
	}}

	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), config, Payload{Event: "PreToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := outcomeCommands(outcomes), "high,mid,low"; got != want {
		t.Fatalf("priority order = %q, want %q", got, want)
	}
}

func TestDispatchDefaultPriorityKeepsDeclarationOrder(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config := &Config{Commands: []Command{
		{Event: PostToolUse, Command: "first", Timeout: time.Second},
		{Event: PostToolUse, Command: "second", Timeout: time.Second},
		{Event: PostToolUse, Command: "third", Timeout: time.Second},
	}}

	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), config, Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := outcomeCommands(outcomes), "first,second,third"; got != want {
		t.Fatalf("default order = %q, want %q", got, want)
	}
}

func TestDispatchStopPropagationShortCircuits(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	cases := []struct {
		name     string
		commands []Command
		want     string
	}{
		{
			name: "highest priority stops every later hook",
			commands: []Command{
				{Event: PreToolUse, Command: "low", Timeout: time.Second},
				{Event: PreToolUse, Command: "mid", Priority: 5, Timeout: time.Second},
				{Event: PreToolUse, Command: "high", Priority: 10, StopPropagation: true, Timeout: time.Second},
			},
			want: "high",
		},
		{
			name: "stopping hook shields lower priorities only",
			commands: []Command{
				{Event: PreToolUse, Command: "low", Timeout: time.Second},
				{Event: PreToolUse, Command: "mid", Priority: 5, StopPropagation: true, Timeout: time.Second},
				{Event: PreToolUse, Command: "high", Priority: 10, Timeout: time.Second},
			},
			want: "high,mid",
		},
		{
			name: "chain ends at the first hook that stops",
			commands: []Command{
				{Event: PreToolUse, Command: "low", Timeout: time.Second},
				{Event: PreToolUse, Command: "mid", Priority: 5, StopPropagation: true, Timeout: time.Second},
				{Event: PreToolUse, Command: "high", Priority: 10, StopPropagation: true, Timeout: time.Second},
			},
			want: "high",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := &Config{Commands: test.commands}
			outcomes, err := helperDispatcher(5*time.Second).Run(
				context.Background(), config, Payload{Event: "PreToolUse"})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := outcomeCommands(outcomes); got != test.want {
				t.Fatalf("executed = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDispatchRejectsUnknownEvents(t *testing.T) {
	_, err := helperDispatcher(time.Second).Dispatch(context.Background(), &Config{}, Payload{Event: "SesionStart"})
	if err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("unknown events should be rejected: %v", err)
	}
}

func TestDispatchDocumentRequiresADocument(t *testing.T) {
	if _, err := helperDispatcher(time.Second).DispatchDocument(context.Background(), nil, Payload{Event: "Stop"}); err != ErrUntrusted {
		t.Fatalf("expected ErrUntrusted, got %v", err)
	}
}

func TestRunCommandUsesTheShell(t *testing.T) {
	// 宽松的超时：在完整的并行 `go test ./...`（或 -race）下，
	// shell 可能要几秒才能启动。本测试检查使用的是 shell 且输出被捕获，
	// 而不是严格的挂钟时间上限。
	dispatcher := &Dispatcher{Timeout: 30 * time.Second}
	command := Command{Event: SessionStart, Command: "echo argent-hook", Timeout: 30 * time.Second}
	outcome := dispatcher.RunCommand(context.Background(), command, Payload{Event: "SessionStart", CWD: t.TempDir()})
	if outcome.Err != nil || outcome.ExitCode != 0 || outcome.TimedOut {
		t.Fatalf("shell command failed: %+v", outcome)
	}
	if !strings.Contains(outcome.Stdout, "argent-hook") {
		t.Fatalf("stdout not captured: %q (stderr %q)", outcome.Stdout, outcome.Stderr)
	}
}

func TestRunCommandCustomShellSkipsBashProbe(t *testing.T) {
	// findBash 在 Windows 上以两秒超时探测最多五个解释器，
	// 且它在 runCtx 存在之前运行，因此其开销不受
	// 钩子超时限制。提供了 Shell 的调用方已经选好了
	// 解释器；RunCommand 绝不应再探测默认解释器。
	oldBashRuns := bashRuns
	var probes atomic.Int32
	bashRuns = func(string) bool { probes.Add(1); return false }
	t.Cleanup(func() {
		bashRuns = oldBashRuns
		bashOnce = sync.Once{}
		bashPath = ""
	})
	bashOnce = sync.Once{}
	bashPath = ""

	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	used := false
	dispatcher := &Dispatcher{
		Timeout: 5 * time.Second,
		Shell: func(string) (string, []string) {
			used = true
			return os.Args[0], nil
		},
	}
	command := Command{Event: PostToolUse, Command: "run-hook", Timeout: 5 * time.Second}
	outcome := dispatcher.RunCommand(context.Background(), command, Payload{Event: "PostToolUse"})
	if !used || outcome.ExitCode != 0 {
		t.Fatalf("custom shell was not used: used=%t %+v", used, outcome)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("custom Shell triggered %d bash probes, want 0", got)
	}

	// 完整性检查：没有 Shell 覆盖时默认路径仍必须探测，因此
	// 上面的零计数不可能是探测被禁用的假象。
	plain := &Dispatcher{Timeout: 5 * time.Second}
	plain.RunCommand(context.Background(), command, Payload{Event: "PostToolUse"})
	if got := probes.Load(); got == 0 {
		t.Fatal("default shell did not probe for bash; test seam is inert")
	}
}

func TestRunCommandWaitDelayBoundsLingeringChildOutput(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "lingering")
	exitFile := filepath.Join(t.TempDir(), "child-exit")
	t.Setenv("ARGENT_HOOK_EXIT_FILE", exitFile)
	// 在本二进制文件退出前释放子进程；在 Windows 上，存活的子进程
	// 否则会在 go test 清理期间锁定测试可执行文件。
	t.Cleanup(func() {
		_ = os.WriteFile(exitFile, nil, 0o600)
		time.Sleep(100 * time.Millisecond)
	})

	dispatcher := helperDispatcher(300 * time.Millisecond)
	command := Command{Event: PostToolUse, Command: "run-hook", Timeout: 300 * time.Millisecond}

	started := time.Now()
	outcome := dispatcher.RunCommand(context.Background(), command, Payload{Event: "PostToolUse"})
	elapsed := time.Since(started)

	if outcome.Err != nil {
		t.Fatalf("hook failed to run: %v", outcome.Err)
	}
	// helper 立即退出，但其子进程继承被捕获的 stdout 和
	// stderr 管道并保持打开。钩子自身的超时必须限制
	// 挂钟等待；没有 WaitDelay 时运行需要数秒而非
	// ~waitDelay.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("lingering child blocked the hook for %s", elapsed)
	}
}

func waitForHelperFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hook helper never created %s", path)
}

// dispatchLingering 运行一个在截止时间前刚好退出的钩子，同时
// 后台子进程在截止时间之后仍保持被捕获的管道打开。于是截止时间
// 在退出后的管道排空期间触发，旧的 runCtx.Err() 检查
// 将其误判为钩子超时。helper 在子进程持有管道时发出信号，
// 使时序即使在 -race 下也保持确定。
func dispatchLingering(t *testing.T, event Event, exitCode int) Outcome {
	t.Helper()
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	goFile := filepath.Join(dir, "go")
	exitFile := filepath.Join(dir, "child-exit")
	t.Setenv("ARGENT_HOOK_HELPER", "lingering-signal")
	t.Setenv("ARGENT_HOOK_EXIT_CODE", strconv.Itoa(exitCode))
	t.Setenv("ARGENT_HOOK_READY_FILE", readyFile)
	t.Setenv("ARGENT_HOOK_GO_FILE", goFile)
	t.Setenv("ARGENT_HOOK_EXIT_FILE", exitFile)
	t.Cleanup(func() {
		_ = os.WriteFile(exitFile, nil, 0o600)
		time.Sleep(100 * time.Millisecond)
	})

	// 在启动计时之前预热一次性的 bash 探测；在 Windows 上其
	// 首次调用可能花两秒检查候选解释器。
	findBash()

	const timeout = 3 * time.Second
	dispatcher := helperDispatcher(timeout)
	command := Command{Event: event, Command: "run-hook", Timeout: timeout}
	type result struct{ outcome Outcome }
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		done <- result{dispatcher.RunCommand(context.Background(), command, Payload{Event: string(event)})}
	}()

	waitForHelperFile(t, readyFile)
	// 将进程退出安排在截止时间前 400ms。届时它已消失，因此只有延迟的管道排空越过截止时间。
	// context 到期时它已消失，因此只有延迟的管道排空越过截止时间。
	if delay := time.Until(started.Add(2600 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	_ = os.WriteFile(goFile, nil, 0o600)
	return (<-done).outcome
}

func TestRunCommandLingeringChildKeepsRealExitStatus(t *testing.T) {
	// 钩子进程远在其截止时间之前就已退出；只有退出后的
	// 管道等待越过了它。其真实退出状态必须保留，且它
	// 不得被误标为超时。
	outcome := dispatchLingering(t, PostToolUse, 1)
	if outcome.TimedOut {
		t.Fatalf("hook mislabelled as timed out: %+v", outcome)
	}
	if outcome.Err != nil || outcome.ExitCode != 1 {
		t.Fatalf("real exit status lost: %+v", outcome)
	}
	if result := Summarize([]Outcome{outcome}); result.Blocked ||
		!strings.Contains(result.Message, "exit 1") {
		t.Fatalf("summary lost the real failure: %+v", result)
	}
}

func TestRunCommandLingeringChildKeepsBlockingExit(t *testing.T) {
	// 同样的管道排空超时不得吞掉真实的 exit-2 拦截：拦截
	// 信号只对未超时的钩子生效。
	outcome := dispatchLingering(t, PreToolUse, 2)
	if outcome.TimedOut {
		t.Fatalf("blocking hook mislabelled as timed out: %+v", outcome)
	}
	if outcome.ExitCode != 2 || !outcome.Blocked {
		t.Fatalf("exit-2 block lost: %+v", outcome)
	}
	result := Summarize([]Outcome{outcome})
	if !result.Blocked || !strings.Contains(result.Message, "blocked") {
		t.Fatalf("aggregate lost the block: %+v", result)
	}
}

// processStateForExit 运行 helper 只是为了获得真实的 *os.ProcessState
// 及已知退出状态；分类测试需要真实状态，
// 及已知退出状态；分类测试需要真实状态，因为其字段未导出。
func processStateForExit(t *testing.T, code int) *os.ProcessState {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"ARGENT_HOOK_HELPER=exitcode",
		fmt.Sprintf("ARGENT_HOOK_EXIT_CODE=%d", code))
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		t.Fatalf("helper did not produce a ProcessState for exit %d", code)
	}
	return cmd.ProcessState
}

func TestClassifyRunResultKeepsExitStatusAfterPipeDrainTimeout(t *testing.T) {
	// 持有被捕获管道的后台子进程使 Wait 在截止时间后报告
	// ErrWaitDelay，即使钩子已经退出。
	// context 的状态不得覆盖真实的退出码。
	for _, code := range []int{0, 1, 2} {
		state := processStateForExit(t, code)
		gotCode, timedOut, err := classifyRunResult(exec.ErrWaitDelay, state, false)
		if gotCode != code || timedOut || err != nil {
			t.Fatalf("exit %d: got (%d, timedOut %t, %v), want (%d, false, nil)",
				code, gotCode, timedOut, err, code)
		}
		gotCode, timedOut, err = classifyRunResult(context.DeadlineExceeded, state, false)
		if gotCode != code || timedOut || err != nil {
			t.Fatalf("exit %d with late deadline: got (%d, %t, %v)", code, gotCode, timedOut, err)
		}
	}
}

func TestClassifyRunResultReportsDeadlineKill(t *testing.T) {
	code, timedOut, err := classifyRunResult(nil, nil, true)
	if code != -1 || !timedOut || err != nil {
		t.Fatalf("deadline kill = (%d, %t, %v), want (-1, true, nil)", code, timedOut, err)
	}
}

func TestClassifyRunResultKeepsLaunchError(t *testing.T) {
	launchErr := errors.New("spawn failed")
	code, timedOut, err := classifyRunResult(launchErr, nil, false)
	if code != -1 || timedOut || !errors.Is(err, launchErr) {
		t.Fatalf("launch error = (%d, %t, %v), want (-1, false, spawn failed)", code, timedOut, err)
	}
}

func TestOutcomeMessageDescribesFailures(t *testing.T) {
	cases := []struct {
		outcome Outcome
		want    string
	}{
		{Outcome{Event: PreToolUse, Command: "x", ExitCode: 0}, "succeeded"},
		{Outcome{Event: PreToolUse, Command: "x", ExitCode: 2, Blocked: true, Stdout: "no"}, "blocked"},
		{Outcome{Event: PreToolUse, Command: "x", ExitCode: 1, Stderr: "boom"}, "boom"},
		{Outcome{Event: PreToolUse, Command: "x", TimedOut: true, ExitCode: -1}, "timed out"},
	}
	for _, test := range cases {
		if got := test.outcome.Message(); !strings.Contains(got, test.want) {
			t.Fatalf("Message() = %q, want %q", got, test.want)
		}
	}
}

func TestClipHelpersKeepUTF8Valid(t *testing.T) {
	clippers := map[string]func(string) string{
		"clip":        clip,
		"clipCommand": clipCommand,
	}
	// A rune that straddles the byte limit must not be split. "界" is 3 bytes
	// 而 "🙂" 是 4 字节，因此对某些重复次数，400 和 120 必定落在 rune 中间。
	// 对某些重复次数必定落在 rune 中间。
	for _, rune := range []string{"界", "🙂", "a界🙂"} {
		for name, clipFn := range clippers {
			got := clipFn(strings.Repeat(rune, 200))
			if !utf8.ValidString(got) {
				t.Errorf("%s over %q produced invalid UTF-8: %q", name, rune, got)
			}
			if !strings.HasSuffix(got, "…") {
				t.Errorf("%s over %q did not clip: %q", name, rune, got)
			}
		}
	}
	// 限制内的字符串被原样返回（没有多余的省略号）。
	for name, clipFn := range clippers {
		if got := clipFn("short"); got != "short" {
			t.Errorf("%s changed a short string: %q", name, got)
		}
	}
}

func TestOutcomeMessageIsRuneSafeWhenClipped(t *testing.T) {
	outcome := Outcome{
		Event:    PreToolUse,
		Command:  strings.Repeat("🙂", 80),  // 320 bytes > clipCommand's 120
		Stdout:   strings.Repeat("界", 200), // 600 bytes > summaryClip's 400
		ExitCode: 1,
	}
	got := outcome.Message()
	if !utf8.ValidString(got) {
		t.Fatalf("Message() produced invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Fatalf("Message() lost its prefix: %q", got)
	}
}

// --- 并行分发（Crush 审计移植） -----------------------------------

func TestDispatchRunsHooksConcurrently(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "sleepms")
	t.Setenv("ARGENT_HOOK_SLEEP_MS", "500")

	const hooks = 8
	commands := make([]Command, 0, hooks)
	for i := 0; i < hooks; i++ {
		commands = append(commands, Command{
			Event: PostToolUse, Command: fmt.Sprintf("sleep-%d", i), Timeout: 6 * time.Second,
		})
	}

	started := time.Now()
	outcomes, err := helperDispatcher(6*time.Second).Run(
		context.Background(), &Config{Commands: commands}, Payload{Event: "PostToolUse"})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != hooks {
		t.Fatalf("expected %d outcomes, got %d: %+v", hooks, len(outcomes), outcomes)
	}
	for i, outcome := range outcomes {
		if outcome.ExitCode != 0 {
			t.Fatalf("hook %d failed: %+v", i, outcome)
		}
	}
	// 串行执行至少需要 8×500ms = 4s。宽松的 2.5s 上限
	// 为 -race / 负载较重的 CI 下缓慢的进程启动留出余量，同时仍能
	// 捕获退化回旧串行循环的回归。
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("hooks ran serially: %s for %d × 500ms sleeps", elapsed, hooks)
	}
}

func TestDispatchAggregatesParallelOutcomes(t *testing.T) {
	commands := []Command{
		{Event: PreToolUse, Command: "one", Timeout: time.Second},
		{Event: PreToolUse, Command: "two", Timeout: time.Second},
		{Event: PreToolUse, Command: "three", Timeout: time.Second},
	}
	config := &Config{Commands: commands}

	t.Run("blocking hooks", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "block")
		outcomes, err := helperDispatcher(5*time.Second).Run(
			context.Background(), config, Payload{Event: "PreToolUse"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(outcomes) != 3 {
			t.Fatalf("expected 3 outcomes, got %d", len(outcomes))
		}
		for _, outcome := range outcomes {
			if !outcome.Blocked || outcome.ExitCode != 2 {
				t.Fatalf("parallel exit-2 hook lost its block flag: %+v", outcome)
			}
		}
		result := Summarize(outcomes)
		if !result.Blocked || strings.Count(result.Message, "blocked") != 3 {
			t.Fatalf("aggregate lost the blocks: %+v", result)
		}
	})

	t.Run("failing hooks", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "fail")
		outcomes, err := helperDispatcher(5*time.Second).Run(
			context.Background(), config, Payload{Event: "PreToolUse"})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		result := Summarize(outcomes)
		if result.Blocked {
			t.Fatal("exit 1 must warn, not block")
		}
		if strings.Count(result.Message, "exit 1") != 3 ||
			strings.Count(result.Message, "hook exploded") != 3 {
			t.Fatalf("warnings were not all aggregated: %q", result.Message)
		}
	})

	t.Run("mixed outcomes", func(t *testing.T) {
		// 顺序和拦截标志在聚合后得以保留，无论并发钩子
		// 以何种顺序完成。
		outcomes := []Outcome{
			{Event: PreToolUse, Command: "ok", ExitCode: 0},
			{Event: PreToolUse, Command: "bad", ExitCode: 1, Stderr: "hook exploded"},
			{Event: PreToolUse, Command: "stop", ExitCode: 2, Blocked: true},
		}
		result := Summarize(outcomes)
		if !result.Blocked {
			t.Fatal("a blocking outcome must win the aggregate")
		}
		for _, want := range []string{"blocked", "exit 1", "hook exploded"} {
			if !strings.Contains(result.Message, want) {
				t.Fatalf("aggregate message lost %q: %q", want, result.Message)
			}
		}
	})
}

func TestDispatchDeduplicatesByCommand(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "append")
	logPath := filepath.Join(t.TempDir(), "hooks.log")
	t.Setenv("ARGENT_HOOK_LOG", logPath)

	config := &Config{Commands: []Command{
		{Event: PostToolUse, Command: "dup", Timeout: time.Second},
		{Event: PostToolUse, Command: "unique", Timeout: time.Second},
		{Event: PostToolUse, Command: "dup", Timeout: time.Second},
	}}
	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), config, Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := outcomeCommands(outcomes), "dup,unique"; got != want {
		t.Fatalf("dedup outcomes = %q, want %q", got, want)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Fatalf("hook executions = %d, want 2 (duplicate must not run twice)", got)
	}
}

func TestDispatchDedupKeepsHighestPriorityOccurrence(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config := &Config{Commands: []Command{
		{Event: PostToolUse, Command: "same", Timeout: time.Second},
		{Event: PostToolUse, Command: "other", Priority: 5, Timeout: time.Second},
		{Event: PostToolUse, Command: "same", Priority: 10, Timeout: time.Second},
	}}
	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), config, Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := outcomeCommands(outcomes), "same,other"; got != want {
		t.Fatalf("dedup order = %q, want %q", got, want)
	}
	if outcomes[0].Command != "same" {
		t.Fatalf("higher-priority duplicate must win: %+v", outcomes[0])
	}
}

func TestDispatchStopPropagationWithDuplicates(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "x", Priority: 10, StopPropagation: true, Timeout: time.Second},
		{Event: PreToolUse, Command: "x", Priority: 5, Timeout: time.Second},
		{Event: PreToolUse, Command: "y", Timeout: time.Second},
	}}
	outcomes, err := helperDispatcher(5*time.Second).Run(
		context.Background(), config, Payload{Event: "PreToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := outcomeCommands(outcomes), "x"; got != want {
		t.Fatalf("stop boundary regressed: got %q, want %q", got, want)
	}
}

func TestDispatchContextCancellation(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "echo")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := helperDispatcher(time.Second).Run(
			ctx, configFor(t, PostToolUse, "", ""), Payload{Event: "PostToolUse"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("cancelled mid-run", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "sleep")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(50*time.Millisecond, cancel)

		commands := make([]Command, 0, 4)
		for i := 0; i < 4; i++ {
			commands = append(commands, Command{
				Event: PostToolUse, Command: fmt.Sprintf("sleep-%d", i), Timeout: 6 * time.Second,
			})
		}
		started := time.Now()
		outcomes, err := helperDispatcher(6*time.Second).Run(
			ctx, &Config{Commands: commands}, Payload{Event: "PostToolUse"})
		elapsed := time.Since(started)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v (outcomes %+v)", err, outcomes)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("cancellation did not stop hooks promptly: %s", elapsed)
		}
	})
}

func TestFireFileChangedDispatchesPath(t *testing.T) {
	var captured Payload
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, payload Payload) Outcome {
			captured = payload
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}
	config := configFor(t, FileChanged, "", "")
	result, err := dispatcher.FireFileChanged(context.Background(), config, "internal/hooks/hooks.go")
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if result.Blocked || result.Message != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if captured.Event != string(FileChanged) {
		t.Fatalf("event = %q, want FileChanged", captured.Event)
	}
	if captured.Path != "internal/hooks/hooks.go" {
		t.Fatalf("path = %q, want the changed file", captured.Path)
	}
}

func TestFireFileChangedMatchesHooksByPath(t *testing.T) {
	// FileChanged 匹配器描述受影响的文件（"*.go"），但
	// 便捷包装器只填充 Payload.Path。匹配必须查看那里。
	config, err := ParseConfig([]byte(`{"hooks":{"FileChanged":[
		{"matcher":"*.go","hooks":[{"command":"go-hook"}]},
		{"matcher":"*.md","hooks":[{"command":"md-hook"}]},
		{"hooks":[{"command":"any-hook"}]}
	]}}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	ran := make(chan string, 4)
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, payload Payload) Outcome {
			ran <- command.Command
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}
	if _, err := dispatcher.FireFileChanged(context.Background(), config, "internal/hooks/hooks.go"); err != nil {
		t.Fatalf("fire: %v", err)
	}
	close(ran)

	seen := make(map[string]bool)
	for name := range ran {
		seen[name] = true
	}
	if !seen["go-hook"] || !seen["any-hook"] {
		t.Fatalf("matching FileChanged hooks did not run: %v", seen)
	}
	if seen["md-hook"] {
		t.Fatalf("non-matching FileChanged hook ran: %v", seen)
	}
}

func TestRunFileChangedMatcherUsesPathNotTool(t *testing.T) {
	// 该路径是 FileChanged 文档规定的匹配值。即使调用方
	// 也提供了 Tool，匹配器仍必须针对路径测试。
	config, err := ParseConfig([]byte(`{"hooks":{"FileChanged":[
		{"matcher":"*.go","hooks":[{"command":"go-hook"}]},
		{"matcher":"write","hooks":[{"command":"tool-hook"}]}
	]}}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	ran := make(chan string, 4)
	dispatcher := &Dispatcher{
		Runner: func(_ context.Context, command Command, payload Payload) Outcome {
			ran <- command.Command
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}
	_, err = dispatcher.Run(context.Background(), config, Payload{
		Event: "FileChanged", Tool: "write", Path: "notes.md",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	close(ran)

	var got []string
	for name := range ran {
		got = append(got, name)
	}
	if len(got) != 0 {
		t.Fatalf("no hook should match path notes.md, got %v", got)
	}
}

func TestPlanRunGroupsByPriority(t *testing.T) {
	// planRun 接收已按优先级降序排序的命令，这是 Run
	// 在其稳定排序后保证的。
	commands := []Command{
		{Command: "high-a", Priority: 10},
		{Command: "high-b", Priority: 10},
		{Command: "mid", Priority: 5},
		{Command: "low", Priority: 0},
	}
	tiers := planRun(commands)
	if len(tiers) != 3 {
		t.Fatalf("tier count = %d, want 3: %+v", len(tiers), tiers)
	}
	for i, want := range [][]string{{"high-a", "high-b"}, {"mid"}, {"low"}} {
		var got []string
		for _, command := range tiers[i] {
			got = append(got, command.Command)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("tier %d = %v, want %v", i, got, want)
		}
	}
}
