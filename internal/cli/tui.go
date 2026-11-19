package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// runTUI implements `argent tui [flags]`.
func runTUI(app App, ctx context.Context, args []string) int {
	opts, positional, err := parseArgs(args, app.env, app.IO.Err)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(app.IO.Err, "argent:", ue.msg)
			tuiUsage(app.IO.Err)
		}
		return 2
	}
	if opts.Help {
		tuiUsage(app.IO.Out)
		return 0
	}
	if len(positional) > 0 {
		fmt.Fprintln(app.IO.Err, "argent: tui takes flags only; the prompt is typed in the UI")
		tuiUsage(app.IO.Err)
		return 2
	}
	opts.TUI = true
	return app.runTUI(ctx, opts)
}

// runTUI hands resolved options to the injected full-screen frontend.
func (a App) runTUI(ctx context.Context, opts *Options) int {
	if a.TUI == nil {
		fmt.Fprintln(a.IO.Err, "error: TUI not configured")
		return 1
	}
	if err := a.TUI(ctx, *opts); err != nil {
		fmt.Fprintln(a.IO.Err, "error:", err)
		return 1
	}
	return 0
}

func tuiUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent tui [flags]

Runs the full-screen terminal UI. Flags are the same as one-shot mode
(--model, --provider, --profile, --sandbox-mode, --approval, --yolo, ...).
Ctrl-C aborts a turn, Ctrl-C again quits; /quit also exits.
`)
}
