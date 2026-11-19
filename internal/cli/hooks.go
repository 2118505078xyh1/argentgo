package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/hooks"
)

// runHooks implements `hooks [-C root]`: every hook document, its digest and
// whether the trust store has granted that digest.
func runHooks(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	for _, arg := range rest {
		switch arg {
		case "-h", "--help":
			hooksUsage(app.IO.Out)
			return 0
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
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
		fmt.Fprintln(app.IO.Out, "no hook documents found under .argent/hooks.json or ~/.argent-go/hooks.json")
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

// hookDocuments lists the hook files a session would consider, project first.
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
		add(filepath.Join(home, ".argent-go", "hooks.json"))
	}
	return paths
}

func hooksUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent hooks [-C <dir>]

Lists hook documents and their trust state. A document runs only when the
SHA-256 of its current bytes is granted in <storage>/hooks-trust.json.
`)
}
