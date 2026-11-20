package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestClassifyTurnEndThreeReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		canceled bool
		want     TurnEndReason
	}{
		{"complete", nil, false, TurnEndComplete},
		{"failure", errors.New("provider exploded"), false, TurnEndFailure},
		{"interrupted by error", context.Canceled, false, TurnEndInterrupted},
		{"interrupted by flag", nil, true, TurnEndInterrupted},
		{"interrupted by both", context.Canceled, true, TurnEndInterrupted},
		{"flag dominates error", errors.New("boom"), true, TurnEndInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, tc.canceled); got != tc.want {
				t.Fatalf("ClassifyTurnEnd(%v, %v) = %q, want %q", tc.err, tc.canceled, got, tc.want)
			}
		})
	}
}

func TestClassifyTurnEndWrappedCanceled(t *testing.T) {
	base := fmt.Errorf("agent loop: %w", context.Canceled)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"one wrap", base},
		{"two wraps", fmt.Errorf("transport: %w", base)},
		{"joined", errors.Join(errors.New("partial"), context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, false); got != TurnEndInterrupted {
				t.Fatalf("ClassifyTurnEnd(%v, false) = %q, want interrupted", tc.err, got)
			}
		})
	}
}

func TestClassifyTurnEndNilError(t *testing.T) {
	if got := ClassifyTurnEnd(nil, false); got != TurnEndComplete {
		t.Fatalf("ClassifyTurnEnd(nil, false) = %q, want complete", got)
	}
}

func TestClassifyTurnEndDoesNotMisjudge(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain error named like cancellation", errors.New("context canceled")},
		{"plain error named like operation canceled", errors.New("operation was canceled")},
		{"deadline", context.DeadlineExceeded},
		{"wrapped deadline", fmt.Errorf("provider: %w", context.DeadlineExceeded)},
		{"deadline wording only", errors.New("context deadline exceeded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, false); got != TurnEndFailure {
				t.Fatalf("ClassifyTurnEnd(%v, false) = %q, want failure", tc.err, got)
			}
		})
	}
}

// outcomeRunner returns a fixed error immediately, so an ACP prompt reaches
// its terminal result without gating or real cancellation timing.
type outcomeRunner struct{ err error }

func (r outcomeRunner) Prompt(context.Context, string, func(string)) (string, error) {
	return "partial", r.err
}
func (r outcomeRunner) Abort() {}

// TestACPTerminalResultCarriesTurnEndReason verifies the additive field is on
// the two result-shaped terminal outcomes, while the original stopReason is
// untouched. The failure path stays a -32603 error (its code already tells it
// apart), so no existing wire shape changes.
func TestACPTerminalResultCarriesTurnEndReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStop   string
		wantReason TurnEndReason
	}{
		{"complete", nil, "end_turn", TurnEndComplete},
		{"interrupted", fmt.Errorf("wrapped: %w", context.Canceled), "cancelled", TurnEndInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- RunACP(context.Background(), inR, outW, Config{Runner: outcomeRunner{err: tc.err}})
			}()
			defer inW.Close()

			enc := json.NewEncoder(inW)
			dec := json.NewDecoder(outR)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
			readJSON(t, dec)

			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
				"params": map[string]any{"prompt": "hi"},
			})
			var result map[string]any
			for {
				msg := readJSON(t, dec)
				if msg["id"] == float64(2) {
					result = msg
					break
				}
			}
			body, _ := result["result"].(map[string]any)
			if body["stopReason"] != tc.wantStop {
				t.Fatalf("stopReason = %v, want %q (result %v)", body["stopReason"], tc.wantStop, result)
			}
			if body["turnEndReason"] != string(tc.wantReason) {
				t.Fatalf("turnEndReason = %v, want %q (result %v)", body["turnEndReason"], tc.wantReason, result)
			}
			_ = inW.Close()
			waitFor(t, done)
		})
	}
}
