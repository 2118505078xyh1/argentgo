package skills

import (
	"sort"
	"strings"
)

// Fields 保存从类 YAML frontmatter 块解析出的值。
//
// 只支持技能或命令文件所需的子集：`key: scalar`、
// 带引号的标量、`key: [a, b]` 内联列表、缩进的 `- item` 列表以及
// `>` / `|` 块标量。刻意没有 YAML 依赖：输入是手写的 markdown，一个完整的解析器反而会接受我们本不想读取的文档。
// 输入是手写的 markdown，一个完整的解析器反而会接受我们
// 本不想读取的文档。
type Fields struct {
	scalars map[string]string
	lists   map[string][]string
}

// ParseFields 解析两个 `---` 栅栏之间的文本。
//
// 键被转为小写。重复键保留最后一个值。不属于
// `key: value` 且不属于缩进块的行被忽略，因此
// 游离的散文行不会使整个文档失败。
func ParseFields(frontmatter string) Fields {
	f := Fields{scalars: make(map[string]string), lists: make(map[string][]string)}
	lines := strings.Split(strings.ReplaceAll(frontmatter, "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indentOf(line) > 0 {
			// 游离的散文行不会使整个文档失败。
			// 没有所属键的缩进行：属于已消费的块，或是垃圾。无论哪种都没有可附加之处。
			continue
		}
		key, value, ok := splitField(line)
		if !ok {
			continue
		}

		block, next := gatherBlock(lines, i+1)
		i = next - 1

		switch {
		case value == "":
			switch {
			case isListBlock(block):
				f.lists[key] = parseListBlock(block)
				f.scalars[key] = strings.Join(f.lists[key], ", ")
			case hasContent(block):
				f.scalars[key] = blockText("", block)
			default:
				f.scalars[key] = ""
			}
		case strings.HasPrefix(value, ">"), strings.HasPrefix(value, "|"):
			f.scalars[key] = blockText(value, block)
		case strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]"):
			f.lists[key] = splitInlineList(value[1 : len(value)-1])
			f.scalars[key] = strings.Join(f.lists[key], ", ")
		default:
			f.scalars[key] = unquote(value)
		}
	}
	return f
}

// Get 返回 key 的标量值，缺失时返回 ""。
func (f Fields) Get(key string) string {
	return f.scalars[strings.ToLower(strings.TrimSpace(key))]
}

// List 返回 key 的值。标量 `a, b` 按逗号拆分，因此
// `tools: [a, b]` 和 `tools:\n  - a\n  - b` 行为相同。
func (f Fields) List(key string) []string {
	key = strings.ToLower(strings.TrimSpace(key))
	if values, ok := f.lists[key]; ok {
		out := make([]string, len(values))
		copy(out, values)
		return out
	}
	return splitInlineList(f.scalars[key])
}

// Keys 返回见到的所有键，已排序。
func (f Fields) Keys() []string {
	seen := make(map[string]bool, len(f.scalars)+len(f.lists))
	for key := range f.scalars {
		seen[key] = true
	}
	for key := range f.lists {
		seen[key] = true
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func splitField(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:idx]))
	if key == "" {
		return "", "", false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return "", "", false
		}
	}
	return key, strings.TrimSpace(line[idx+1:]), true
}

func gatherBlock(lines []string, from int) (block []string, next int) {
	for next = from; next < len(lines); next++ {
		line := lines[next]
		if strings.TrimSpace(line) == "" {
			block = append(block, "")
			continue
		}
		if indentOf(line) == 0 {
			break
		}
		block = append(block, line)
	}
	// 末尾空行属于下一个字段而非本块。
	for len(block) > 0 && strings.TrimSpace(block[len(block)-1]) == "" {
		block = block[:len(block)-1]
		next--
	}
	return block, next
}

func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

func hasContent(block []string) bool {
	for _, line := range block {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func isListBlock(block []string) bool {
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return trimmed == "-" || strings.HasPrefix(trimmed, "- ")
	}
	return false
}

func parseListBlock(block []string) []string {
	var values []string
	for _, line := range block {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "-")
		for _, item := range splitInlineList(trimmed) {
			if item != "" {
				values = append(values, item)
			}
		}
	}
	return values
}

func splitInlineList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		item := unquote(strings.TrimSpace(part))
		if item != "" {
			values = append(values, item)
		}
	}
	return values
}

// blockText 渲染 `>`、`|` 或普通缩进块。末尾空行
// 总是被去掉；因此裁剪指示符（`>-`、`|-`）含义相同
// 于其普通形式。
func blockText(marker string, block []string) string {
	lines := dedent(block)
	if len(lines) == 0 {
		return ""
	}
	if strings.HasPrefix(marker, "|") {
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	var paragraphs []string
	var current []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				paragraphs = append(paragraphs, strings.Join(current, " "))
				current = nil
			}
			continue
		}
		current = append(current, strings.TrimSpace(line))
	}
	if len(current) > 0 {
		paragraphs = append(paragraphs, strings.Join(current, " "))
	}
	return strings.TrimRight(strings.Join(paragraphs, "\n"), "\n")
}

func dedent(block []string) []string {
	common := -1
	for _, line := range block {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := indentOf(line)
		if common < 0 || n < common {
			common = n
		}
	}
	if common < 0 {
		return nil
	}
	lines := make([]string, 0, len(block))
	for _, line := range block {
		if len(line) >= common {
			lines = append(lines, line[common:])
		} else {
			lines = append(lines, strings.TrimLeft(line, " \t"))
		}
	}
	return lines
}

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return value
	}
	switch value[0] {
	case '"':
		if value[len(value)-1] != '"' {
			return value
		}
		// 转义从左到右解码。链式 ReplaceAll 会在
		// `\n` 在 `\\` 之前，把字面量 `\\n`（反斜杠 + "n"）变成
		// 换行并破坏引号中的 Windows 路径等值。
		inner := value[1 : len(value)-1]
		var b strings.Builder
		for i := 0; i < len(inner); {
			if inner[i] != '\\' || i+1 >= len(inner) {
				b.WriteByte(inner[i])
				i++
				continue
			}
			switch inner[i+1] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				// 未知转义：逐字节保留。
				b.WriteByte('\\')
				b.WriteByte(inner[i+1])
			}
			i += 2
		}
		return b.String()
	case '\'':
		if value[len(value)-1] != '\'' {
			return value
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

// SplitFrontmatter 从正文中拆分开头的 `---` 块。起始栅栏必须是第一行，结束栅栏必须独占一行。没有块的文件返回 ok=false，由调用方决定
//
// 起始栅栏必须是第一行，结束栅栏必须独占
// 一行。没有块的文件返回 ok=false，由调用方决定
// 那是错误（技能）还是可接受（命令）。
func SplitFrontmatter(text string) (frontmatter, body string, ok bool) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var rest string
	switch {
	case strings.HasPrefix(text, "---\n"):
		rest = text[len("---\n"):]
	case text == "---":
		return "", "", true
	default:
		return "", "", false
	}
	offset := 0
	for {
		end := strings.IndexByte(rest[offset:], '\n')
		lineEnd := len(rest)
		var line string
		if end < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+end]
			lineEnd = offset + end
		}
		if strings.TrimSpace(line) == "---" {
			bodyStart := lineEnd
			if end >= 0 {
				bodyStart++
			}
			if bodyStart > len(rest) {
				bodyStart = len(rest)
			}
			return rest[:offset], rest[bodyStart:], true
		}
		if end < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return "", "", false
}
