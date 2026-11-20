package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// execute is the single test seam for Prompt. Production always uses Execute;
// tests swap it to observe cancellation without running the real agent loop.
var execute = Execute

// Factory returns the runner factory the CLI expects.
func Factory(stdio cli.IO) cli.RunnerFactory {
	return func(opts cli.Options) (cli.Runner, error) {
		resolved, err := Resolve(stdio, opts)
		if err != nil {
			return nil, err
		}
		store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
		if err != nil {
			return nil, fmt.Errorf("open sessions: %w", err)
		}
		return &runner{stdio: stdio, res: resolved, store: store}, nil
	}
}

// ActiveSessionID reports the session a runner is appending to, when it
// exposes one. Callers holding only a cli.Runner can use this to show or log
// the resumed session id.
func ActiveSessionID(r cli.Runner) string {
	if typed, ok := r.(interface{ SessionID() string }); ok {
		return typed.SessionID()
	}
	return ""
}

type runner struct {
	stdio cli.IO
	res   Resolved
	store *session.Store

	// All of busy, cancel and current are protected by mu. In particular the
	// single-flight slot (busy) and its cancel func are installed and cleared
	// in the same critical section, so Abort can never observe "busy but no
	// cancel registered" and lose the abort.
	mu      sync.Mutex
	busy    bool
	cancel  context.CancelFunc
	current *session.Session
}

// promptClaimHook is a test seam called by Prompt after it has atomically
// claimed the single-flight slot and registered its cancel func. Production
// leaves it nil. Tests use it to run Abort exactly at prompt start.
var promptClaimHook func(*runner)

// Prompt satisfies cli.Runner. At most one prompt may be in flight on a runner.
func (r *runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Claim the single-flight slot and register the cancel func under one
	// critical section. Abort uses the same mu, so it either sees no prompt at
	// all (no-op) or sees a fully registered cancel func and can cancel it.
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		cancel() // do not leak the derived context when the prompt is refused
		return "", errors.New("prompt already running")
	}
	r.busy = true
	r.cancel = cancel
	r.mu.Unlock()

	// Test seam: by this point busy and cancel are both installed. Production
	// leaves the hook nil.
	if promptClaimHook != nil {
		promptClaimHook(r)
	}

	// Clear the cancel slot and release the slot atomically under the same mu,
	// mutually exclusive with Prompt and Abort.
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.busy = false
		r.mu.Unlock()
		cancel()
	}()

	sess, err := r.ensureSession(text)
	if err != nil {
		return "", err
	}
	result, err := execute(ctx, r.stdio, r.res, sess, text, onText)
	return result.Text, err
}

// Abort satisfies cli.Runner.
func (r *runner) Abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// SessionID reports the session the runner is currently appending to. It is
// empty before the first prompt.
func (r *runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return ""
	}
	return r.current.ID()
}

func (r *runner) ensureSession(firstPrompt string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	sess, err := openOrCreateSession(r.store, r.res.Options, firstPrompt)
	if err != nil {
		return nil, err
	}
	r.current = sess
	return sess, nil
}
