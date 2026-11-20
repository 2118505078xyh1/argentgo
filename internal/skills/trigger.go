package skills

import (
	"path"
	"strings"
)

// Triggers 保存技能的可选自动激活规则。每个列表都是独立过滤器，空列表表示"无限制"：零触发器的技能在任何地方都符合条件。调用方组合适合其事件（文件路径、提示、模型家族）的
//
// 每个列表都是独立过滤器，空列表表示"无限制"：
// 零触发器的技能在任何地方都符合条件。调用方组合
// 适合其事件（文件路径、提示、模型家族）的过滤器，而不是
// 过滤器，而不是依赖模型按名称调用 use_skill。这里的匹配是纯的：无文件系统访问，除标准库外无依赖。
// 无文件系统访问，除标准库外无依赖。
type Triggers struct {
	// 路径是 gitignore 风格的 glob，与 agent 即将读取或编辑的文件
	// 路径是 gitignore 风格的 glob，与 agent 即将读取或编辑的文件路径匹配。
	Paths []string
	// 关键词是与提示文本匹配的不区分大小写的子串。
	Keywords []string
	// 关键词是与提示文本匹配的不区分大小写的子串。
	// Models 把技能限制在这些模型家族（例如 "claude"、"gemini"）。空列表、"all" 或 "*" 表示所有家族。
	Models []string
}

// MatchPath 报告 p 是否匹配中任一 gitignore 风格 glob。
// patterns.
//
// 语法：`**` 跨越零个或多个路径段，`*` 停留在单个段内，不含斜杠的模式在任意深度针对基础名测试。
// 不含斜杠的模式在任意深度针对基础名测试。
// 模式与 p 用同样的方式规范化：反斜杠变斜杠，
// 模式与 p 用同样的方式规范化：反斜杠变斜杠，去掉前导 `./` 或 `/`，比较不区分大小写。因此一条规则同时适用于 Windows 和 POSIX 路径。
// 因此一条规则同时适用于 Windows 和 POSIX 路径。
func MatchPath(patterns []string, p string) bool {
	segments := strings.Split(normalizeGlobPath(p), "/")
	for _, pattern := range patterns {
		glob := normalizeGlobPath(pattern)
		if glob == "" {
			continue
		}
		if !strings.Contains(glob, "/") {
			glob = "**/" + glob
		}
		if matchSegments(strings.Split(glob, "/"), segments, 0, 0) {
			return true
		}
	}
	return false
}

// matchSegments 一起遍历模式和各路径段。`**` 模式段
// 消耗零个或多个路径段；其他每段交给
// path.Match 拥有 `*` / `?` / `[...]` 通配符语法。
func matchSegments(pattern, segments []string, pi, si int) bool {
	for pi < len(pattern) {
		if pattern[pi] == "**" {
			for next := si; next <= len(segments); next++ {
				if matchSegments(pattern, segments, pi+1, next) {
					return true
				}
			}
			return false
		}
		if si >= len(segments) {
			return false
		}
		ok, err := path.Match(pattern[pi], segments[si])
		if err != nil || !ok {
			return false
		}
		pi++
		si++
	}
	return si == len(segments)
}

// normalizeGlobPath 使模式或具体路径可比较：单一
// normalizeGlobPath 使模式或具体路径可比较：单一分隔符、无前导标记、单一大小写。小写化让 Windows 风格不区分大小写的路径无需构建标签或运行时检查即可工作。
// 不区分大小写的路径无需构建标签或运行时检查即可工作。
func normalizeGlobPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	return strings.ToLower(p)
}

// SkillsForPath 返回适合某文件路径的技能：无路径触发器的技能加上
// SkillsForPath 返回适合某文件路径的技能：无路径触发器的技能加上路径触发器匹配 p 的技能。保留 registry 顺序。
func SkillsForPath(skills []Skill, p string) []Skill {
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Paths) == 0 || MatchPath(skill.Triggers.Paths, p) {
			out = append(out, skill)
		}
	}
	return out
}

