package cli

import (
	"fmt"
	"os"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// applySessionFlags 在构建 runner 之前执行 --undo/--retry/--title 的本地部分。
// 它返回（退出码, 是否已处理）；当 handled 为 false 时，正常的 prompt 路径继续，
// 此时 opts.Session 可能指向一个分叉。
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
	// 新会话上单独的标题由 app 在创建会话时应用；本包此时还不知道会话 id。
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

// ApplyTitleToSession 在一个已打开的会话上记录显式的标题覆盖。
// 它被导出，使集成方能在刚创建的会话上兑现 --title
// （见 session-agent 报告中的 app 接线钩子）。
func ApplyTitleToSession(sess *session.Session, title string) error {
	if sess == nil {
		return fmt.Errorf("cli: nil session")
	}
	return sess.SetTitle(title)
}
