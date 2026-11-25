package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/hooks"
)

// runHooks 实现 `hooks [-C root]`：列出每个 hook 文档、它的摘要，
// 以及信任存储是否已授予该摘要。
func runHooks(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argentgo:", err)
		return 2
	}
	for _, arg := range rest {
		switch arg {
		case "-h", "--help":
			hooksUsage(app.IO.Out)
			return 0
		default:
			fmt.Fprintf(app.IO.Err, "argentgo: unexpected argument %q\n", arg)
			hooksUsage(app.IO.Err)
			return 2
		}
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	trust := hooks.NewTrustStore()
	trustPath := filepath.Join(app.storageDir(), "hooks-trust.json")
	if err := trust.LoadFile(trustPath); err != nil {
		fmt.Fprintln(app.IO.Err, "warning:", err)
	}

	paths := hookDocuments(root, homeFromEnv(app.env))
	if len(paths) == 0 {
		fmt.Fprintln(app.IO.Out, "no hook documents found under .argent/hooks.json or ~/.argentgo/hooks.json")
		return 0
	}
	for _, path := range paths {
		digest, err := hooks.DigestFile(path)
		if err != nil {
			fmt.Fprintf(app.IO.Out, "missing\t%s\n", path)
			continue
		}
		state := "untrusted"
		if trust.IsTrusted(path, digest) {
			state = "trusted"
		}
		events := "?"
		if config, err := hooks.LoadConfig(path); err == nil && config != nil {
			events = fmt.Sprintf("%d", len(config.Commands))
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			events = "invalid"
		}
		if len(digest) > 12 {
			digest = digest[:12]
		}
		fmt.Fprintf(app.IO.Out, "%s\t%s\tevents=%s\tdigest=%s\n", state, path, events, digest)
	}
	return 0
}

// hookDocuments 列出会话会考虑的 hook 文件，项目级优先。
func hookDocuments(root, home string) []string {
	var paths []string
	add := func(path string) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			paths = append(paths, path)
		}
	}
	add(filepath.Join(root, ".argent", "hooks.json"))
	if entries, err := os.ReadDir(filepath.Join(root, ".argent", "hooks")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				add(filepath.Join(root, ".argent", "hooks", entry.Name()))
			}
		}
	}
	if home != "" {
		add(filepath.Join(home, ".argentgo", "hooks.json"))
	}
	return paths
}

func hooksUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo hooks [-C <dir>]

Lists hook documents and their trust state. A document runs only when the
SHA-256 of its current bytes is granted in <storage>/hooks-trust.json.
`)
}
