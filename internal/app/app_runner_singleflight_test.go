package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// singleflightCtl drives the Execute seam so a prompt can be held in flight,
// released normally, or cancelled via its context without a real model call.
type singleflightCtl struct {
	calls   atomic.Int32
	entered chan context.Context
	release chan struct{}
}

// resume lets exactly one in-flight Execute finish with a successful result.
func (c *singleflightCtl) resume() { c.release <- struct{}{} }

type promptResult struct {
	text string
	err  error
}

func awaitEntered(t *testing.T, ctl *singleflightCtl) context.Context {
	t.Helper()
	select {
	case ctx := <-ctl.entered:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("Execute was not called")
		return nil
	}
}

func awaitResult(t *testing.T, ch <-chan promptResult) promptResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not finish")
		return promptResult{}
	}
}

func newSingleflightRunner(t *testing.T) (*runner, *singleflightCtl) {
	t.Helper()
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctl := &singleflightCtl{
		entered: make(chan context.Context, 4),
		release: make(chan struct{}, 4),
	}
	previous := execute
	execute = func(ctx context.Context, _ cli.IO, _ Resolved, _ *session.Session, _ string, _ func(string)) (agent.Result, error) {
		ctl.calls.Add(1)
		ctl.entered <- ctx
		select {
		case <-ctl.release:
			return agent.Result{Text: "done"}, nil
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	t.Cleanup(func() { execute = previous })
	return &runner{stdio: cli.IO{Env: env}, res: res, store: testStore(t, res)}, ctl
}

func promptAsync(r *runner, text string) <-chan promptResult {
	out := make(chan promptResult, 1)
	go func() {
		got, err := r.Prompt(context.Background(), text, nil)
		out <- promptResult{text: got, err: err}
	}()
	return out
}

func TestRunnerSingleFlight(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *runner, *singleflightCtl)
	}{
		{
			name: "second concurrent prompt is refused before Execute",
			run: func(t *testing.T, r *runner, ctl *singleflightCtl) {
				first := promptAsync(r, "first")
				awaitEntered(t, ctl) // first prompt is still in flight

				got, err := r.Prompt(context.Background(), "second", nil)
				if err == nil || err.Error() != "prompt already running" {
					t.Fatalf("second prompt err = %v, want %q", err, "prompt already running")
				}
				if got != "" {
					t.Errorf("second prompt text = %q, want empty", got)
				}
				if n := ctl.calls.Load(); n != 1 {
					t.Fatalf("Execute calls = %d, want 1 (second must not run)", n)
				}

				ctl.resume()
				if res := awaitResult(t, first); res.err != nil || res.text != "done" {
					t.Fatalf("first prompt = %+v, want done/nil", res)
				}
			},
		},
		{
			name: "a new prompt is allowed after the first finishes",
			run: func(t *testing.T, r *runner, ctl *singleflightCtl) {
				first := promptAsync(r, "first")
				awaitEntered(t, ctl)
				ctl.resume()
				if res := awaitResult(t, first); res.err != nil || res.text != "done" {
					t.Fatalf("first prompt = %+v, want done/nil", res)
				}

				second := promptAsync(r, "second")
				awaitEntered(t, ctl)
				ctl.resume()
				if res := awaitResult(t, second); res.err != nil || res.text != "done" {
					t.Fatalf("second prompt = %+v, want done/nil", res)
				}
				if n := ctl.calls.Load(); n != 2 {
					t.Fatalf("Execute calls = %d, want 2", n)
				}
			},
		},
		{
			name: "abort cancels only the in-flight prompt",
			run: func(t *testing.T, r *runner, ctl *singleflightCtl) {
				first := promptAsync(r, "first")
				ctx1 := awaitEntered(t, ctl)

				r.Abort()
				if res := awaitResult(t, first); !errors.Is(res.err, context.Canceled) {
					t.Fatalf("first prompt err = %v, want context.Canceled", res.err)
				}
				if ctx1.Err() != context.Canceled {
					t.Fatalf("first Execute ctx err = %v, want context.Canceled", ctx1.Err())
				}

				// The aborted run released the single-flight slot; a later
				// prompt must run with a fresh, uncancelled context.
				second := promptAsync(r, "second")
				ctx2 := awaitEntered(t, ctl)
				if ctx2.Err() != nil {
					t.Fatalf("second prompt ctx cancelled by earlier abort: %v", ctx2.Err())
				}
				ctl.resume()
				if res := awaitResult(t, second); res.err != nil || res.text != "done" {
					t.Fatalf("second prompt = %+v, want done/nil", res)
				}
				if n := ctl.calls.Load(); n != 2 {
					t.Fatalf("Execute calls = %d, want 2", n)
				}
			},
		},
		{
			name: "abort with no in-flight prompt is a no-op",
			run: func(t *testing.T, r *runner, ctl *singleflightCtl) {
				r.Abort()

				first := promptAsync(r, "first")
				ctx := awaitEntered(t, ctl)
				if ctx.Err() != nil {
					t.Fatalf("no-op abort cancelled the new prompt: %v", ctx.Err())
				}
				ctl.resume()
				if res := awaitResult(t, first); res.err != nil || res.text != "done" {
					t.Fatalf("prompt = %+v, want done/nil", res)
				}
				if n := ctl.calls.Load(); n != 1 {
					t.Fatalf("Execute calls = %d, want 1", n)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ctl := newSingleflightRunner(t)
			tc.run(t, r, ctl)
		})
	}
}