// MatchKeywords 报告 text 是否包含任一不区分大小写的关键词。子串匹配使 CJK 关键词仍然可用，而基于词的切分会丢弃它们。
// 关键词。子串匹配使 CJK 关键词仍然可用，而基于词的
// 切分会丢弃它们。
func MatchKeywords(keywords []string, text string) bool {
	if len(keywords) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// SkillsForKeywords 返回适合提示文本的技能：无关键词触发器的技能加上有关键词命中的技能。保留 registry 顺序。
// 有关键词命中的技能。保留 registry 顺序。
func SkillsForKeywords(skills []Skill, text string) []Skill {
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Keywords) == 0 || MatchKeywords(skill.Triggers.Keywords, text) {
			out = append(out, skill)
		}
	}
	return out
}

// SkillsForModel 返回某模型家族可用的技能。family 是
// 不区分大小写比较；无模型限制（或为
// SkillsForModel 返回某模型家族可用的技能。family 不区分大小写比较；无模型限制（或为 "all" / "*"）的技能对所有家族可用。保留 registry 顺序。
func SkillsForModel(skills []Skill, family string) []Skill {
	family = strings.ToLower(strings.TrimSpace(family))
	var out []Skill
	for _, skill := range skills {
		if len(skill.Triggers.Models) == 0 {
			out = append(out, skill)
			continue
		}
		for _, model := range skill.Triggers.Models {
			model = strings.ToLower(strings.TrimSpace(model))
			if model == family || model == "*" || model == "all" {
				out = append(out, skill)
				break
			}
		}
	}
	return out
}

// ResolveTriggeredSkills 是自动技能激活的唯一入口。
// ResolveTriggeredSkills 是自动技能激活的唯一入口。它用文件路径、提示文本和模型家族描述一个 agent 事件，并返回适合该事件的每个技能。
// 模型家族描述一个 agent 事件，并返回适合该事件的每个技能。
//
// 三个触发维度是独立约束：只有当技能声明的每个列表为空（无限制）
// 或被匹配参数满足时，它才符合条件。换句话说，路径、关键词和
// 或被匹配参数满足。换句话说，路径、关键词和
// 模型过滤器是交集而非并集 —— 模型限制不得
// 仅因某关键词恰好匹配而被绕过。空参数
// 因此排除限制到该维度的技能，所以在没有上下文时
// 三个触发维度是独立约束：只有当技能声明的每个列表为空（无限制）或被匹配参数满足时，它才符合条件。换句话说，路径、关键词和模型过滤器是交集而非并集 —— 模型限制不得仅因某关键词恰好匹配而被绕过。
//
// 因此空参数会排除限制到该维度的技能，所以在没有上下文时只留下无限制技能（加上模型通配符技能）。
// 保留 registry 顺序（每个过滤器只移除条目）且没有名称返回两次。nil registry 产生 nil。
func ResolveTriggeredSkills(registry *Registry, path, keywords, modelFamily string) []Skill {
	if registry == nil {
		return nil
	}
	return resolveTriggeredSkills(registry.All(), path, keywords, modelFamily)
}

// resolveTriggeredSkills 是 ResolveTriggeredSkills 基于切片的核心。单独保留它使去重保证能用手建切片直接演练（Registry 已强制名称唯一）。
// 单独保留它使去重保证能直接演练
// 用手建切片（Registry 已强制名称唯一）。
func resolveTriggeredSkills(skills []Skill, path, keywords, modelFamily string) []Skill {
	eligible := SkillsForPath(skills, path)
	eligible = SkillsForKeywords(eligible, keywords)
	eligible = SkillsForModel(eligible, modelFamily)

	seen := make(map[string]bool, len(eligible))
	out := make([]Skill, 0, len(eligible))
	for _, skill := range eligible {
		if seen[skill.Name] {
			continue
		}
		seen[skill.Name] = true
		out = append(out, skill)
	}
	return out
}
