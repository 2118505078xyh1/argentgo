package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/session"
	"github.com/2118505078xyh1/argent-go/internal/tui"
)

// TUI returns the frontend the command layer runs for `argent tui` / --tui.
//
// It reuses the agent wiring of Execute: a prompt streams through the same
// registry, policy and session store, and approvals are answered by the TUI
// modal. The live model is captured from tui.Options.OnReady and every
// security.Approver call blocks on Model.Approve until the operator answers or
// the turn's context ends.
func TUI(stdio cli.IO) cli.TUIFunc {
	return func(ctx context.Context, opts cli.Options) error {
		res, err := Resolve(stdio, opts)
		if err != nil {
			return err
		}
		store, err := session.Open(filepath.Join(res.Storage, "sessions"))
		if err != nil {
			return fmt.Errorf("open sessions: %w", err)
		}

		approver := NewApprover()
		res.Approve = approver

		var (
			mu      sync.Mutex
			current *session.Session
		)
		sessionFor := func(text string) (*session.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			if current != nil {
				return current, nil
			}
			title := firstLine(text, 60)
			if title == "" {
				title = "session"
			}
			sess, err := store.NewSession(title)
			if err != nil {
				return nil, fmt.Errorf("new session: %w", err)
			}
			current = sess
			return sess, nil
		}

		frontendOpts := tui.Options{
			Root:     res.Root,
			Model:    res.Model,
			Locale:   res.Locale,
			Sessions: sessionTitles(store),
			Prompt: func(turnCtx context.Context, text string, onText func(string)) error {
				sess, err := sessionFor(text)
				if err != nil {
					return err
				}
				_, err = Execute(turnCtx, stdio, res, sess, text, onText)
				return err
			},
			OnReady: func(model *tui.Model) { approver.attach(model) },
		}
		// A test (or a pipe) must not put the terminal into raw mode.
		if !realStreams(stdio) {
			frontendOpts.IO = &tui.IO{In: stdio.In, Out: stdio.Out}
		}
		return tui.Run(ctx, frontendOpts)
	}
}

// Approver satisfies security.Approver by delegating to the TUI modal.
type Approver struct {
	mu    sync.Mutex
	model *tui.Model
	ready chan struct{}
	once  sync.Once
}

// NewApprover returns an Approver whose modal is not attached yet.
func NewApprover() *Approver {
	return &Approver{ready: make(chan struct{})}
}

// attach records the live model; the first call opens the ready latch.
func (a *Approver) attach(model *tui.Model) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.once.Do(func() { close(a.ready) })
}

// Ask blocks until the modal resolves or ctx ends.
func (a *Approver) Ask(ctx context.Context, name, args string) (bool, error) {
	select {
	case <-a.ready:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	a.mu.Lock()
	model := a.model
	a.mu.Unlock()
	if model == nil {
		return false, nil
	}
	return model.Approve(ctx, name, args)
}

// sessionTitles renders the picker entries, newest first.
func sessionTitles(store *session.Store) []string {
	metas, err := store.List()
	if err != nil {
		return nil
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	titles := make([]string, 0, len(metas))
	for _, meta := range metas {
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		titles = append(titles, fmt.Sprintf("%s — %s", title, meta.ID))
	}
	return titles
}

// realStreams reports whether stdio is the process terminal, which is the only
// case where the TUI should take over raw mode.
func realStreams(stdio cli.IO) bool {
	return stdio.In == os.Stdin && stdio.Out == os.Stdout
}
