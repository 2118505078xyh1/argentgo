package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuardConvertsPanicsIntoCallIDErrors(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() error
		wantErr      bool
		wantPanic    bool
		wantSentinel bool
	}{
		{name: "normal completion", fn: func() error { return nil }},
		{name: "ordinary error", fn: func() error { return errors.New("boom") }, wantErr: true},
		{name: "panic", fn: func() error { panic("kaboom") }, wantErr: true, wantPanic: true, wantSentinel: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := guard("call-7", test.fn)
			if (err != nil) != test.wantErr {
				t.Fatalf("guard error = %v, wantErr %t", err, test.wantErr)
			}
			if !test.wantPanic {
				return
			}
			var panicked *PanicError
			if !errors.As(err, &panicked) {
				t.Fatalf("error %v is not a PanicError", err)
			}
			if panicked.CallID != "call-7" {
				t.Fatalf("call ID = %q, want %q", panicked.CallID, "call-7")
			}
			if panicked.Value != "kaboom" {
				t.Fatalf("panic value = %v, want kaboom", panicked.Value)
			}
			if test.wantSentinel && !errors.Is(err, ErrCallbackPanic) {
				t.Fatalf("error %v does not wrap ErrCallbackPanic", err)
			}
			if !strings.Contains(err.Error(), "call-7") {
				t.Fatalf("error text lost the call ID: %q", err.Error())
			}
		})
	}
}

func TestDispatchSurvivesAPanickingHook(t *testing.T) {
	t.Setenv("ARGENT_HOOK_HELPER", "echo")
	config, err := ParseConfig([]byte(`{"hooks":{"PostToolUse":[{"hooks":[
		{"command":"panic-hook"},
		{"command":"echo-hook"}
	]}]}}`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	dispatcher := helperDispatcher(5 * time.Second)
	baseShell := dispatcher.Shell
	dispatcher.Shell = func(command string) (string, []string) {
		if command == "panic-hook" {
			panic("kaboom")
		}
		return baseShell(command)
	}

	outcomes, err := dispatcher.Run(context.Background(), config, Payload{
		Event: "PostToolUse", CallID: "call-7",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2 (the run must continue)", len(outcomes))
	}

	var panicked *PanicError
	if !errors.As(outcomes[0].Err, &panicked) {
		t.Fatalf("first outcome error = %v, want PanicError", outcomes[0].Err)
	}
	if panicked.CallID != "call-7" {
		t.Fatalf("panic call ID = %q, want %q", panicked.CallID, "call-7")
	}
	if outcomes[1].Err != nil || outcomes[1].ExitCode != 0 {
		t.Fatalf("second hook did not run after the panic: %+v", outcomes[1])
	}

	result := Summarize(outcomes)
	if result.Blocked {
		t.Fatal("a panicking hook must not silently block the call")
	}
	if !strings.Contains(result.Message, "call-7") || !strings.Contains(result.Message, "panicked") {
		t.Fatalf("summary lost the panic error: %q", result.Message)
	}
}
