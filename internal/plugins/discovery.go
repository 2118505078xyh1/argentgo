package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StateFileName is the per-plugins-directory enablement file.
const StateFileName = "state.json"

// State is the persisted enablement of a plugins directory. A plugins
// directory without a state file enables every installed bundle.
type State struct {
	Enabled []string `json:"enabled"`
}

// Bundle is one discovered plugin directory.
type Bundle struct {
	Name     string
	Version  string
	Dir      string
	Manifest Manifest
	Enabled  bool
	// Problems holds non-fatal findings about this bundle.
	Problems []Problem
}

// ProjectPluginsDir returns the workspace plugins directory, <root>/.argent/plugins.
func ProjectPluginsDir(root string) string {
	return filepath.Join(root, ".argent", "plugins")
}

// UserPluginsDir returns $HOME/.argent-go/plugins.
func UserPluginsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("plugins: locate home directory: %w", err)
	}
	return filepath.Join(home, ".argent-go", "plugins"), nil
}

// Roots lists the discovery roots in precedence order: the workspace first,
// then the user directory.
func Roots(root string) []string {
	roots := make([]string, 0, 2)
	if strings.TrimSpace(root) != "" {
		roots = append(roots, ProjectPluginsDir(root))
	}
	if home, err := UserPluginsDir(); err == nil {
		roots = append(roots, home)
	}
	return roots
}

// Discover scans every root and returns all bundles in precedence order. A
// name seen twice keeps the first bundle; the later one is reported as a
// problem. Per-bundle validation findings are returned both here and in
// Bundle.Problems.
func Discover(root string) ([]Bundle, []Problem, error) {
	var bundles []Bundle
	var problems []Problem
	byName := make(map[string]string)
	for _, dir := range Roots(root) {
		found, rootProblems := scanRoot(dir)
		problems = append(problems, rootProblems...)

		state, hasState, err := LoadState(dir)
		if err != nil {
			return bundles, problems, err
		}
		enabled := make(map[string]bool, len(state.Enabled))
		for _, name := range state.Enabled {
			enabled[name] = true
		}

		for _, bundle := range found {
			bundle.Enabled = !hasState || enabled[bundle.Name]
			problems = append(problems, bundle.Problems...)
			if first, duplicate := byName[bundle.Name]; duplicate {
				problems = append(problems, Problem{
					Plugin:  bundle.Name,
					Path:    bundle.Dir,
					Message: fmt.Sprintf("duplicate plugin name; already provided by %s", first),
				})
				continue
			}
			byName[bundle.Name] = bundle.Dir
			bundles = append(bundles, bundle)
		}
	}
	return bundles, problems, nil
}

// Resolve returns the enabled bundles for a workspace. Validation findings do
// not fail the call; use Discover to see them.
func Resolve(root string) ([]Bundle, error) {
	bundles, _, err := Discover(root)
	if err != nil {
		return nil, err
	}
	enabled := make([]Bundle, 0, len(bundles))
	for _, bundle := range bundles {
		if bundle.Enabled {
			enabled = append(enabled, bundle)
		}
	}
	return enabled, nil
}

// Validate aggregates manifest problems and duplicate names across bundles.
func Validate(bundles []Bundle) []Problem {
	var problems []Problem
	seen := make(map[string]string, len(bundles))
	for _, bundle := range bundles {
		problems = append(problems, bundle.Manifest.Validate()...)
		if first, duplicate := seen[bundle.Name]; duplicate {
			problems = append(problems, Problem{
				Plugin:  bundle.Name,
				Path:    bundle.Dir,
				Message: fmt.Sprintf("duplicate plugin name; already provided by %s", first),
			})
			continue
		}
		seen[bundle.Name] = bundle.Dir
	}
	return problems
}

