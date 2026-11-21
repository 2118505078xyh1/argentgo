package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
)

// jobsDir 是该存储目录下 registry 及其日志所在的位置。
func (a App) jobsDir() string {
	return filepath.Join(a.storageDir(), "jobs")
}

// openJobs 拒绝凭空创建 registry：jobs 目录缺失意味着这里从未启动过任何作业，
// 调用方会将其报告为一行错误。
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
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			jobsUsage(app.IO.Out)
			return true
		}
	}
	return false
}

func runAttach(app App, _ context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, lines, err := parseTailArgs(args)
	if errors.Is(err, errHelp) {
		jobsUsage(app.IO.Out)
		return 0
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
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
		return reportLongOut(app, text+"\n")
	}
	return 0
}

func runLogs(app App, _ context.Context, args []string) int {
	if jobsHelp(app, args) {
		return 0
	}
	id, _, err := parseTailArgs(args)
	if errors.Is(err, errHelp) {
		jobsUsage(app.IO.Out)
		return 0
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
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
				var content strings.Builder
				printJob(&content, job)
				return reportLongOut(app, content.String())
			}
		}
		fmt.Fprintf(app.IO.Err, "error: jobs: no job named %q\n", id)
		return 1
	}
	if len(all) == 0 {
		fmt.Fprintln(app.IO.Out, "no background jobs")
		return 0
	}
	var content strings.Builder
	for _, job := range all {
		printJob(&content, job)
	}
	return reportLongOut(app, content.String())
}

// runJobs 是 `logs` 的可选别名 `jobs`。
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
	if errors.Is(err, errHelp) {
		jobsUsage(app.IO.Out)
		return 0
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
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
	if errors.Is(err, errHelp) {
		jobsUsage(app.IO.Out)
		return 0
	}
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
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
	// 先打印所请求的历史内容，然后只流式输出新追加的字节。
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

// parseTailArgs 接受 `<id>`、`[-n|--lines] N` 和 `--lines=N`。对于不跟踪
// 日志的命令，行数是可选的。
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

// parseTailArgs 遇到 -h 时返回 errHelp，使调用方可以把 usage 输出到
// stdout，而不把它当作错误。
var errHelp = fmt.Errorf("help requested")

func jobsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo attach <id> [-n N]
       argentgo logs [<id>]  (jobs is an alias)
       argentgo follow <id> [-n N]
       argentgo stop <id>

attach prints the tail of a job log, follow streams it until interrupted,
logs lists jobs and their log files, and stop kills a running job.
`)
}
