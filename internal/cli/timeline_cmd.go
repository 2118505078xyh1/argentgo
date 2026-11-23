package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/2118505078xyh1/argentgo/internal/tui/timeline"
)

// runTimeline 实现 `argentgo timeline info|replay|hash <file>`：检查、回放或
// 计算 TUI 时间线录制的最终帧哈希（用于跨机器一致性对比）。
func runTimeline(app App, _ context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		timelineUsage(app.IO.Out)
		return 0
	}
	if len(args) != 2 {
		fmt.Fprintln(app.IO.Err, "argentgo: timeline needs a subcommand and a file")
		timelineUsage(app.IO.Err)
		return 2
	}
	sub, path := args[0], args[1]
	p, err := timeline.Load(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	switch sub {
	case "info":
		h := p.Header()
		fmt.Fprintf(app.IO.Out, "version=%d cols=%d rows=%d encoding=%s events=%d\n",
			h.Version, h.Cols, h.Rows, h.Encoding, len(p.Events()))
		return 0
	case "replay":
		if err := p.Replay(app.IO.Out, false, func(cols, rows int) {
			fmt.Fprintf(app.IO.Err, "resize: %dx%d\n", cols, rows)
		}); err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		return 0
	case "hash":
		snap, err := p.Snapshot()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		sum := sha256.Sum256(snap)
		fmt.Fprintln(app.IO.Out, hex.EncodeToString(sum[:]))
		return 0
	default:
		fmt.Fprintf(app.IO.Err, "argentgo: unknown timeline subcommand %q\n", sub)
		timelineUsage(app.IO.Err)
		return 2
	}
}

func timelineUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo timeline info|replay|hash <file>

Inspects a TUI timeline recording. "replay" writes the recorded frames to stdout;
"hash" prints the SHA-256 of the final frame for cross-machine comparison.
`)
}
