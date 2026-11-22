package cli

import (
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// sessionsEditContext 实现 `sessions edit-context <session-id> <target-record-id> [--hide | --text <text>]`。
// 它只追加一条 context_edit 记录，绝不改写目标记录或任何既有字节。
func sessionsEditContext(app App, id, target string, args []string) int {
	text := ""
	hide := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--hide":
			hide = true
		case "--text":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argentgo: --text needs a value")
				return 2
			}
			i++
			text = args[i]
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected edit-context argument %q\n", args[i])
			return 2
		}
	}
	if hide && text != "" {
		fmt.Fprintln(app.IO.Err, "argentgo: --hide and --text are mutually exclusive")
		return 2
	}
	if !hide && strings.TrimSpace(text) == "" {
		fmt.Fprintln(app.IO.Err, "argentgo: edit-context needs --hide or a non-empty --text")
		return 2
	}
	store, err := session.Open(app.sessionDir())
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if err := sess.AppendContextEdit(session.ContextEdit{TargetID: target, Text: text}); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if hide {
		fmt.Fprintf(app.IO.Out, "hid record %s in session %s\n", target, id)
	} else {
		fmt.Fprintf(app.IO.Out, "replace-edit recorded for %s in session %s\n", target, id)
	}
	return 0
}
