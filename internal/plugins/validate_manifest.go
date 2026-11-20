package plugins

import (
	"fmt"
	"strings"
)

// IssueKind 分类 PluginIssue，使调用方可分组、过滤或统计
// 发现而无需解析消息文本。
type IssueKind string

const (
	// IssueEmptyField 报告为空的必填字段。
	IssueEmptyField IssueKind = "empty_field"
	// IssueInvalidName 报告不安全或畸形的插件名。
	IssueInvalidName IssueKind = "invalid_name"
	// IssueInvalidVersion 报告非语义化版本。
	IssueInvalidVersion IssueKind = "invalid_version"
	// IssuePathEscape 报告解析到 bundle 之外的声明路径。
	IssuePathEscape IssueKind = "path_escape"
	// IssueDuplicateSource 报告命名同一来源的两条声明。
	IssueDuplicateSource IssueKind = "duplicate_source"
	IssueIncompleteMCP   IssueKind = "incomplete_mcp"
)

// PluginIssue 是 ValidateManifest 的一条结构发现。它与
// Problem 有两点不同：它只通过检查 Manifest 产生（从不
// 触碰文件系统），且携带 Field 和 Kind 供编程使用。
// PluginIssue 是 ValidateManifest 的一条结构发现。它与 Problem 有两点不同：它只通过检查 Manifest 产生（从不触碰文件系统），且携带 Field 和 Kind 供编程使用。关于不存在文件的发现属于 Manifest.Validate。String 渲染发现供日志使用。
type PluginIssue struct {
	Field   string    `json:"field,omitempty"`
	Kind    IssueKind `json:"kind"`
	Message string    `json:"message"`
}

// String 渲染发现供日志使用。
func (i PluginIssue) String() string {
	if i.Field != "" {
		return fmt.Sprintf("%s: %s", i.Field, i.Message)
	}
	return i.Message
}

// ValidateManifest 报告解析后清单的结构问题：缺失
// 必填字段、名称和版本格式、逃逸 bundle 的路径，以及
// 重复的 MCP/技能/命令来源。
//
// 它是纯函数：路径针对 m.Root 解析但不打开文件。使用
// ValidateManifest 报告解析后清单的结构问题：缺失必填字段、名称和版本格式、逃逸 bundle 的路径，以及重复的 MCP/技能/命令来源。它是纯函数：路径针对 m.Root 解析但不打开文件。需要检查文件系统的发现（缺失或类型错误的文件）请使用 Manifest.Validate。发现按稳定顺序返回：名称、版本、技能、命令、钩子，然后是按排序名称顺序的 MCP 服务器。
// 或类型错误的文件）。发现按稳定顺序返回：名称、
// 版本、技能、命令、钩子，然后是按排序名称顺序的 MCP 服务器。
func ValidateManifest(m Manifest) []PluginIssue {
	var issues []PluginIssue
	add := func(field string, kind IssueKind, format string, args ...any) {
		issues = append(issues, PluginIssue{
			Field:   field,
			Kind:    kind,
			Message: fmt.Sprintf(format, args...),
		})
	}

	// 必填字段。空值报告一次为缺失字段；
	// 非空值检查合法性，因此两者永不重复。
	if strings.TrimSpace(m.Name) == "" {
		add("name", IssueEmptyField, "name is empty")
	} else if err := ValidateName(m.Name); err != nil {
		add("name", IssueInvalidName, "%v", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		add("version", IssueEmptyField, "version is empty")
	} else if err := ValidateVersion(m.Version); err != nil {
		add("version", IssueInvalidVersion, "version %q: %v", m.Version, err)
	}

	// 声明的路径：离开 bundle 的路径是致命的接线问题，
	// 且同一来源的两种拼写（"skills"、"./skills"、"sub/../skills"）
	// 必须被标记，使清单作者能折叠它们。
	checkPaths := func(field string, paths []string) {
		seen := make(map[string]string, len(paths))
		for _, raw := range paths {
			resolved := m.ResolvePath(raw)
			if !within(m.Root, resolved) {
				add(field, IssuePathEscape, "%s path %q escapes the bundle", field, raw)
				continue
			}
			if first, ok := seen[resolved]; ok {
				add(field, IssueDuplicateSource, "%s source %q duplicates %q", field, raw, first)
				continue
			}
			seen[resolved] = raw
		}
	}
	checkPaths("skills", m.Skills)
	checkPaths("commands", m.Commands)
	checkPaths("hooks", m.Hooks.Paths)

	// MCP 服务器。map 不能容纳两个相同的名称，但两个名称可以
	// 必填字段。空值报告一次为缺失字段；非空值检查合法性，因此两者永不重复。声明的路径：离开 bundle 的路径是致命的接线问题，且同一来源的两种拼写（"skills"、"./skills"、"sub/../skills"）必须被标记，使清单作者能折叠它们。MCP 服务器：map 不能容纳两个相同的名称，但两个名称可以指向同一 command 或 url，这几乎总是值得在接线构建前呈现的复制/粘贴错误。
	// 值得在接线构建前呈现的复制/粘贴错误。
	bySource := make(map[string]string)
	for _, name := range sortedServerNames(m.MCPServers) {
		spec := m.MCPServers[name]
		field := "mcpServers." + name
		if strings.TrimSpace(spec.Command) == "" && strings.TrimSpace(spec.URL) == "" {
			add(field, IssueIncompleteMCP, "mcp server %q has neither command nor url", name)
			continue
		}
		source := spec.URL
		if source == "" {
			source = spec.Command + "\x00" + strings.Join(spec.Args, "\x00")
		}
		if first, ok := bySource[source]; ok {
			add(field, IssueDuplicateSource, "mcp server %q uses the same source as %q", name, first)
			continue
		}
		bySource[source] = name
	}
	return issues
}
