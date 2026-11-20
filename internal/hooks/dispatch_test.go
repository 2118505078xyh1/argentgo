package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
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
