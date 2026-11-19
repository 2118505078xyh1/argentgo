package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// runAudit implements `audit [--tail n] [--json]`: the newest audit entries.
func runAudit(app App, _ context.Context, args []string) int {
	tail := 20
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h" || args[i] == "--help":
			auditUsage(app.IO.Out)
			return 0
		case args[i] == "--json":
			asJSON = true
		case args[i] == "--tail":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --tail needs a value")
				return 2
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				fmt.Fprintf(app.IO.Err, "argent: --tail needs a non-negative integer, got %q\n", args[i])
				return 2
			}
			tail = n
		case strings.HasPrefix(args[i], "--tail="):
			n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--tail="))
			if err != nil || n < 0 {
				fmt.Fprintf(app.IO.Err, "argent: --tail needs a non-negative integer, got %q\n", args[i])
				return 2
			}
			tail = n
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", args[i])
			auditUsage(app.IO.Err)
			return 2
		}
	}

	dir := filepath.Join(app.storageDir(), "audit")
	path := latestAuditFile(dir)
	if path == "" {
		fmt.Fprintf(app.IO.Out, "no audit log under %s\n", dir)
		return 0
	}
	entries, err := security.ReadAudit(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if tail < len(entries) {
		entries = entries[len(entries)-tail:]
	}
	if asJSON {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(app.IO.Out, "%s\t%-8s\t%s\t%s\n", entry.TS, entry.Decision, entry.Tool, entry.Reason)
	}
	return 0
}

// latestAuditFile prefers today's file, then the newest daily file present.
func latestAuditFile(dir string) string {
	today := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	if info, err := os.Stat(today); err == nil && !info.IsDir() {
		return today
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		return ""
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1])
}

func auditUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent audit [--tail <n>] [--json]

Prints the newest audit entries from <storage>/audit (default: last 20).
Arguments and reasons are already redacted when written.
`)
}
