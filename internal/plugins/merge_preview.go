package plugins

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ConflictKind 标识 MergeConflict 所指的贡献类别。
type ConflictKind string

const (
	ConflictMCPServer ConflictKind = "mcp_server"
	ConflictSkill     ConflictKind = "skill"
	ConflictCommand   ConflictKind = "command"
	ConflictHook      ConflictKind = "hook"
)

// Contribution 是清单会添加到合并接线中的一项。
type Contribution struct {
	// Plugin 是声明清单的名称。
	Plugin string
	// Name 是冲突键：MCP 服务器键，或
	// 技能/命令/钩子路径的基础名（下游加载器按名称注册这些）。
	Name string
	// ConflictKind 标识 MergeConflict 所指的贡献类别。Contribution 是清单会添加到合并接线中的一项。Plugin 是声明清单的名称。Name 是冲突键：MCP 服务器键，或技能/命令/钩子路径的基础名（下游加载器按名称注册这些）。Source 是服务器的 MCP 命令（或 URL），以及技能、命令和钩子的解析路径。
	// 对技能、命令和钩子的解析路径。
	Source string
}

// MergeConflict 是被多个清单认领的名称。Owners 按
// 输入顺序保存声明插件名。
type MergeConflict struct {
	Kind   ConflictKind
	Name   string
	Owners []string
}

// MergeConflict 是被多个清单认领的名称。Owners 按输入顺序保存声明插件名。MergePreview 是 PreviewMerge 的预演结果。每个切片保持清单的输入顺序，且在清单内保持声明顺序（MCP 服务器使用排序的名称顺序以匹配 Merge）。冲突使用固定种类顺序和排序名称，使值稳定可比较。
// 清单的输入顺序，且在清单内保持声明顺序（MCP
// 服务器使用排序名称顺序以匹配 Merge）。冲突使用固定种类
// 顺序和排序名称，使值稳定可比较。
type MergePreview struct {
	MCPServers []Contribution
	Skills     []Contribution
	Commands   []Contribution
	Hooks      []Contribution
	Conflicts  []MergeConflict
}

// claim 记录哪个清单贡献了某名称。ownerIndex 防止共享同一 Name 的两个不同清单折叠为一个所有者。PreviewMerge 报告给定清单合并时会贡献什么，以及哪些名称被多个清单认领。它是纯预演：路径针对每个清单的 Root 解析，但不打开文件、不启动服务器、不写磁盘。
// 共享同一 Name 的两个不同清单折叠为一个所有者。
type claim struct {
	ownerIndex int
	ownerName  string
}

// PreviewMerge 报告给定清单合并时会贡献什么，
// 以及哪些名称被多个清单认领。它是纯预演：
// 路径针对每个清单的 Root 解析，但不打开文件、不启动
// 服务器、不写磁盘。
//
// Merge 只保留给定名称的第一个 MCP 服务器，并按解析形式对
// 技能/命令/钩子路径去重；它从不呈现两个 bundle
// 竞争同一注册表名称这一事实。PreviewMerge 使那些
// 跨清单冲突在接线构建之前可见。
func PreviewMerge(manifests []Manifest) MergePreview {
	var preview MergePreview
	claims := map[ConflictKind]map[string][]claim{
		ConflictMCPServer: {},
		ConflictSkill:     {},
		ConflictCommand:   {},
		ConflictHook:      {},
	}
	addClaim := func(kind ConflictKind, name string, index int, owner string) {
		claims[kind][name] = append(claims[kind][name], claim{ownerIndex: index, ownerName: owner})
	}

	for index, manifest := range manifests {
		owner := manifest.Name
		if owner == "" {
			owner = filepath.Base(filepath.Clean(manifest.Path))
		}
		if owner == "" || owner == "." {
			owner = "unnamed"
		}

		for _, serverName := range sortedServerNames(manifest.MCPServers) {
			spec := manifest.MCPServers[serverName]
			source := spec.Command
			if source == "" {
				source = spec.URL
			}
			preview.MCPServers = append(preview.MCPServers, Contribution{
				Plugin: owner, Name: serverName, Source: source,
			})
			addClaim(ConflictMCPServer, serverName, index, owner)
		}

		groups := []struct {
			kind  ConflictKind
			paths []string
			dest  *[]Contribution
		}{
			{ConflictSkill, manifest.Skills, &preview.Skills},
			{ConflictCommand, manifest.Commands, &preview.Commands},
			{ConflictHook, manifest.Hooks.Paths, &preview.Hooks},
		}
		for _, group := range groups {
			for _, relative := range group.paths {
				resolved := manifest.ResolvePath(relative)
				name := path.Base(strings.ReplaceAll(resolved, "\\", "/"))
				if name == "." || name == "/" {
					name = resolved
				}
				*group.dest = append(*group.dest, Contribution{
					Plugin: owner, Name: name, Source: resolved,
				})
				addClaim(group.kind, name, index, owner)
			}
		}
	}

	for _, kind := range []ConflictKind{ConflictMCPServer, ConflictSkill, ConflictCommand, ConflictHook} {
		byName := claims[kind]
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			forName := byName[name]
			// 清单重复一个名称是 ValidateManifest 发现，而非跨清单冲突，因此只统计不同的来源清单。
			// 冲突，因此只统计不同的来源清单。
			byIndex := make(map[int]bool, len(forName))
			owners := make([]string, 0, len(forName))
			for _, entry := range forName {
				if byIndex[entry.ownerIndex] {
					continue
				}
				byIndex[entry.ownerIndex] = true
				owners = append(owners, entry.ownerName)
			}
			if len(byIndex) < 2 {
				continue
			}
			preview.Conflicts = append(preview.Conflicts, MergeConflict{
				Kind: kind, Name: name, Owners: owners,
			})
		}
	}
	return preview
}
