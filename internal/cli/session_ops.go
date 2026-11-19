package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsUndo restores the files named by the newest checkpoint manifest.
func sessionsUndo(app App, id string, args []string) int {
	workspace, err := undoWorkspace(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	checkpoint, record, err := sess.LatestCheckpoint()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	restored, err := session.UndoCheckpoint(workspace, checkpoint)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "restored %d file(s) from checkpoint %s in %s\n", len(restored), record.ID, sess.ID())
	return 0
}

func undoWorkspace(args []string) (string, error) {
	root := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C" || args[i] == "--cwd":
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a directory", args[i])
			}
			i++
			root = args[i]
		case strings.HasPrefix(args[i], "--cwd="):
			root = strings.TrimPrefix(args[i], "--cwd=")
		default:
			return "", fmt.Errorf("unexpected undo argument %q", args[i])
		}
	}
	if root == "" {
		return os.Getwd()
	}
	return filepath.Abs(root)
}

// sessionsRetry forks a session at a checkpoint parent and prints the new id.
func sessionsRetry(app App, id string, args []string) int {
	checkpointID := ""
	if len(args) > 0 {
		checkpointID = args[0]
	}
	if len(args) > 1 {
		fmt.Fprintf(app.IO.Err, "argent: unexpected retry argument %q\n", args[1])
		return 2
	}
	fork, err := session.Retry(app.sessionDir(), id, checkpointID)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintln(app.IO.Out, fork.ID())
	return 0
}

// sessionsName stores an explicit title override.
func sessionsName(app App, id, title string) int {
	sess, err := session.OpenSession(app.sessionDir(), id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if err := ApplyTitleToSession(sess, title); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "renamed session %s\n", sess.ID())
	return 0
}
