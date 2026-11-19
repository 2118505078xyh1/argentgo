// Command argent is the terminal entry point.
package main

import (
	"os"
	"path/filepath"

	"github.com/2118505078xyh1/argent-go/internal/app"
	"github.com/2118505078xyh1/argent-go/internal/cli"
)

func main() {
	stdio := cli.OSIO()
	storage := app.DefaultStorage(stdio.Env)
	code := cli.RunWith(cli.App{
		IO:         stdio,
		Version:    cli.Version,
		NewRunner:  app.Factory(stdio),
		Sessions:   app.Sessions(storage),
		Eval:       app.NewEvaler(stdio),
		ConfigPath: filepath.Join(storage, "config.json"),
		StorageDir: storage,
	}, os.Args[1:])
	os.Exit(code)
}
