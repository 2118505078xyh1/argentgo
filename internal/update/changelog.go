package update

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// extractChangelogSection 从 markdown 格式的变更日志中提取指定版本的段落。
//
// 匹配规则：
//   - 段落以二级标题（"## vX.Y.Z ..."）开始，到下一个二级标题结束；
//     三级及更深的子标题属于当前段落，会被完整保留。
//   - version 可以带或不带前导 "v"/"V"，标题也可以带或不带该前缀，两者
//     会先各自去掉前缀再比较，因此 "1.2.0" 与 "v1.2.0" 可互相匹配。
//   - 版本号按完整标记匹配：其后必须是字符串结尾或分隔符（空白、连字符、
//     破折号、冒号等），因此 "1.2" 不会误匹配标题 "v1.2.0"。
//   - 标题中的日期、描述以及段落正文会原样保留（含中文内容）；返回值会
//     去掉首尾空白。找不到匹配段落时返回空串。
//
// 本函数为纯函数：不做任何 I/O，不修改全局状态。匹配与切分只依据行边界
// 和 UTF-8 符文边界，不会在多字节字符中间切分，因此对 CJK 输入安全。
func extractChangelogSection(changelog, version string) string {
	want := strings.TrimSpace(version)
	want = strings.TrimPrefix(strings.TrimPrefix(want, "v"), "V")
	if want == "" {
		return ""
	}

	lines := strings.Split(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n")
	start := -1
	for index, line := range lines {
		level, text, ok := markdownHeading(line)
		if ok && level == 2 && headingMatchesVersion(text, want) {
			start = index
			break
		}
	}
	if start < 0 {
		return ""
	}

	var section strings.Builder
	for index := start + 1; index < len(lines); index++ {
		// 只有同级（二级）标题才结束当前段落；更深的子标题保留在段落内。
		if level, _, ok := markdownHeading(lines[index]); ok && level == 2 {
			break
		}
		if section.Len() > 0 {
			section.WriteByte('\n')
		}
		section.WriteString(lines[index])
	}
	return strings.TrimSpace(section.String())
}

// headingMatchesVersion 报告二级标题文本是否标注了指定版本。
// headingText 形如 "v0.8.0 — 2026-11-20"；版本号之后的字符必须是
// 结尾或分隔符，避免把较短的版本号当成较长版本号的前缀命中。
func headingMatchesVersion(headingText, want string) bool {
	text := strings.TrimSpace(headingText)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "v"), "V")
	if !strings.HasPrefix(text, want) {
		return false
	}
	rest := text[len(want):]
	if rest == "" {
		return true
	}
	first, size := utf8.DecodeRuneInString(rest)
	switch {
	case unicode.IsSpace(first), first == '–', first == '—', first == ':':
		// 空白、破折号与冒号均可作为版本号与日期/描述之间的分隔符。
		return true
	case first == '-':
		// ASCII 连字符可能属于预发布后缀（如 1.0.0-beta.2），只有当它
		// 后面是空白或字符串结尾时，才视为版本号与日期之间的分隔符。
		after := rest[size:]
		if after == "" {
			return true
		}
		next, _ := utf8.DecodeRuneInString(after)
		return unicode.IsSpace(next)
	}
	return false
}
