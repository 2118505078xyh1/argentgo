package gui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// skippedDirs are never walked: build output, dependencies and dot directories
// would drown the pickers.
var skippedDirs = map[string]bool{
	"target":       true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
}

// listFiles mirrors the Tauri @-mention picker: a shallow, capped walk of the
// workspace matching a case-insensitive substring of the relative path.
func listFiles(root, query string) []string {
	found := make([]string, 0, 32)
	needle := strings.ToLower(strings.TrimSpace(query))
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 || len(found) >= 200 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if depth > 3 || len(found) >= 200 {
				return
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || skippedDirs[name] {
				continue
			}
			full := filepath.Join(dir, name)
			if entry.IsDir() {
				walk(full, depth+1)
				continue
			}
			relative, err := filepath.Rel(root, full)
			if err != nil {
				continue
			}
			relative = filepath.ToSlash(relative)
			if needle == "" || strings.Contains(strings.ToLower(relative), needle) {
				found = append(found, relative)
			}
		}
	}
	walk(root, 0)
	return found
}

// listWorkspaceFiles is the file tree page: path, base name and directory flag.
func (b *bridge) listWorkspaceFiles() []map[string]any {
	rows := make([]map[string]any, 0, 64)
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 || len(rows) >= 500 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if depth > 4 || len(rows) >= 500 {
				return
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || skippedDirs[name] {
				continue
			}
			full := filepath.Join(dir, name)
			relative, err := filepath.Rel(b.dir, full)
			if err != nil {
				continue
			}
			rows = append(rows, map[string]any{
				"path": filepath.ToSlash(relative),
				"name": name,
				"dir":  entry.IsDir(),
			})
			if entry.IsDir() {
				walk(full, depth+1)
			}
		}
	}
	walk(b.dir, 0)
	return rows
}

// resolve maps a UI relative path into the workspace and rejects escapes.
func resolve(root, relative string, mustExist bool) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("invalid relative path")
	}
	cleaned := filepath.FromSlash(relative)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	target, err := filepath.Abs(filepath.Join(root, cleaned))
	if err != nil {
		return "", errors.New("invalid relative path")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	if mustExist {
		if _, err := os.Stat(target); err != nil {
			return "", errors.New("path does not exist")
		}
	}
	return target, nil
}

func (b *bridge) readWorkspaceFile(relative string) (string, error) {
	target, err := resolve(b.dir, relative, true)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return clip(string(data), 200_000), nil
}

func (b *bridge) writeWorkspaceFile(relative, content string) error {
	target, err := resolve(b.dir, relative, false)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if _, err := os.Stat(parent); err != nil {
		return errors.New("parent directory does not exist")
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func (b *bridge) deleteWorkspaceFile(relative string) error {
	target, err := resolve(b.dir, relative, true)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("directory deletion is not supported")
	}
	return os.Remove(target)
}

func (b *bridge) uploadWorkspaceFile(name, base64Data string) (string, error) {
	base := filepath.Base(filepath.FromSlash(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", errors.New("invalid file name")
	}
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("invalid base64: %w", err)
	}
	if len(data) > 10*1024*1024 {
		return "", errors.New("file is larger than 10 MB")
	}
	relative := "uploads/" + base
	target, err := resolve(b.dir, relative, false)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", err
	}
	return relative, nil
}

// gitStatus returns `git status --porcelain` lines, newest first is not needed:
// the page renders them as they come.
func (b *bridge) gitStatus(ctx context.Context) ([]string, error) {
	out, err := runGit(ctx, b.dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		if len(strings.TrimRight(line, "\r")) > 3 {
			lines = append(lines, strings.TrimRight(line, "\r"))
		}
	}
	return lines, nil
}

func (b *bridge) gitDiff(ctx context.Context, path string) (string, error) {
	out, err := runGit(ctx, b.dir, "diff", "--", path)
	if err != nil {
		return "", err
	}
	return clip(out, 60_000), nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return "", errors.New(message)
		}
		return "", fmt.Errorf("git is unavailable: %w", err)
	}
	return stdout.String(), nil
}

// configRoot is the per-user argent directory (~/.argent by default).
func (b *bridge) configRoot() string {
	return storageRoot(b.opts.StorageDir)
}

// listMCPServers reads the configured server names without starting anything.
func (b *bridge) listMCPServers() []string {
	names := make([]string, 0, 4)
	for _, file := range []string{
		filepath.Join(b.configRoot(), "settings.json"),
		filepath.Join(b.dir, ".argent", "settings.json"),
	} {
		document := readJSONFile(file)
		for _, pointer := range []string{"mcpServers", "mcp"} {
			object, ok := document[pointer].(map[string]any)
			if !ok {
				continue
			}
			if pointer == "mcp" {
				object, ok = object["servers"].(map[string]any)
				if !ok {
					continue
				}
			}
			for key := range object {
				if !contains(names, key) {
					names = append(names, key)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// listPlugins enumerates installed plugin directories and manifests.
func (b *bridge) listPlugins() []string {
	names := make([]string, 0, 4)
	for _, dir := range []string{
		filepath.Join(b.configRoot(), "plugins"),
		filepath.Join(b.configRoot(), "plugins", "cache"),
		filepath.Join(b.dir, ".argent", "plugins"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() && !hasSuffixFold(name, ".json") && !hasSuffixFold(name, ".toml") {
				continue
			}
			stem := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".toml")
			if stem != "" && !contains(names, stem) {
				names = append(names, stem)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (b *bridge) telemetryPreferences() map[string]any {
	document := readJSONFile(filepath.Join(b.configRoot(), "settings.json"))
	enabled, consent := false, false
	if telemetry, ok := document["telemetry"].(map[string]any); ok {
		enabled, _ = telemetry["enabled"].(bool)
		consent, _ = telemetry["consent"].(bool)
	}
	return map[string]any{"enabled": enabled, "consent": consent}
}

func (b *bridge) setTelemetryPreferences(enabled, consent bool) error {
	file := filepath.Join(b.configRoot(), "settings.json")
	document := readJSONFile(file)
	telemetry, ok := document["telemetry"].(map[string]any)
	if !ok {
		telemetry = map[string]any{}
	}
	telemetry["enabled"] = enabled
	telemetry["consent"] = consent
	document["telemetry"] = telemetry
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(body, '\n'), 0o600)
}

// readJSONFile returns an empty object for missing or malformed files; a bad
// settings file must not take the whole page down.
func readJSONFile(file string) map[string]any {
	data, err := os.ReadFile(file)
	if err != nil {
		return map[string]any{}
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return map[string]any{}
	}
	return document
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasSuffixFold(value, suffix string) bool {
	return len(value) >= len(suffix) && strings.EqualFold(value[len(value)-len(suffix):], suffix)
}

// clip truncates on a rune boundary so output never splits a UTF-8 rune.
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
