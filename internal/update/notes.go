package update

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// maxSummaryChanges 限制 SummarizeReleaseNotes 打印的变更项数量。
const maxSummaryChanges = 10

// changelogLabels 会被识别为发布正文中的变更日志章节标题
// （不区分大小写，作为子串匹配）。包含中文标签，
// 因为 argent 的发布说明是双语的。
var changelogLabels = []string{
	"changelog", "change log", "what's changed", "whats changed",
	"release notes", "changes",
	"更新日志", "变更记录", "变更日志", "更新内容", "本次更新",
}

var markdownLink = regexp.MustCompile(`!?\[([^\]]+)\]\([^)]*\)`)

// NoteChecksum 是发布说明中找到的 SHA-256 摘要及其
// 后面的文件名。Hash 始终为小写十六进制；对于裸摘要行，
// Name 为空。
type NoteChecksum struct {
	Hash string
	Name string
}

// ReleaseNotes 是 Notes 返回的 GitHub 发布正文的结构化视图。
// ParseReleaseNotes.
type ReleaseNotes struct {
	// Summary 是正文的第一行散文，已去除内联 markdown
	// 标记。标题、列表项和校验和行会被跳过。
	Summary string
	// Changes 是变更日志章节的列表项。若正文中
	// 没有可识别的变更日志标题，则返回所有列表项。
	Changes []string
	// Checksums 是正文中任意位置找到的所有 SHA-256 行，按顺序排列。
	Checksums []NoteChecksum
}

// Notes 将 r.Body 解析为结构化发布说明。它从不失败：空正文
// 或异常正文只会产生空字段。
func (r Release) Notes() ReleaseNotes { return ParseReleaseNotes(r.Body) }

// ParseReleaseNotes 从 GitHub 发布正文中提取简短摘要、变更日志和 SHA-256 行。
// 它是一个纯函数：无 I/O、无修改，且
// 相同输入总是产生相同输出。
//
// 标题可使用英文或中文标签（例如 "## What's Changed"）。
// or "## 更新日志"); bullets may use "-", "*", "+" or "1."; and checksum lines
// 摘要行可能被代码块包裹、以列表标记为前缀，或为裸摘要。
// 变更日志章节内的子标题会使其列表项保留在该章节中；
// 同级或更高级别的标题则结束该章节。
func ParseReleaseNotes(body string) ReleaseNotes {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	hasChangelog := false
	for _, raw := range lines {
		if _, text, ok := markdownHeading(raw); ok && isChangelogHeading(text) {
			hasChangelog = true
			break
		}
	}

	var out ReleaseNotes
	sectionLevel := 0
	inChangelog := false
	var allBullets, changelogBullets []string

	for _, raw := range lines {
		if level, text, ok := markdownHeading(raw); ok {
			if inChangelog && level <= sectionLevel {
				inChangelog = false
			}
			if isChangelogHeading(text) && !inChangelog {
				inChangelog = true
				sectionLevel = level
			}
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			continue
		}
		if sum, ok := parseNoteChecksum(line); ok {
			out.Checksums = append(out.Checksums, sum)
			continue
		}
		if bullet, ok := markdownBullet(line); ok {
			allBullets = append(allBullets, bullet)
			if inChangelog {
				changelogBullets = append(changelogBullets, bullet)
			}
			continue
		}
		if out.Summary == "" {
			out.Summary = stripInlineMarkdown(line)
		}
	}

	if hasChangelog {
		out.Changes = changelogBullets
	} else {
		out.Changes = allBullets
	}
	return out
}

// SummarizeReleaseNotes 将说明渲染为紧凑、确定性的、
// 以换行分隔的摘要，适合用于更新提示或日志行。最多
// 打印 maxSummaryChanges 个变更项；其余部分只计数。
func SummarizeReleaseNotes(notes ReleaseNotes) string {
	var builder strings.Builder
	if notes.Summary != "" {
		builder.WriteString(notes.Summary)
	}
	shown := len(notes.Changes)
	if shown > maxSummaryChanges {
		shown = maxSummaryChanges
	}
	for _, change := range notes.Changes[:shown] {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString("- ")
		builder.WriteString(change)
	}
	if remaining := len(notes.Changes) - shown; remaining > 0 {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "... and %d more change(s)", remaining)
	}
	if count := len(notes.Checksums); count > 0 {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "%d SHA-256 checksum(s)", count)
	}
	return builder.String()
}

// markdownHeading 识别 ATX 标题（"## Foo"）。它返回级别
// （1..6）、标题文本，以及该行是否确为标题。
func markdownHeading(line string) (level int, text string, ok bool) {
	trimmed := strings.TrimSpace(line)
	count := 0
	for count < len(trimmed) && trimmed[count] == '#' {
		count++
	}
	if count == 0 || count > 6 {
		return 0, "", false
	}
	if count < len(trimmed) && trimmed[count] != ' ' && trimmed[count] != '\t' {
		return 0, "", false
	}
	return count, strings.Trim(trimmed[count:], "# \t"), true
}

func isChangelogHeading(text string) bool {
	normalised := strings.ToLower(strings.TrimSpace(text))
	if normalised == "" {
		return false
	}
	for _, label := range changelogLabels {
		if strings.Contains(normalised, label) {
			return true
		}
	}
	return false
}

// markdownBullet 从裁剪后的行中去掉列表或有序列表标记，
// 并移除内联 markdown，使条目读作纯文本。
func markdownBullet(line string) (string, bool) {
	item, ok := stripListMarker(strings.TrimSpace(line))
	if !ok {
		return "", false
	}
	return stripInlineMarkdown(item), true
}

// stripListMarker 去掉前导 "-"、"*"、"+" 或 "1." / "1)" 标记，
// 并报告是否存在这样的标记。
func stripListMarker(line string) (string, bool) {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') &&
		(line[1] == ' ' || line[1] == '\t') {
		return strings.TrimSpace(line[2:]), true
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index > 0 && index+1 < len(line) &&
		(line[index] == '.' || line[index] == ')') &&
		(line[index+1] == ' ' || line[index+1] == '\t') {
		return strings.TrimSpace(line[index+2:]), true
	}
	return line, false
}

// parseNoteChecksum 查找行首的 64 字符 SHA-256 摘要，
// 可选地位于列表标记之后和/或反引号之内，并返回
// 小写化的摘要及其后的文件名（可能为空）。
func parseNoteChecksum(line string) (NoteChecksum, bool) {
	text, _ := stripListMarker(strings.TrimSpace(line))
	text = strings.TrimSpace(strings.Trim(text, "`"))
	if len(text) < sha256.Size*2 {
		return NoteChecksum{}, false
	}
	candidate := text[:sha256.Size*2]
	hash, err := normaliseChecksum(candidate)
	if err != nil {
		return NoteChecksum{}, false
	}
	// 更长的十六进制字符串不是校验和行；其前 64 个字符
	// 只是一个（无效的）标记的一部分。
	if len(text) > sha256.Size*2 && isHexDigit(text[sha256.Size*2]) {
		return NoteChecksum{}, false
	}
	name := strings.TrimSpace(text[sha256.Size*2:])
	name = strings.TrimSpace(strings.TrimLeft(name, "* \t"))
	name = strings.TrimSpace(strings.Trim(name, "`"))
	return NoteChecksum{Hash: hash, Name: name}, true
}

func isHexDigit(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}

// stripInlineMarkdown 去除常见的内联 markdown 装饰，使
// 摘要行读作纯文本。
func stripInlineMarkdown(line string) string {
	text := markdownLink.ReplaceAllString(line, "$1")
	text = strings.NewReplacer("`", "", "**", "", "__", "").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}
