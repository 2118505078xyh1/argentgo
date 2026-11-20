package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestRunPromptCancellationIsSilent verifies a Ctrl-C/abort is not reported as
// an unrecognized provider error: no stderr, no actionable hint, exit 0.
func TestRunPromptCancellationIsSilent(t *testing.T) {
	cases := []struct {
		name   string
		runner *stubRunner
	}{
		{"direct", &stubRunner{reply: "partial", fail: context.Canceled}},
		{"wrapped", &stubRunner{reply: "partial", fail: fmt.Errorf("run interrupted: %w", context.Canceled)}},
		{"with streamed text", &stubRunner{chunks: []string{"par", "tial"}, fail: context.Canceled}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := func(Options) (Runner, error) { return tc.runner, nil }
			ioState, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"hi"})
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr = %q", code, errOut.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q, want empty on cancellation", errOut.String())
			}
			if tc.runner.chunks != nil {
				if got := out.String(); got != "partial" {
					t.Fatalf("stdout = %q, want the streamed text to survive", got)
				}
			} else if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", out.String())
			}
		})
	}
}

// TestRunPromptDeadlineStillFails keeps a timeout on the failure path: only a
// user abort is silent, a deadline must still report an error and exit 1.
func TestRunPromptDeadlineStillFails(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{fail: context.DeadlineExceeded}, nil
	}
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"hi"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "error: "+context.DeadlineExceeded.Error()) {
		t.Fatalf("stderr = %q, want the deadline error", errOut.String())
	}
}

// TestRunPromptCancellationJSONModeKeepsEventShape keeps machine readers on the
// same result event: cancellation carries an empty error and a stopReason,
// never "context canceled" as a failed request.
func TestRunPromptCancellationJSONModeKeepsEventShape(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{fail: context.Canceled}, nil
	}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"--json-lines", "hi"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr = %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &event); err != nil {
		t.Fatalf("decode result event %q: %v", out.String(), err)
	}
	if event["type"] != "result" || event["error"] != "" || event["stopReason"] != "cancelled" {
		t.Fatalf("result event = %v", event)
	}
}
