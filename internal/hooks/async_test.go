package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// asyncConfig builds a config with an optional background hook and an optional
// synchronous hook. Both get generous timeouts so only the behaviour under
// test (background execution) can make a run slow.
func asyncConfig(asyncName, syncName string) *Config {
	commands := make([]Command, 0, 2)
	if asyncName != "" {
		commands = append(commands, Command{
			Event: PostToolUse, Command: asyncName, Async: true, Timeout: 5 * time.Second,
		})
	}
	if syncName != "" {
		commands = append(commands, Command{
			Event: PostToolUse, Command: syncName, Timeout: 5 * time.Second,
		})
	}
	return &Config{Commands: commands}
}

func TestDispatchRunsAsyncHookWithoutBlocking(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32

	dispatcher := &Dispatcher{
		Runner: func(ctx context.Context, command Command, payload Payload) Outcome {
			calls.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			// Bounded wait so a regression that makes Run wait for the async
			// hook fails on elapsed time instead of hanging the test binary.
			select {
			case <-release:
			case <-time.After(3 * time.Second):
			}
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}

	begin := time.Now()
	outcomes, err := dispatcher.Run(context.Background(), asyncConfig("bg", ""), Payload{Event: "PostToolUse"})
	elapsed := time.Since(begin)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("async hook leaked into the decision: %+v", outcomes)
	}
	if elapsed > time.Second {
		t.Fatalf("Run blocked on the async hook for %s", elapsed)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async hook was never invoked")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("invocations = %d, want 1", got)
	}

	close(release)
	dispatcher.WaitAsync()
	if got := calls.Load(); got != 1 {
		t.Fatalf("async hook ran %d times, want 1", got)
	}
}

func TestDispatchAsyncHookPanicIsIsolated(t *testing.T) {
	var calls atomic.Int32
	dispatcher := &Dispatcher{
		Runner: func(ctx context.Context, command Command, payload Payload) Outcome {
			calls.Add(1)
			if command.Command == "boom" {
				panic("async kaboom")
			}
			return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0}
		},
	}
	sink := &telemetrySink{}
	dispatcher.Telemetry = sink.observe

	outcomes, err := dispatcher.Run(context.Background(),
		asyncConfig("boom", "sync-ok"), Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Command != "sync-ok" || outcomes[0].ExitCode != 0 {
		t.Fatalf("async panic changed the synchronous decision: %+v", outcomes)
	}
	dispatcher.WaitAsync()

	if got := calls.Load(); got != 2 {
		t.Fatalf("invocations = %d, want 2 (one async, one sync)", got)
	}
	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("telemetry records = %d, want 2: %+v", len(records), records)
	}
	var isolated bool
	for _, record := range records {
		if record.Name == "boom" && record.Err != nil && errors.Is(record.Err, ErrCallbackPanic) {
			isolated = true
		}
	}
	if !isolated {
		t.Fatalf("async panic was not contained: %+v", records)
	}
}

type asyncCtxSnapshot struct {
	err         error
	deadline    time.Time
	hasDeadline bool
}

func TestDispatchAsyncHookContextIsDetachedAndBounded(t *testing.T) {
	first := make(chan asyncCtxSnapshot, 1)
	second := make(chan error, 1)
	release := make(chan struct{})

	dispatcher := &Dispatcher{
		Runner: func(ctx context.Context, command Command, payload Payload) Outcome {
			deadline, ok := ctx.Deadline()
			first <- asyncCtxSnapshot{err: ctx.Err(), deadline: deadline, hasDeadline: ok}
			select {
			case <-release:
			case <-time.After(3 * time.Second):
			}
			second <- ctx.Err()
			return Outcome{Event: command.Event, Command: command.Command}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := dispatcher.Run(ctx, asyncConfig("bg", ""), Payload{Event: "PostToolUse"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	snapshot := <-first
	if snapshot.err != nil {
		t.Fatalf("async context already errored at start: %v", snapshot.err)
	}
	if !snapshot.hasDeadline {
		t.Fatal("async hook has no timeout deadline")
	}
	if remaining := time.Until(snapshot.deadline); remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("async deadline is %v from now, want within 5s", remaining)
	}

	// Cancelling the parent after Run returns must not cancel the detached hook.
	cancel()
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("async context inherited parent cancellation: %v", err)
	}
	dispatcher.WaitAsync()
}

func TestPlanAsyncRespectsDedupAndStopPropagation(t *testing.T) {
	commands := []Command{
		{Command: "a", Async: true},
		{Command: "b"},
		{Command: "a", Async: true},
		{Command: "c", Async: true, StopPropagation: true},
		{Command: "d", Async: true},
	}
	got := planAsync(commands)
	names := make([]string, 0, len(got))
	for _, command := range got {
		names = append(names, command.Command)
	}
	if nameList := strings.Join(names, ","); nameList != "a,c" {
		t.Fatalf("planAsync = %q, want %q", nameList, "a,c")
	}
}

func TestDispatchAsyncHookReallyExecutes(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "append")
	logPath := filepath.Join(t.TempDir(), "async.log")
	t.Setenv("ARGENT_HOOK_LOG", logPath)

	dispatcher := helperDispatcher(5 * time.Second)
	config := &Config{Commands: []Command{
		{Event: PostToolUse, Command: "bg", Async: true, Timeout: 5 * time.Second},
	}}
	outcomes, err := dispatcher.Run(context.Background(), config, Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("async hook leaked into the decision: %+v", outcomes)
	}

	dispatcher.WaitAsync()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read async hook log: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 1 {
		t.Fatalf("async hook executions = %d, want 1", got)
	}
}
