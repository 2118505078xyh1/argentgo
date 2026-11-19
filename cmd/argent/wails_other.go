//go:build !windows

package main

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/app"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/gui"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

func wailsRun(_ context.Context, stdio cli.IO, _ gui.Runner, _ app.Resolved, _ *session.Store, _ gui.TurnHistory, _, _ string) int {
	fmt.Fprintln(stdio.Err, "argent gui: Wails desktop shell is Windows-only; use --web")
	return 1
}
