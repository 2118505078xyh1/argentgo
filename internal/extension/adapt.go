package extension

import (
	"example.com/argentgo/internal/commands"
	"example.com/argentgo/internal/plugins"
	"example.com/argentgo/internal/skills"
)

// 本文件是四套体系的薄适配：把各自的条目/诊断类型映射成门面类型。
// 它只做字段搬运，不含任何发现/信任/解析逻辑——那些仍留在各自包内。
// 所有函数首字小写（未导出），不触发 archguard 的新增导出原语门禁。

// fromSkills 把技能条目映射为统一描述符。skills 磁盘发现无授权门，
// Trust 保持 Unknown（不是"不信任"，而是该体系无信任概念）。
func fromSkills(s []skills.Skill) []Descriptor {
	out := make([]Descriptor, 0, len(s))
	for _, item := range s {
		out = append(out, Descriptor{Kind: KindSkill, Name: item.Name, Path: item.Path, Trust: TrustUnknown})
	}
	return out
}

// fromCommands 把命令条目映射为统一描述符。插件命令的 Path 可能为空，
// 用 Plugin 字段作为来源标识补充到 Path（仅为诊断可读性，不参与匹配）。
func fromCommands(c []commands.Command) []Descriptor {
	out := make([]Descriptor, 0, len(c))
	for _, item := range c {
		path := item.Path
		if path == "" {
			path = item.Plugin
		}
		out = append(out, Descriptor{Kind: KindCommand, Name: item.Name, Path: path, Trust: TrustUnknown})
	}
	return out
}

// fromPlugins 把插件 bundle 映射为统一描述符。
func fromPlugins(b []plugins.Bundle) []Descriptor {
	out := make([]Descriptor, 0, len(b))
	for _, item := range b {
		out = append(out, Descriptor{Kind: KindPlugin, Name: item.Name, Path: item.Dir, Trust: TrustUnknown})
	}
	return out
}

// fromPluginProblems 把插件诊断映射为统一诊断。
func fromPluginProblems(probs []plugins.Problem) []Diagnostic {
	out := make([]Diagnostic, 0, len(probs))
	for _, p := range probs {
		out = append(out, Diagnostic{Kind: DiagnosticInvalid, Source: firstNonEmpty(p.Plugin, p.Path), Message: p.Message})
	}
	return out
}

// fromSkilledDiagnostics 把 hooks 的字符串诊断（校验入口返回 []string）
// 映射为统一诊断。hooks 的警告/错误文本保留原样。
func fromHookStrings(src string, messages []string) []Diagnostic {
	out := make([]Diagnostic, 0, len(messages))
	for _, m := range messages {
		out = append(out, Diagnostic{Kind: DiagnosticInvalid, Source: src, Message: m})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
