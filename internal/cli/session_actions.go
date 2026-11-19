package cli

import (
	"fmt"
	"os"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// applySessionFlags performs the local part of --undo/--retry/--title before a
// runner is built. It returns (exit code, handled); when handled is false the
// normal prompt path continues, with opts.Session possibly pointing at a fork.
func (a App) applySessionFlags(opts *Options) (int, bool) {
	if !opts.Undo && !opts.Retry && opts.Title == "" {
		return 0, false
	}
	fail := func(format string, args ...any) (int, bool) {
		fmt.Fprintf(a.IO.Err, "argent: "+format+"\n", args...)
		return 2, true
	}
	if opts.Undo && opts.Retry {
		return fail("--undo and --retry cannot be combined")
	}

	id := opts.Session
	if id == "" && opts.Resume {
		store, err := session.Open(a.sessionDir())
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		metas, err := store.List()
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		if len(metas) > 0 {
			id = metas[0].ID
		}
	}
	// A title alone on a fresh session is applied by the app when it creates
	// the session; this package cannot know the session id yet.
	if id == "" {
		if opts.Title != "" {
			return 0, false
		}
		return fail("session flags require --session <id> or --resume")
	}
	store, err := session.Open(a.sessionDir())
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1, true
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1, true
	}

	if opts.Undo {
		checkpoint, record, err := sess.LatestCheckpoint()
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		workspace := opts.Cwd
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		restored, err := session.UndoCheckpoint(workspace, checkpoint)
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		fmt.Fprintf(a.IO.Out, "restored %d file(s) from checkpoint %s in %s\n", len(restored), record.ID, sess.ID())
		return 0, true
	}

	if opts.Retry {
		fork, err := sess.RetryFromCheckpoint("")
		if err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		fmt.Fprintf(a.IO.Out, "forked session %s from %s\n", fork.ID(), sess.ID())
		opts.Session = fork.ID()
		opts.Resume = false
	}
	if opts.Title != "" {
		if err := ApplyTitleToSession(sess, opts.Title); err != nil {
			fmt.Fprintln(a.IO.Err, "error:", err)
			return 1, true
		}
		fmt.Fprintf(a.IO.Out, "renamed session %s\n", sess.ID())
	}
	return 0, false
}

// ApplyTitleToSession records an explicit title override on an open session.
// It is exported so the integrator can honour --title on a session it just
// created (see the app wiring hook in the session-agent report).
func ApplyTitleToSession(sess *session.Session, title string) error {
	if sess == nil {
		return fmt.Errorf("cli: nil session")
	}
	return sess.SetTitle(title)
}
