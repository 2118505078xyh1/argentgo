package hooks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// telemetrySink 是并发安全的观察者：钩子回调从钩子 goroutine 运行，因此记录必须加锁（本包以
// 钩子 goroutine，因此记录必须加锁（本包以
// -race).
type telemetrySink struct {
	mu      sync.Mutex
	records []HookTelemetry
}

func (s *telemetrySink) observe(record HookTelemetry) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *telemetrySink) snapshot() []HookTelemetry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HookTelemetry(nil), s.records...)
}

func TestTelemetryRecordsElapsedAndIdentity(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "sleepms")
	t.Setenv("ARGENT_HOOK_SLEEP_MS", "150")

	sink := &telemetrySink{}
	dispatcher := helperDispatcher(5 * time.Second)
	dispatcher.Telemetry = sink.observe

	if _, err := dispatcher.Run(context.Background(),
		configFor(t, PreToolUse, "", ""), Payload{Event: "PreToolUse"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("telemetry records = %d, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record.Name != "run-hook" || record.Event != PreToolUse {
		t.Fatalf("identity lost: %+v", record)
	}
	if record.Elapsed < 100*time.Millisecond || record.Elapsed > 4*time.Second {
		t.Fatalf("elapsed = %s, want roughly the 150ms hook sleep", record.Elapsed)
	}
	if record.ExitCode != 0 || record.Failed() || record.Slow {
		t.Fatalf("successful fast hook reported as failed/slow: %+v", record)
	}
}

func TestTelemetryMarksSlowCalls(t *testing.T) {
	t.Run("slow hook", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "sleepms")
		t.Setenv("ARGENT_HOOK_SLEEP_MS", "200")

		sink := &telemetrySink{}
		dispatcher := helperDispatcher(5 * time.Second)
		dispatcher.Telemetry = sink.observe
		dispatcher.SlowThreshold = 120 * time.Millisecond

		if _, err := dispatcher.Run(context.Background(),
			configFor(t, PostToolUse, "", ""), Payload{Event: "PostToolUse"}); err != nil {
			t.Fatalf("run: %v", err)
		}
		records := sink.snapshot()
		if len(records) != 1 || !records[0].Slow {
			t.Fatalf("200ms hook must be marked slow at 120ms: %+v", records)
		}
	})

	t.Run("fast hook", func(t *testing.T) {
		t.Setenv("ARGENT_HOOK_HELPER", "echo")

		sink := &telemetrySink{}
		dispatcher := helperDispatcher(5 * time.Second)
		dispatcher.Telemetry = sink.observe
		dispatcher.SlowThreshold = 5 * time.Second

		if _, err := dispatcher.Run(context.Background(),
			configFor(t, PostToolUse, "", ""), Payload{Event: "PostToolUse"}); err != nil {
			t.Fatalf("run: %v", err)
		}
		records := sink.snapshot()
		if len(records) != 1 || records[0].Slow {
			t.Fatalf("instant hook must not be marked slow: %+v", records)
		}
	})
}