// scanRoot scans one plugins directory: the directory itself may be a bundle,
// and each entry may be a bundle or a container holding plugins/* bundles.
func scanRoot(dir string) ([]Bundle, []Problem) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var bundles []Bundle
	var problems []Problem
	if _, ok := FindManifest(dir); ok {
		bundle, bundleProblems := loadBundle(dir)
		if bundle != nil {
			bundles = append(bundles, *bundle)
		}
		return bundles, bundleProblems
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []Problem{{Path: dir, Message: fmt.Sprintf("read plugins directory: %v", err)}}
	}
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		if !isDir(child) {
			continue
		}
		if _, ok := FindManifest(child); ok {
			bundle, bundleProblems := loadBundle(child)
			problems = append(problems, bundleProblems...)
			if bundle != nil {
				bundles = append(bundles, *bundle)
			}
			continue
		}
		nested := filepath.Join(child, "plugins")
		if !isDir(nested) {
			continue
		}
		nestedEntries, err := os.ReadDir(nested)
		if err != nil {
			problems = append(problems, Problem{Path: nested, Message: fmt.Sprintf("read bundled plugins: %v", err)})
			continue
		}
		for _, nestedEntry := range nestedEntries {
			candidate := filepath.Join(nested, nestedEntry.Name())
			if !isDir(candidate) {
				continue
			}
			bundle, bundleProblems := loadBundle(candidate)
			problems = append(problems, bundleProblems...)
			if bundle != nil {
				bundles = append(bundles, *bundle)
			}
		}
	}
	return bundles, problems
}

// loadBundle parses one bundle directory. A parse failure is a problem, not an
// error: one broken bundle must not hide the others.
func loadBundle(dir string) (*Bundle, []Problem) {
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, []Problem{{Path: dir, Message: err.Error()}}
	}
	problems := manifest.Validate()
	return &Bundle{
		Name:     manifest.Name,
		Version:  manifest.Version,
		Dir:      manifest.Root,
		Manifest: manifest,
		Enabled:  true,
		Problems: problems,
	}, problems
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// LoadState reads <pluginsDir>/state.json. The boolean reports whether the
// file exists; a missing file means every installed bundle is enabled.
func LoadState(pluginsDir string) (State, bool, error) {
	data, err := os.ReadFile(filepath.Join(pluginsDir, StateFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, false, nil
		}
		return State{}, false, fmt.Errorf("plugins: read state of %s: %w", pluginsDir, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, true, fmt.Errorf("plugins: invalid state %s: %w", filepath.Join(pluginsDir, StateFileName), err)
	}
	return state, true, nil
}

// SaveState writes <pluginsDir>/state.json.
func SaveState(pluginsDir string, state State) error {
	names := dedupe(state.Enabled)
	if names == nil {
		names = []string{}
	}
	data, err := json.MarshalIndent(State{Enabled: names}, "", "  ")
	if err != nil {
		return fmt.Errorf("plugins: encode state: %w", err)
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return fmt.Errorf("plugins: create %s: %w", pluginsDir, err)
	}
	path := filepath.Join(pluginsDir, StateFileName)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("plugins: write state %s: %w", path, err)
	}
	return nil
}

// SetEnabled turns one plugin on or off in a plugins directory. When no state
// file exists yet it is seeded with every installed bundle first, so enabling
// one plugin never silently disables the others.
func SetEnabled(pluginsDir, name string, enabled bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	state, exists, err := LoadState(pluginsDir)
	if err != nil {
		return err
	}
	names := state.Enabled
	if !exists {
		names = installedNames(pluginsDir)
	}
	if enabled {
		if !containsName(names, name) {
			names = append(names, name)
		}
	} else {
		filtered := names[:0]
		for _, existing := range names {
			if existing != name {
				filtered = append(filtered, existing)
			}
		}
		names = filtered
	}
	return SaveState(pluginsDir, State{Enabled: names})
}

// installedNames lists the bundles directly under one plugins directory.
func installedNames(pluginsDir string) []string {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		candidate := filepath.Join(pluginsDir, entry.Name())
		if !isDir(candidate) {
			continue
		}
		manifest, err := LoadManifest(candidate)
		if err != nil {
			continue
		}
		names = append(names, manifest.Name)
	}
	sort.Strings(names)
	return names
}

func containsName(names []string, name string) bool {
	for _, existing := range names {
		if existing == name {
			return true
		}
	}
	return false
}

func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	var out []string
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func sortedServerNames(servers map[string]MCPServer) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
