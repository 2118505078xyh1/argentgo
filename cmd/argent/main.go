// Command argent 是终端入口点。
package main

import (
	"os"
	"path/filepath"

	"github.com/2118505078xyh1/argentgo/internal/app"
	"github.com/2118505078xyh1/argentgo/internal/cli"
)

func main() {
	stdio := cli.OSIO()
	args := os.Args[1:]
	// 只有前导 `gui` 子命令启动原生客户端。--gui 是
	// 普通 CLI 标志，由 cli.RunWith 处理（报告为未迁移）。
	if isGUIRequest(args) {
		os.Exit(runGUI(stdio, args))
	}
	storage := app.DefaultStorage(stdio.Env)
	code := cli.RunWith(cli.App{
		IO:                 stdio,
		Version:            cli.Version,
		NewRunner:          app.Factory(stdio),
		Sessions:           app.Sessions(storage),
		Eval:               app.NewEvaler(stdio),
		TUI:                app.TUI(stdio),
		Profiles:           func(opts cli.Options) ([]string, string, error) { return app.ListProfiles(stdio, opts) },
		TokenReport:        func(opts cli.Options) (string, error) { return app.TokenReport(stdio, opts) },
		ContextReport:      func(opts cli.Options) (string, error) { return app.ContextReport(stdio, opts) },
		ConfigInfoProvider: app.NewConfigInfoProvider(stdio),
		ConfigPath:         filepath.Join(storage, "config.json"),
		StorageDir:         storage,
	}, os.Args[1:])
	os.Exit(code)
}