func TestTelemetryRecordsFailures(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		timeout     time.Duration
		configExtra string
		shell       func(string) (string, []string)
		wantExit    int
		wantErr     bool
		wantTimeout bool
		wantBlocked bool
	}{
		{name: "non-zero exit", mode: "fail", timeout: 5 * time.Second, wantExit: 1},
		{name: "block signal", mode: "block", timeout: 5 * time.Second, wantExit: 2, wantBlocked: true},
		{name: "timeout", mode: "sleep", timeout: 200 * time.Millisecond, configExtra: `,"timeoutMs":200`, wantExit: -1, wantTimeout: true},
		{
			name:     "could not run",
			timeout:  time.Second,
			shell:    func(string) (string, []string) { return "argent-no-such-hook-binary-xyz", nil },
			wantExit: -1, wantErr: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.mode != "" {
				t.Setenv("ARGENT_HOOK_HELPER", test.mode)
			}
			sink := &telemetrySink{}
			dispatcher := helperDispatcher(test.timeout)
			dispatcher.Telemetry = sink.observe
			if test.shell != nil {
				dispatcher.Shell = test.shell
			}
			if _, err := dispatcher.Run(context.Background(),
				configFor(t, PreToolUse, "", test.configExtra), Payload{Event: "PreToolUse"}); err != nil {
				t.Fatalf("run: %v", err)
			}
			records := sink.snapshot()
			if len(records) != 1 {
				t.Fatalf("telemetry records = %d, want 1: %+v", len(records), records)
			}
			record := records[0]
			if !record.Failed() {
				t.Fatalf("failure not flagged: %+v", record)
			}
			if record.ExitCode != test.wantExit {
				t.Errorf("exit code = %d, want %d: %+v", record.ExitCode, test.wantExit, record)
			}
			if (record.Err != nil) != test.wantErr {
				t.Errorf("Err = %v, wantErr %t: %+v", record.Err, test.wantErr, record)
			}
			if record.TimedOut != test.wantTimeout || record.Blocked != test.wantBlocked {
				t.Errorf("flags = (timedOut %t, blocked %t), want (%t, %t)",
					record.TimedOut, record.Blocked, test.wantTimeout, test.wantBlocked)
			}
		})
	}
}

func TestTelemetryDisabledByDefaultDoesNotRegress(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	result, err := helperDispatcher(5*time.Second).Dispatch(
		context.Background(), configFor(t, PreToolUse, "", ""), Payload{Event: "PreToolUse"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked || result.Message != "" {
		t.Fatalf("unexpected decision without telemetry: %+v", result)
	}
}

func TestTelemetryFromParallelHooksIsRaceFree(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "sleepms")
	t.Setenv("ARGENT_HOOK_SLEEP_MS", "100")

	const hooks = 6
	commands := make([]Command, 0, hooks)
	for i := 0; i < hooks; i++ {
		commands = append(commands, Command{
			Event: PostToolUse, Command: fmt.Sprintf("sleep-%d", i), Timeout: 3 * time.Second,
		})
	}

	sink := &telemetrySink{}
	dispatcher := helperDispatcher(3 * time.Second)
	dispatcher.Telemetry = sink.observe
	dispatcher.SlowThreshold = 50 * time.Millisecond

	if _, err := dispatcher.Run(context.Background(),
		&Config{Commands: commands}, Payload{Event: "PostToolUse"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	records := sink.snapshot()
	if len(records) != hooks {
		t.Fatalf("telemetry records = %d, want %d", len(records), hooks)
	}
	seen := make(map[string]bool, hooks)
	for _, record := range records {
		if seen[record.Name] {
			t.Fatalf("duplicate record for %q", record.Name)
		}
		seen[record.Name] = true
		if !record.Slow || record.Failed() {
			t.Fatalf("parallel hook not reported correctly: %+v", record)
		}
	}
	for i := 0; i < hooks; i++ {
		if name := fmt.Sprintf("sleep-%d", i); !seen[name] {
			t.Fatalf("missing telemetry record for %q", name)
		}
	}
}

func TestTelemetryPanicDoesNotBreakDispatch(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	dispatcher := helperDispatcher(5 * time.Second)
	dispatcher.Telemetry = func(HookTelemetry) { panic("bad observer") }

	result, err := dispatcher.Dispatch(
		context.Background(), configFor(t, PostToolUse, "", ""), Payload{Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if result.Blocked || strings.Contains(result.Message, "panicked") {
		t.Fatalf("telemetry panic leaked into the decision: %+v", result)
	}
}

func TestWithTelemetrySetsCallbackAndThreshold(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	sink := &telemetrySink{}
	dispatcher := WithTelemetry(sink.observe, time.Hour)
	dispatcher.Timeout = 5 * time.Second
	dispatcher.Shell = helperDispatcher(time.Second).Shell

	if _, err := dispatcher.Dispatch(
		context.Background(), configFor(t, SessionStart, "", ""), Payload{Event: "SessionStart"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	records := sink.snapshot()
	if len(records) != 1 || records[0].Slow {
		t.Fatalf("WithTelemetry did not wire the observer: %+v", records)
	}
}
