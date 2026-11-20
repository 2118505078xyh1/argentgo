package workflow

import (
	"context"
	"strings"
	"testing"
)

// TestRunStopsOnCancelledContext checks the executor's obligation to observe
// the run context even when the injected seams ignore it: once the context is
// done, no further prompt or tool call may start and the step must not be
// reported as a success just because a seam returned a value.
func TestRunStopsOnCancelledContext(t *testing.T) {
	cases := []struct {
		name            string
		definition      string
		cancelOnCall    int // -1 cancels before Run; otherwise cancels on that call
		wantCalls       int
		wantStatus      []string
		wantAttempts    []int
		wantErrContains []string
	}{
		{
			name:            "already cancelled: no seam is called",
			definition:      `{"steps":[{"name":"a","prompt":"a"},{"name":"b","prompt":"b"}]}`,
			cancelOnCall:    -1,
			wantCalls:       0,
			wantStatus:      []string{StatusFailed, StatusSkipped},
			wantAttempts:    []int{0, 0},
			wantErrContains: []string{"context canceled", "an earlier step stopped the workflow"},
		},
		{
			name:            "cancelled during first attempt: seam ignores ctx",
			definition:      `{"steps":[{"name":"a","prompt":"a"},{"name":"b","prompt":"b"}]}`,
			cancelOnCall:    1,
			wantCalls:       1,
			wantStatus:      []string{StatusFailed, StatusSkipped},
			wantAttempts:    []int{1, 0},
			wantErrContains: []string{"context canceled", "an earlier step stopped the workflow"},
		},
		{
			name: "continue policy still avoids further calls",
			definition: `{"steps":[
				{"name":"a","prompt":"a","on_failure":"continue"},
				{"name":"b","prompt":"b","on_failure":"continue"}
			]}`,
			cancelOnCall:    -1,
			wantCalls:       0,
			wantStatus:      []string{StatusFailed, StatusFailed},
			wantAttempts:    []int{0, 0},
			wantErrContains: []string{"context canceled", "context canceled"},
		},
		{
			name:            "tool steps honour cancellation too",
			definition:      `{"steps":[{"name":"a","tool":"write","args":"x"},{"name":"b","tool":"write","args":"y"}]}`,
			cancelOnCall:    -1,
			wantCalls:       0,
			wantStatus:      []string{StatusFailed, StatusSkipped},
			wantAttempts:    []int{0, 0},
			wantErrContains: []string{"context canceled", "an earlier step stopped the workflow"},
		},
		{
			name:            "retries do not restart a cancelled step",
			definition:      `{"steps":[{"name":"a","prompt":"a","retries":3}]}`,
			cancelOnCall:    1,
			wantCalls:       1,
			wantStatus:      []string{StatusFailed},
			wantAttempts:    []int{1},
			wantErrContains: []string{"context canceled"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls int
			call := func() {
				calls++
				if tc.cancelOnCall == calls {
					cancel()
				}
			}
			// Both seams deliberately ignore their context and return a value,
			// so only the executor itself can stop the run.
			exec := &Executor{
				Prompter: prompterFunc(func(context.Context, string) (string, error) {
					call()
					return "done", nil
				}),
				Tools: toolsFunc(func(context.Context, string, string) (string, error) {
					call()
					return "done", nil
				}),
			}
			if tc.cancelOnCall < 0 {
				cancel()
			}

			run, err := exec.Run(ctx, mustDefinition(t, tc.definition), "")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if run.Status != StatusFailed {
				t.Fatalf("status = %q, want %q", run.Status, StatusFailed)
			}
			if calls != tc.wantCalls {
				t.Fatalf("seam calls = %d, want %d", calls, tc.wantCalls)
			}
			if len(run.Steps) != len(tc.wantStatus) {
				t.Fatalf("steps = %d, want %d", len(run.Steps), len(tc.wantStatus))
			}
			for i, want := range tc.wantStatus {
				if run.Steps[i].Status != want {
					t.Errorf("step %d status = %q, want %q", i, run.Steps[i].Status, want)
				}
			}
			for i, want := range tc.wantAttempts {
				if run.Steps[i].Attempts != want {
					t.Errorf("step %d attempts = %d, want %d", i, run.Steps[i].Attempts, want)
				}
			}
			for i, want := range tc.wantErrContains {
				if !strings.Contains(run.Steps[i].Error, want) {
					t.Errorf("step %d error = %q, want it to contain %q", i, run.Steps[i].Error, want)
				}
			}
		})
	}
}
