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

// runAudit implements `audit [--tail n] [--json] [--decision d] [--session id]
// [--tool name] [--since time]`. Without a filter flag it keeps the original
// "newest entries from the latest daily file" behavior unchanged.
func runAudit(app App, _ context.Context, args []string) int {
	tail := 20
	asJSON := false
	var filter AuditFilter
	hasFilter := false

	for i := 0; i < len(args); i++ {
		// String-valued filter flags, in both "--flag value" and "--flag=value"
		// spellings. Their presence switches runAudit to the cross-file search.
		var target *string
		var name string
		switch {
		case args[i] == "--decision" || strings.HasPrefix(args[i], "--decision="):
			name, target = "--decision", &filter.Decision
		case args[i] == "--session" || strings.HasPrefix(args[i], "--session="):
			name, target = "--session", &filter.Session
		case args[i] == "--tool" || strings.HasPrefix(args[i], "--tool="):
			name, target = "--tool", &filter.Tool
		case args[i] == "--since" || strings.HasPrefix(args[i], "--since="):
			name, target = "--since", &filter.Since
		}
		if target != nil {
			value, next, errMsg := takeAuditStringValue(args, i, name)
			if errMsg != "" {
				fmt.Fprintln(app.IO.Err, errMsg)
				return 2
			}
			*target = value
			hasFilter = true
			i = next
			continue
		}

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
	if hasFilter {
		return runAuditFiltered(app, dir, filter, tail, asJSON)
	}

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
	return printAuditEntries(app.IO.Out, app.IO.Err, entries, asJSON)
}

// takeAuditStringValue extracts the value of a string flag, advancing next to
// the index of the consumed value token when the "--flag value" form is used.
func takeAuditStringValue(args []string, i int, name string) (value string, next int, errMsg string) {
	arg := args[i]
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), i, ""
	}
	if arg == name {
		if i+1 >= len(args) {
			return "", i, "argent: " + name + " needs a value"
		}
		return args[i+1], i + 1, ""
	}
	return "", i, ""
}

// runAuditFiltered searches every daily audit file, applies filter and prints
// the matches. It is only reached when at least one filter flag was supplied.
func runAuditFiltered(app App, dir string, filter AuditFilter, tail int, asJSON bool) int {
	files := allAuditFiles(dir)
	if len(files) == 0 {
		fmt.Fprintf(app.IO.Out, "no audit log under %s\n", dir)
		return 0
	}
	var entries []security.AuditEntry
	for _, path := range files {
		batch, err := security.ReadAudit(path)
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		entries = append(entries, batch...)
	}
	entries = FilterAuditEntries(entries, filter)
	if tail < len(entries) {
		entries = entries[len(entries)-tail:]
	}
	return printAuditEntries(app.IO.Out, app.IO.Err, entries, asJSON)
}

// allAuditFiles returns every .jsonl file under dir, name-sorted so the files
// (and therefore their entries) are read oldest day first.
func allAuditFiles(dir string) []string {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range dirEntries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths
}

// printAuditEntries writes entries in the command's two supported formats.
// It is shared by the default and filtered paths so both format identically.
func printAuditEntries(out, errOut io.Writer, entries []security.AuditEntry, asJSON bool) int {
	if asJSON {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		fmt.Fprintln(out, string(data))
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(out, "%s\t%-8s\t%s\t%s\n", entry.TS, entry.Decision, entry.Tool, entry.Reason)
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
	fmt.Fprint(w, `usage: argent audit [--tail <n>] [--json] [--decision <d>] [--session <id>] [--tool <name>] [--since <time>]

Prints audit entries from <storage>/audit (default: last 20 from the newest daily file).
With any filter flag, searches every daily file and prints matches oldest first.
Timestamps accept RFC 3339, plain dates and CJK forms (e.g. 2026年01月02日).
Arguments and reasons are already redacted when written.
`)
}
