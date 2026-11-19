package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/update"
)

// defaultUpdateRepo is where argent-go releases live.
const defaultUpdateRepo = "2118505078xyh1/argent"

// runUpdate implements `update [--check|--apply] [--repo owner/name]`.
func runUpdate(app App, ctx context.Context, args []string) int {
	apply := false
	asJSON := false
	repo := firstNonEmpty(app.env("ARGENT_UPDATE_REPO"), defaultUpdateRepo)
	current := app.Version
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-h" || args[i] == "--help":
			updateUsage(app.IO.Out)
			return 0
		case args[i] == "--check":
			// The default action; accepted for symmetry with --apply.
		case args[i] == "--apply":
			apply = true
		case args[i] == "--json":
			asJSON = true
		case args[i] == "--repo":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --repo needs a value")
				return 2
			}
			i++
			repo = args[i]
		case strings.HasPrefix(args[i], "--repo="):
			repo = strings.TrimPrefix(args[i], "--repo=")
		case args[i] == "--current":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --current needs a value")
				return 2
			}
			i++
			current = args[i]
		case strings.HasPrefix(args[i], "--current="):
			current = strings.TrimPrefix(args[i], "--current=")
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", args[i])
			updateUsage(app.IO.Err)
			return 2
		}
	}
	if strings.TrimSpace(current) == "" {
		current = Version
	}
	if apply {
		fmt.Fprintln(app.IO.Err, "error: update --apply is not wired yet: download the release asset from GitHub and replace the binary")
		return 1
	}

	client := &http.Client{Timeout: 30 * time.Second}
	release, newer, err := update.Check(ctx, repo, current, client)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if asJSON {
		data, err := json.MarshalIndent(map[string]any{
			"current": current,
			"latest":  release.Version(),
			"tag":     release.Tag,
			"url":     release.URL,
			"update":  newer,
			"assets":  release.Assets,
		}, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if !newer {
		fmt.Fprintf(app.IO.Out, "argent-go %s is up to date (latest %s)\n", current, release.Tag)
		return 0
	}
	fmt.Fprintf(app.IO.Out, "a newer release is available: %s (running %s)\n", release.Tag, current)
	fmt.Fprintf(app.IO.Out, "  %s\n", release.URL)
	for _, asset := range release.Assets {
		fmt.Fprintf(app.IO.Out, "  asset: %s (%d bytes)\n", asset.Name, asset.Size)
	}
	fmt.Fprintln(app.IO.Out, "  replace the running binary manually; --apply is not wired yet")
	return 0
}

func updateUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent update [--check] [--repo <owner/name>] [--current <version>]

Checks the latest GitHub release (30s timeout). --apply is not wired yet, so a
newer release is reported with its download URL and assets instead.
`)
}
