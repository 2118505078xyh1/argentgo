//go:build windows

package main

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/app"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/gui"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// wailsRun hosts the existing HTTP+SSE UI bridge inside a WebView2 window.
// The bridge, the engine runner and the frontend are unchanged; Wails only
// replaces Edge --app as the window shell.
func wailsRun(ctx context.Context, stdio cli.IO, runner gui.Runner, resolved app.Resolved, store *session.Store, history gui.TurnHistory, provider, locale string) int {
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	defer stopBridge()

	ready := make(chan string, 1)
	opts := gui.Options{
		Dir:        resolved.Root,
		Version:    cli.Version,
		Model:      resolved.Model,
		Provider:   providerName(resolved, provider),
		Protocol:   resolved.Protocol,
		Locale:     locale,
		StorageDir: resolved.Storage,
		Sessions:   &guiSessions{dir: store.Dir(), cwd: resolved.Root},
		History:    history,
		NewRunner:  func() gui.Runner { return runner },
		OnReady:    func(url string) { ready <- url },
	}
	go func() {
		if err := gui.Run(bridgeCtx, opts); err != nil && ctx.Err() == nil {
			fmt.Fprintln(stdio.Err, "error:", err)
		}
	}()

	var url string
	select {
	case url = <-ready:
	case <-ctx.Done():
		return 0
	}

	desktop := application.New(application.Options{
		Name:        "argentgo",
		Description: "ArgentGo coding agent",
	})
	desktop.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "argentgo",
		Width:            1180,
		Height:           760,
		MinWidth:         800,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(23, 23, 22),
		URL:              url,
	})

	if err := desktop.Run(); err != nil {
		fmt.Fprintln(stdio.Err, "error:", err)
		return 1
	}
	return 0
}
