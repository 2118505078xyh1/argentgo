package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
