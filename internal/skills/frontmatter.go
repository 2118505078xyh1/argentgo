package skills

import (
	"sort"
	"strings"
)

// Fields holds the values parsed from a YAML-ish frontmatter block.
//
// Only the subset a skill or command file needs is supported: `key: scalar`,
// quoted scalars, `key: [a, b]` inline lists, indented `- item` lists and the
// `>` / `|` block scalars. There is deliberately no YAML dependency: the input
// is hand-written markdown, and a full parser would accept documents we do not
// want to read anyway.
type Fields struct {
	scalars map[string]string
	lists   map[string][]string
}

// ParseFields parses the text between the two `---` fences.
//
// Keys are lower-cased. A repeated key keeps its last value. Lines that are
// not `key: value` and are not part of an indented block are ignored, so a
// stray prose line cannot fail a whole document.
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
			// An indented line with no owning key: part of a block already
			// consumed, or junk. Either way there is nothing to attach it to.
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

// Get returns the scalar value for key, or "" when it is absent.
func (f Fields) Get(key string) string {
	return f.scalars[strings.ToLower(strings.TrimSpace(key))]
}

// List returns the values for key. A scalar `a, b` is split on commas, so both
// `tools: [a, b]` and `tools:\n  - a\n  - b` behave the same way.
func (f Fields) List(key string) []string {
	key = strings.ToLower(strings.TrimSpace(key))
	if values, ok := f.lists[key]; ok {
		out := make([]string, len(values))
		copy(out, values)
		return out
	}
	return splitInlineList(f.scalars[key])
}

// Keys returns every key seen, sorted.
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
	// Trailing blank lines belong to the next field, not this block.
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

// blockText renders a `>`, `|` or plain indented block. Trailing blank lines
// are always dropped; chomping indicators (`>-`, `|-`) therefore mean the same
// thing as their plain forms.
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
		inner := value[1 : len(value)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		inner = strings.ReplaceAll(inner, `\t`, "\t")
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	case '\'':
		if value[len(value)-1] != '\'' {
			return value
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

// SplitFrontmatter splits a leading `---` block from the body.
//
// The opening fence must be the first line and the closing fence a line of its
// own. A file without a block returns ok=false and the caller decides whether
// that is an error (skills) or acceptable (commands).
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
