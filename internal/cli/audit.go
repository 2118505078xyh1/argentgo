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

	"example.com/argentgo/internal/security"
)

// runAudit 实现 `audit [--tail n] [--json] [--decision d] [--session id]
// [--tool name] [--since time]`。不带过滤 flag 时，保持原有的
// "来自最新每日文件的最新条目" 行为不变。
func runAudit(app App, _ context.Context, args []string) int {
	tail := 20
	asJSON := false
	var filter AuditFilter
	hasFilter := false

	for i := 0; i < len(args); i++ {
		// 字符串值的过滤 flag，同时支持 "--flag value" 和 "--flag=value"
		// 两种写法。它们的存在会把 runAudit 切换到跨文件搜索。
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
				fmt.Fprintln(app.IO.Err, "argentgo: --tail needs a value")
				return 2
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				fmt.Fprintf(app.IO.Err, "argentgo: --tail needs a non-negative integer, got %q\n", args[i])
				return 2
			}
			tail = n
		case strings.HasPrefix(args[i], "--tail="):
			n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--tail="))
			if err != nil || n < 0 {
				fmt.Fprintf(app.IO.Err, "argentgo: --tail needs a non-negative integer, got %q\n", args[i])
				return 2
			}
			tail = n
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected argument %q\n", args[i])
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

// takeAuditStringValue 提取字符串 flag 的值；当使用 "--flag value" 形式时，
// 把 next 推进到被消费的值 token 的索引。
func takeAuditStringValue(args []string, i int, name string) (value string, next int, errMsg string) {
	arg := args[i]
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), i, ""
	}
	if arg == name {
		if i+1 >= len(args) {
			return "", i, "argentgo: " + name + " needs a value"
		}
		return args[i+1], i + 1, ""
	}
	return "", i, ""
}

// runAuditFiltered 搜索每个每日审计文件，应用过滤条件并打印
// 匹配项。只有在提供了至少一个过滤 flag 时才会走到这里。
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

// allAuditFiles 返回 dir 下所有的 .jsonl 文件，按名称排序，使文件
// （从而其中的条目）按日期从早到晚读取。
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

// printAuditEntries 以该命令支持的两种格式写入条目。
// 默认路径和过滤路径共用它，因此两者的格式完全一致。
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

// latestAuditFile 优先选择当天的文件，其次是现有的最新每日文件。
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
	fmt.Fprint(w, `usage: argentgo audit [--tail <n>] [--json] [--decision <d>] [--session <id>] [--tool <name>] [--since <time>]

Prints audit entries from <storage>/audit (default: last 20 from the newest daily file).
With any filter flag, searches every daily file and prints matches oldest first.
Timestamps accept RFC 3339, plain dates and CJK forms (e.g. 2026年01月02日).
Arguments and reasons are already redacted when written.
`)
}
