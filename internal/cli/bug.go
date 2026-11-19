package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// runBug implements `bug [--out path]`: a diagnostics dump an issue report can
// attach. It never includes API keys or token secrets.
func runBug(app App, _ context.Context, args []string) int {
	outPath := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h" || args[i] == "--help":
			bugUsage(app.IO.Out)
			return 0
		case args[i] == "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --out needs a value")
				return 2
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(args[i], "--out="):
			outPath = strings.TrimPrefix(args[i], "--out=")
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", args[i])
			bugUsage(app.IO.Err)
			return 2
		}
	}

	if outPath == "" {
		stamp := time.Now().UTC().Format("20060102-150405")
		outPath = filepath.Join(app.storageDir(), "bug-"+stamp+".txt")
	}
	body := bugReport(app)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if err := os.WriteFile(outPath, []byte(body), 0o600); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "wrote %s\n", outPath)
	return 0
}

// bugReport renders the dump. Every probe is fail-soft: a broken config is
// reported as an error line, not a crash.
func bugReport(app App) string {
	var b strings.Builder
	root, _ := os.Getwd()
	fmt.Fprintf(&b, "argentgo %s\n", app.Version)
	fmt.Fprintf(&b, "generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "go: %s\n", runtime.Version())
	fmt.Fprintf(&b, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "cwd: %s\n", root)
	fmt.Fprintf(&b, "storage: %s\n", app.storageDir())
	fmt.Fprintf(&b, "config: %s\n", app.configPath())

	if cfg, err := config.Load(root, app.env); err != nil {
		fmt.Fprintf(&b, "config error: %v\n", err)
	} else {
		fmt.Fprintf(&b, "model: %s\nprotocol: %s\nprovider: %s\nsandbox: %s\nmaxTurns: %d\n",
			cfg.Model, cfg.Protocol, firstNonEmpty(cfg.ProviderName, "(default)"), cfg.SandboxMode, cfg.MaxTurns)
		fmt.Fprintf(&b, "config sources: %s\n", strings.Join(cfg.Sources, ", "))
	}
	if infos, err := app.sessionStore().List(); err == nil {
		fmt.Fprintf(&b, "sessions: %d\n", len(infos))
	} else {
		fmt.Fprintf(&b, "sessions error: %v\n", err)
	}
	if store, err := authStore(app); err == nil {
		if tokens, err := store.List(); err == nil {
			names := make([]string, 0, len(tokens))
			for _, token := range tokens {
				names = append(names, token.Provider)
			}
			fmt.Fprintf(&b, "stored credentials: %s\n", strings.Join(names, ", "))
		}
	}
	if path := latestAuditFile(filepath.Join(app.storageDir(), "audit")); path != "" {
		fmt.Fprintf(&b, "latest audit: %s\n", path)
	}
	fmt.Fprintf(&b, "tools: %s\n", strings.Join(builtinToolNames(), ", "))
	return b.String()
}

// builtinToolNames lists the registry's tools without building a workspace.
func builtinToolNames() []string {
	return []string{
		"read", "write", "edit", "bash", "ls", "grep", "find",
		"glob", "todo", "multi_edit", "web_fetch", "git_status",
	}
}

func bugUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent bug [--out <path>]

Writes a diagnostics dump (version, platform, config, storage, session count,
credential provider names, tool list) for an issue report. Secrets are never
included. Default path: <storage>/bug-<timestamp>.txt.
`)
}
