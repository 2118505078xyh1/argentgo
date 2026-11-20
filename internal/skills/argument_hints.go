package skills

// Skill 参数提示补全元数据。
//
// SkillArgumentHints 把一组技能的声明性 `argument-hint` frontmatter 转换为以技能名为键的枚举候选值。前端
// 技能转换为以技能名为键的枚举候选值。前端
// （例如调用技能的一个斜杠命令）可以用该映射提供
// （例如调用技能的一个斜杠命令）可以用该映射提供实时参数建议，而无需解析 frontmatter 或重复本包的
// 提示语法。该辅助函数是纯函数：只读取其参数，从不
// 触碰文件系统，也从不修改给它的技能。
//
// 提示语法。该辅助函数是纯函数：只读取其参数，从不触碰文件系统，也从不修改给它的技能。
// 提示语法镜像斜杠命令的参数提示。含 '|' 的以空白分隔标记是枚举选项，例如 `dev|stage|prod` 或
// `[dev|stage|prod]`；先去掉一层可选的 <...> 或 [...] 包裹。
// 像 `<file>` 这样的普通占位符描述开放取值且不产生
// 候选项。全角 ASCII（包括全角 '|' 和括号）被折叠为半角，使通过中文输入法编写的提示被以与 ASCII 拼写
// 被折叠为半角，使通过中文输入法编写的提示被以
// 相同的方式读取。

import "strings"

// SkillArgumentHints 返回每个声明了至少一个候选参数的技能的枚举参数候选项，以技能名为键。
// 声明了至少一个候选参数的技能，以技能名为键。
//
// Rules:
//
//   - 只有在去掉一层可选 <...> 或 [...] 包裹后
//     包含 '|' 的标记才有贡献；一组需要两个或更多不同值
//     才算枚举。
//   - 只有在去掉一层可选 <...> 或 [...] 包裹后包含 '|' 的标记才有贡献；一组需要两个或更多不同值才算枚举。
//   - 空备选项和重复项被丢弃；顺序遵循提示。
//   - 没有提示或只有开放占位符的技能没有条目，因此对它们的查找返回 nil 切片。
//   - 重复名称：最后一次出现胜出（普通 map 语义）。
//   - nil 或空输入产生空但非 nil 的 map。
func SkillArgumentHints(skills []Skill) map[string][]string {
	out := make(map[string][]string, len(skills))
	for _, skill := range skills {
		candidates := argumentHintCandidates(skill.ArgumentHint)
		if len(candidates) == 0 {
			continue
		}
		out[skill.Name] = candidates
	}
	return out
}

// argumentHintCandidates 从一个 argument-hint 中提取枚举值。
func argumentHintCandidates(hint string) []string {
	var out []string
	for _, raw := range strings.Fields(hint) {
		token := foldHintWidth(raw)
		bare := token
		if len(bare) >= 2 {
			first, last := bare[0], bare[len(bare)-1]
			if (first == '<' && last == '>') || (first == '[' && last == ']') {
				bare = bare[1 : len(bare)-1]
			}
		}
		if !strings.Contains(bare, "|") {
			continue // a plain placeholder, not a choice
		}
		parts := strings.Split(bare, "|")
		group := make([]string, 0, len(parts))
		seen := make(map[string]bool, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			group = append(group, part)
		}
		if len(group) >= 2 {
			out = append(out, group...)
		}
	}
	return out
}

// foldHintWidth 把全角 ASCII 和表意空格折叠为半角形式，镜像斜杠命令补全，使通过中文输入法键入的提示被识别。CJK 字符本身保持不变。
// 半角形式，镜像斜杠命令补全，使通过
// 中文输入法键入的提示被识别。CJK 字符本身保持不变。
func foldHintWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000': // ideographic space
			b.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E': // full-width ASCII block
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
