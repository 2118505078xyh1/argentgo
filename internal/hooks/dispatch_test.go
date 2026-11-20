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
	"testing"
	"time"
	"unicode/utf8"
)

// TestMain doubles as the hook command under test: when the helper variable is
// set, this process behaves like a tiny hook script instead of a test binary.
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
		// Bounded sleep used by the parallel-execution tests. Given a generous
		// hook timeout, N concurrent hooks take ~one sleep, not N sleeps.
		if ms, err := strconv.Atoi(os.Getenv("ARGENT_HOOK_SLEEP_MS")); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	case "append":
		// Record one line per invocation so tests can count executions. A
		// single small append keeps concurrent writers from interleaving.
		if path := os.Getenv("ARGENT_HOOK_LOG"); path != "" {
			if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = file.WriteString("x\n")
				_ = file.Close()
			}
		}
	case "lingering":
		// Exit immediately but leave a child holding this process's stdout
		// and stderr — the pipes the dispatcher captures. Without a WaitDelay
		// the dispatcher's Wait() blocks until the child releases them.
		// ARGENT_HOOK_EXIT_CODE lets a test check that a real exit status
		// (notably exit 2) survives the post-exit pipe wait.
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "ARGENT_HOOK_HELPER=lingering-child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		code, _ := strconv.Atoi(os.Getenv("ARGENT_HOOK_EXIT_CODE"))
		os.Exit(code)
	case "lingering-child":
		// Hold the inherited pipes until the test signals it is done (with a
		// fallback bound so a failed test cannot leak a process forever).
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

// helperDispatcher runs the test binary instead of a shell.
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
	// Generous timeout: under a full parallel `go test ./...` (or -race) the
	// shell can take several seconds just to start. This test checks that the
	// shell is used and output captured, not a tight wall-clock bound.
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

func TestRunCommandWaitDelayBoundsLingeringChildOutput(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "lingering")
	exitFile := filepath.Join(t.TempDir(), "child-exit")
	t.Setenv("ARGENT_HOOK_EXIT_FILE", exitFile)
	// Release the child before this binary exits; on Windows a live child
	// would otherwise keep the test executable locked during go test cleanup.
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
	// The helper exits at once but its child inherits the captured stdout and
	// stderr pipes and keeps them open. The hook's own timeout must bound the
	// wall-clock wait; without WaitDelay the run takes seconds instead of
	// ~waitDelay.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("lingering child blocked the hook for %s", elapsed)
	}
}

// dispatchLingering runs the "lingering" helper with a 2s timeout. The helper
// exits with exitCode well before the deadline but leaves a child holding the
// captured pipes, so the deadline fires while Wait is still draining them.
// The budget is generous because a -race helper process can take a while to
// start; the child's 5s fallback keeps the pipe-drain window open past 2s.
// The child is released on cleanup so it cannot outlive the test.
func dispatchLingering(t *testing.T, event Event, exitCode int) Outcome {
	t.Helper()
	t.Setenv("ARGENT_HOOK_HELPER", "lingering")
	t.Setenv("ARGENT_HOOK_EXIT_CODE", strconv.Itoa(exitCode))
	exitFile := filepath.Join(t.TempDir(), "child-exit")
	t.Setenv("ARGENT_HOOK_EXIT_FILE", exitFile)
	t.Cleanup(func() {
		_ = os.WriteFile(exitFile, nil, 0o600)
		time.Sleep(100 * time.Millisecond)
	})
	const timeout = 2 * time.Second
	dispatcher := helperDispatcher(timeout)
	command := Command{Event: event, Command: "run-hook", Timeout: timeout}
	return dispatcher.RunCommand(context.Background(), command, Payload{Event: string(event)})
}

func TestRunCommandLingeringChildIsNotMislabelledAsTimeout(t *testing.T) {
	// The hook process exited successfully long before its deadline. Only the
	// post-exit pipe wait ran into it, which is what WaitDelay is for; the
	// successful exit status must survive.
	outcome := dispatchLingering(t, PostToolUse, 0)
	if outcome.TimedOut {
		t.Fatalf("successful hook mislabelled as timed out: %+v", outcome)
	}
	if outcome.Err != nil || outcome.ExitCode != 0 {
		t.Fatalf("successful hook reported as failed: %+v", outcome)
	}
	if result := Summarize([]Outcome{outcome}); result.Blocked || result.Message != "" {
		t.Fatalf("successful hook produced a spurious warning: %+v", result)
	}
}

func TestRunCommandLingeringChildKeepsBlockingExit(t *testing.T) {
	// The same pipe-drain overrun must not swallow a real exit-2 block: the
	// block signal is only honoured for a hook that did not time out.
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
	// and "🙂" is 4 bytes, so 400 and 120 are guaranteed to land mid-rune for
	// some repetition counts.
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
	// Strings within the limit are returned unchanged (no stray ellipsis).
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

// --- parallel dispatch (Crush audit port) -----------------------------------

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
	// Serial execution would take at least 8×500ms = 4s. A generous 2.5s bound
	// leaves room for slow process startup under -race / a loaded CI while still
	// catching a regression to the old serial loop.
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
		// Order and the block flag survive aggregation regardless of the order
		// in which the concurrent hooks finished.
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
	// FileChanged matchers describe the affected file ("*.go"), but the
	// convenience wrapper only fills Payload.Path. Matching must look there.
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
	// The path is the documented match value for FileChanged. Even when a
	// caller also supplies a Tool, a matcher must be tested against the path.
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
	// planRun receives commands already sorted by descending priority, as Run
	// guarantees after its stable sort.
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
