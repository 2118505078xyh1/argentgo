package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tui"
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

		// Feed real agent events to the TUI's tool cards. The TUI host calls
		// Execute directly (no cli.Runner), so it owns a private hub and attaches
		// its emitter to each run context below. Stop closes the bridge on exit.
		events := NewTUIEventPipeline(ctx)
		defer events.Stop()

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
			sess, err := openOrCreateSession(store, opts, text)
			if err != nil {
				return nil, err
			}
			current = sess
			return sess, nil
		}

		// TurnAction hooks back the /undo, /retry and /name commands (and the
		// Alt+U / Alt+R keys) with real session operations. Without them the TUI
		// advertised those commands but only printed "host has not wired it".
		// currentSession returns the live session, or an error before the first
		// prompt creates one; the hooks share sessionFor's mutex so a concurrent
		// prompt cannot race with checkpoint/fork/title writes.
		currentSession := func() (*session.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			if current == nil {
				return nil, fmt.Errorf("no active session yet")
			}
			return current, nil
		}
		undoTurn := func(context.Context, string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			checkpoint, _, err := sess.LatestCheckpoint()
			if err != nil {
				return err
			}
			_, err = session.UndoCheckpoint(res.Root, checkpoint)
			return err
		}
		retryTurn := func(_ context.Context, arg string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			fork, err := session.Retry(store.Dir(), sess.ID(), arg)
			if err != nil {
				return err
			}
			mu.Lock()
			current = fork // subsequent prompts continue on the forked branch
			mu.Unlock()
			return nil
		}
		renameTurn := func(_ context.Context, title string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			return sess.SetTitle(title)
		}

		frontendOpts := tui.Options{
			Root:          res.Root,
			Model:         res.Model,
			Locale:        res.Locale,
			Sessions:      sessionTitles(store),
			ContextWindow: res.Cfg.ContextWindow,
			Events:        events.Events,
			Undo:          undoTurn,
			Retry:         retryTurn,
			Rename:        renameTurn,
			Prompt: func(turnCtx context.Context, text string, onText func(string)) error {
				turnCtx = withEventEmit(turnCtx, events.Emit)
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
