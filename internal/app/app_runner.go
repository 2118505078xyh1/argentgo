package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

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

	mu      sync.Mutex
	cancel  context.CancelFunc
	current *session.Session
}

// Prompt satisfies cli.Runner.
func (r *runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
		cancel()
	}()

	sess, err := r.ensureSession(text)
	if err != nil {
		return "", err
	}
	result, err := Execute(ctx, r.stdio, r.res, sess, text, onText)
	return result.Text, err
}

// Abort satisfies cli.Runner.
func (r *runner) Abort() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
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
