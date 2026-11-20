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

// StateFileName 是每个插件目录的启用状态文件。
const StateFileName = "state.json"

// StateFileName 是每个插件目录的启用状态文件。State 是一个插件目录的持久化启用状态。没有状态文件的插件
// 没有状态文件的插件目录启用所有已安装 bundle。
type State struct {
	Enabled []string `json:"enabled"`
}

// Bundle 是一个发现的插件目录。
type Bundle struct {
	Name     string
	Version  string
	Dir      string
	Manifest Manifest
	Enabled  bool
	// 目录启用所有已安装 bundle。Bundle 是一个发现的插件目录。Problems 保存关于此 bundle 的非致命发现。
	Problems []Problem
}

// ProjectPluginsDir 返回工作区插件目录 <root>/.argent/plugins。
func ProjectPluginsDir(root string) string {
	return filepath.Join(root, ".argent", "plugins")
}

// ProjectPluginsDir 返回工作区插件目录 <root>/.argent/plugins。UserPluginsDir 返回 $HOME/.argentgo/plugins。Roots 按优先级列出发现根目录：先是工作区，然后是用户目录。
func UserPluginsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("plugins: locate home directory: %w", err)
	}
	return filepath.Join(home, ".argentgo", "plugins"), nil
}

// Roots 按优先级列出发现根目录：先是工作区，
// 然后是用户目录。
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

// Discover 扫描每个 root 并按优先级返回所有 bundle。出现两次的名称保留第一个 bundle；较后的一个被报告为
// 出现两次的名称保留第一个 bundle；较后的一个被报告为
// 问题。每个 bundle 的验证发现既在这里也在
// 问题。每个 bundle 的验证发现既在这里返回也在 Bundle.Problems 中返回。Resolve 返回工作区启用的 bundle。验证发现不使调用失败；用 Discover 查看它们。
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

// Resolve 返回工作区启用的 bundle。验证发现不
// 使调用失败；用 Discover 查看它们。
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

// Validate 聚合跨 bundle 的清单问题和重复名称。
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

// Validate 聚合跨 bundle 的清单问题和重复名称。scanRoot 扫描一个插件目录：目录本身可以是 bundle，且每个条目
// 且每个条目可以是 bundle 或包含 plugins/* bundle 的容器。
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

// 可以是 bundle 或包含 plugins/* bundle 的容器。loadBundle 解析一个 bundle 目录。解析失败是问题而非
// 错误：一个损坏的 bundle 不得隐藏其他 bundle。
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

// LoadState 读取 <pluginsDir>/state.json。布尔值报告文件
// 是否存在；文件缺失表示所有已安装 bundle 都启用。
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

// 错误：一个损坏的 bundle 不得隐藏其他 bundle。LoadState 读取 <pluginsDir>/state.json。布尔值报告文件是否存在；文件缺失表示所有已安装 bundle 都启用。SaveState 写入 <pluginsDir>/state.json。
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

// SetEnabled 在插件目录中打开或关闭一个插件。当尚无
// SetEnabled 在插件目录中打开或关闭一个插件。当尚无状态文件时，它先用所有已安装 bundle 种入，使启用
// 一个插件绝不会静默禁用其他插件。
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

// 一个插件绝不会静默禁用其他插件。installedNames 列出某插件目录下直接的 bundle。
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
