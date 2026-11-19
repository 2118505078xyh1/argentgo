package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/jobs"
)

// jobsDir is where the registry and its logs live for this storage directory.
func (a App) jobsDir() string {
	return filepath.Join(a.storageDir(), "jobs")
}

// openJobs refuses to invent a registry: a missing jobs directory means no job
// has ever been started here, which the caller reports as a one-line error.
func (a App) openJobs() (*jobs.Registry, string, error) {
	dir := a.jobsDir()
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, dir, fmt.Errorf("no jobs directory at %s: start a background job first", dir)
	}
	registry, err := jobs.New(dir)
	if err != nil {
		return nil, dir, err
	}
	return registry, dir, nil
}

func jobsHelp(app App, args []string) bool {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		jobsUsage(app.IO.Out)
		return true
	}
	return false
}

func runAttach(app App, _ context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, lines, err := parseTailArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		jobsUsage(app.IO.Err)
		return 2
	}
	registry, _, err := app.openJobs()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	text, err := registry.Tail(id, lines)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if text != "" {
		fmt.Fprintln(app.IO.Out, text)
	}
	return 0
}

func runLogs(app App, _ context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, _, err := parseTailArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		jobsUsage(app.IO.Err)
		return 2
	}
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
	if id != "" {
		for _, job := range all {
			if job.ID == id {
				printJob(app.IO.Out, job)
				return 0
			}
		}
		fmt.Fprintf(app.IO.Err, "error: jobs: no job named %q\n", id)
		return 1
	}
	if len(all) == 0 {
		fmt.Fprintln(app.IO.Out, "no background jobs")
		return 0
	}
	for _, job := range all {
		printJob(app.IO.Out, job)
	}
	return 0
}

// runJobs is the optional `jobs` alias for `logs`.
func runJobs(app App, ctx context.Context, args []string) int {
	return runLogs(app, ctx, args)
}

func printJob(out io.Writer, job jobs.Job) {
	command := job.Command
	if len(job.Args) > 0 {
		command += " " + strings.Join(job.Args, " ")
	}
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", job.ID, job.Status, command, job.LogPath)
}

func runStop(app App, _ context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, _, err := parseTailArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		jobsUsage(app.IO.Err)
		return 2
	}
	registry, _, err := app.openJobs()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	job, err := registry.Get(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if job.Terminal() {
		fmt.Fprintf(app.IO.Out, "job %s is already %s\n", id, job.Status)
		return 0
	}
	if err := registry.Stop(id); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "stopped job %s\n", id)
	return 0
}

const followPoll = 200 * time.Millisecond

func runFollow(app App, ctx context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, lines, err := parseTailArgs(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		jobsUsage(app.IO.Err)
		return 2
	}
	registry, _, err := app.openJobs()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	job, err := registry.Get(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// Print the requested history first, then stream only appended bytes.
	if text, tailErr := registry.Tail(id, lines); tailErr == nil && text != "" {
		fmt.Fprintln(app.IO.Out, text)
	}
	return followLog(ctx, app.IO.Out, app.IO.Err, job.LogPath)
}

func followLog(ctx context.Context, out, errOut io.Writer, path string) int {
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	defer func() { _ = file.Close() }()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	ticker := time.NewTicker(followPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			info, statErr := file.Stat()
			if statErr != nil {
				fmt.Fprintln(errOut, "error:", statErr)
				return 1
			}
			size := info.Size()
			if size < offset { // the log was rotated or truncated
				offset = 0
			}
			if size == offset {
				continue
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				fmt.Fprintln(errOut, "error:", err)
				return 1
			}
			data, err := io.ReadAll(io.LimitReader(file, size-offset))
			if err != nil {
				fmt.Fprintln(errOut, "error:", err)
				return 1
			}
			offset = size
			if len(data) > 0 {
				fmt.Fprint(out, string(data))
			}
		}
	}
}

// parseTailArgs accepts `<id>`, `[-n|--lines] N` and `--lines=N`. The line
// count is optional for commands that do not tail a log.
func parseTailArgs(args []string) (id string, lines int, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h" || args[i] == "--help":
			return "", 0, errHelp
		case args[i] == "-n" || args[i] == "--lines":
			if i+1 >= len(args) {
				return "", 0, fmt.Errorf("%s needs a value", args[i])
			}
			i++
			lines, err = strconv.Atoi(args[i])
			if err != nil || lines < 0 {
				return "", 0, fmt.Errorf("invalid line count %q", args[i])
			}
		case strings.HasPrefix(args[i], "--lines="):
			lines, err = strconv.Atoi(strings.TrimPrefix(args[i], "--lines="))
			if err != nil || lines < 0 {
				return "", 0, fmt.Errorf("invalid line count %q", args[i])
			}
		case strings.HasPrefix(args[i], "-"):
			return "", 0, fmt.Errorf("unexpected argument %q", args[i])
		default:
			if id == "" {
				id = args[i]
			} else {
				return "", 0, fmt.Errorf("unexpected argument %q", args[i])
			}
		}
	}
	return id, lines, nil
}

// errHelp is returned by parseTailArgs for -h so callers can route usage to
// stdout without treating it as an error.
var errHelp = fmt.Errorf("help requested")

func jobsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent attach <id> [-n N]
       argent logs [<id>]  (jobs is an alias)
       argent follow <id> [-n N]
       argent stop <id>

attach prints the tail of a job log, follow streams it until interrupted,
logs lists jobs and their log files, and stop kills a running job.
`)
}
