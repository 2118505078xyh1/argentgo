package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/automation"
)

func (a App) automationDir() string {
	return filepath.Join(a.storageDir(), "automations")
}

// runBoard prints a read-only view of background jobs and scheduled tasks. It
// never Starts, Stops or Put()s anything: the only writes are the registry and
// store constructors reading their existing JSON files.
func runBoard(app App, _ context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		boardUsage(app.IO.Out)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(app.IO.Err, "argent: unexpected argument", args[0])
		boardUsage(app.IO.Err)
		return 2
	}
	rows := 0
	if _, err := os.Stat(app.jobsDir()); err == nil {
		registry, _, err := app.openJobs()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		all, err := registry.List()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, "jobs:")
		for _, job := range all {
			command := job.Command
			if len(job.Args) > 0 {
				command += " " + strings.Join(job.Args, " ")
			}
			fmt.Fprintf(app.IO.Out, "  %s\t%s\t%s\n", job.ID, job.Status, command)
			rows++
		}
	} else {
		fmt.Fprintf(app.IO.Out, "no jobs directory at %s\n", app.jobsDir())
	}

	dir := app.automationDir()
	if _, err := os.Stat(filepath.Join(dir, "automations.json")); err == nil {
		store, err := automation.OpenStore(dir)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, "automations:")
		for _, spec := range store.List() {
			state := "disabled"
			if spec.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(app.IO.Out, "  %s\t%s\t%s\t%s\n", spec.ID, state, spec.Schedule.String(), spec.Name)
			rows++
		}
	} else {
		fmt.Fprintf(app.IO.Out, "no automations store at %s\n", dir)
	}
	if rows == 0 {
		fmt.Fprintln(app.IO.Out, "board is empty")
	}
	return 0
}

func boardUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent board

Prints a read-only board of background jobs and scheduled automations.
`)
}
