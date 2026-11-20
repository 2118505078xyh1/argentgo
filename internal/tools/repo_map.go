package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// ---- repo_map ----

const repoMapMaxFiles = 80

// repoMapIgnoredDirs is a small .gitignore-style deny list. The walk and the
// top-level directory listing share it so the two views never disagree.
var repoMapIgnoredDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true,
	".argent": true, "vendor": true, ".idea": true, ".vscode": true,
	"build": true, "out": true,
}

// repoMapEntryPoints are filenames worth flagging for a model reading the map.
var repoMapEntryPoints = map[string]bool{
	"main.go": true, "go.mod": true, "cargo.toml": true, "package.json": true,
	"readme.md": true, "makefile": true, "dockerfile": true, "main.rs": true,
	"main.py": true, "app.py": true, "index.js": true, "index.ts": true,
	"main.js": true, "main.ts": true, "cmakelists.txt": true, "setup.py": true,
}

type repoMapTool struct{}

func (repoMapTool) Name() string { return "repo_map" }
func (repoMapTool) Description() string {
	return "Map the workspace: top-level directories, up to 80 files with line counts and entry-point markers, plus Go symbols ranked by relevance to the optional mention(s)."
}
func (repoMapTool) Mutating() bool { return false }
func (repoMapTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string","description":"directory to map; defaults to the workspace root"},"mention":{"type":"string","description":"identifier or file path the user currently cares about; boosts matching symbols"},"mentions":{"type":"array","items":{"type":"string"},"description":"additional mention strings"},"symbols":{"type":"boolean","description":"include the ranked symbol section (default true)"}}}`
}

func (repoMapTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string   `json:"path"`
		Mention  string   `json:"mention"`
		Mentions []string `json:"mentions"`
		Symbols  *bool    `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("repo_map: bad arguments: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo_map: %s is not a directory", args.Path)
	}

	dirs, err := repoMapTopDirs(base)
	if err != nil {
		return "", err
	}
	// Walk the whole tree so important files that appear late in walk order
	// (e.g. a deeply nested README or go.mod) are still discoverable. The file
	// view is budgeted afterwards, with important files pinned to the front.
	var allFiles []repoMapWalkedFile
	var relFiles []string // first repoMapMaxFiles walked paths, for Go symbols (legacy behaviour)
	err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != base && repoMapIgnoredDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		allFiles = append(allFiles, repoMapWalkedFile{rel: rel, path: path})
		if len(relFiles) < repoMapMaxFiles {
			relFiles = append(relFiles, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	files := repoMapSelectFiles(allFiles)

	lines := []string{"dirs:"}
	if len(dirs) == 0 {
		lines = append(lines, "  (none)")
	} else {
		for _, dir := range dirs {
			lines = append(lines, "  "+dir)
		}
	}
	lines = append(lines, fmt.Sprintf("files (%d):", len(files)))
	for _, file := range files {
		lines = append(lines, "  "+file)
	}
	if len(allFiles) >= repoMapMaxFiles {
		lines = append(lines, fmt.Sprintf("... [capped at %d files]", repoMapMaxFiles))
	}
	// The ranked symbol view is additive: the dirs/files view above is kept
	// verbatim for existing consumers. Pass {"symbols":false} to opt out.
	if args.Symbols == nil || *args.Symbols {
		mentions := make([]string, 0, 1+len(args.Mentions))
		if strings.TrimSpace(args.Mention) != "" {
			mentions = append(mentions, args.Mention)
		}
		mentions = append(mentions, args.Mentions...)
		lines = append(lines, repoMapSymbolSection(extractRepoSymbols(base, relFiles), mentions, repoMapSymbolTokenBudget)...)
	}
	return clip(ctx, strings.Join(lines, "\n")), nil
}

// repoMapWalkedFile is one regular file discovered by the map walk.
type repoMapWalkedFile struct{ rel, path string }

// repoMapFormatFile renders one walked file as the legacy "rel:lines[marker]"
// line. Sorting these strings reproduces the old sort.Strings(files) order.
func repoMapFormatFile(rel, path string) string {
	marker := ""
	if repoMapEntryPoints[strings.ToLower(filepath.Base(rel))] {
		marker = " [entry]"
	}
	return fmt.Sprintf("%s:%d%s", rel, countLines(path), marker)
}

// repoMapSelectFiles builds the budgeted file view. Important project files
// (go.mod, README, Cargo.toml, Dockerfile, CI workflows, ...) are pinned ahead
// of the ordinary files so neither the repoMapMaxFiles cap nor the final output
// clip can crowd them out. Within each group the original lexical order is
// preserved, so ordinary symbol/file ordering is unchanged otherwise.
func repoMapSelectFiles(all []repoMapWalkedFile) []string {
	var important, ordinary []string
	for _, f := range all {
		line := repoMapFormatFile(f.rel, f.path)
		if provider.IsImportantProjectFile(f.rel) {
			important = append(important, line)
		} else {
			ordinary = append(ordinary, line)
		}
	}
	sort.Strings(important)
	sort.Strings(ordinary)

	files := make([]string, 0, repoMapMaxFiles)
	files = append(files, important...)
	if len(files) > repoMapMaxFiles {
		files = files[:repoMapMaxFiles]
	}
	if remaining := repoMapMaxFiles - len(files); remaining > 0 && len(ordinary) > 0 {
		if len(ordinary) > remaining {
			ordinary = ordinary[:remaining]
		}
		files = append(files, ordinary...)
	}
	return files
}

func repoMapTopDirs(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !repoMapIgnoredDirs[entry.Name()] {
			dirs = append(dirs, entry.Name()+"/")
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// countLines treats a trailing newline as terminating the last line, matching
// how editors and `wc -l` report line counts.
func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0
	}
	lines := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}
