package tui

import (
	"encoding/json"
	"strconv"
	"strings"
)

// markdownLines 用模型实际发出的那一小部分 markdown 渲染一个块：
// 围栏代码、行内代码、粗体、标题和表格。
func markdownLines(textValue string, width int, first, rest string, p Palette) []Line {
	var out []Line
	inCode := false
	firstLine := true
	// 每个输出行都带有 first/rest 前缀（助手标记 "◆ " 或
	// 其缩进），因此内容预算是宽度减去该前缀 —— 正是
	// wrapStyled 对普通行使用的规则。表格必须使用相同的
	// 预算，否则其右边框会越过记录边缘。
	prefixWidth := maxInt(TextWidth(first), TextWidth(rest))
	bodyWidth := maxInt(width-prefixWidth, 1)
	lines := strings.Split(textValue, "\n")
	for i := 0; i < len(lines); {
		raw := lines[i]
		lineText := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(lineText), "```") {
			inCode = !inCode
			i++
			continue
		}
		if inCode {
			for _, segment := range Wrap(lineText, maxInt(width-4, 1)) {
				code := NewLine()
				code.Push("  "+segment, p.muted())
				out = append(out, code)
			}
			firstLine = false
			i++
			continue
		}
		// 表格块是这样一行：紧跟一个由 '|' 构成的分隔符
		// 行。把它整体渲染以保持列对齐，然后跳过它。
		if i+1 < len(lines) && strings.Contains(lineText, "|") {
			if _, ok := parseTableDelimiter(lines[i+1]); ok {
				end := i + 2
				for end < len(lines) && strings.Contains(lines[end], "|") {
					end++
				}
				for _, row := range renderMarkdownTable(lines[i:end], bodyWidth, TextWidth, Wrap) {
					prefix := rest
					if firstLine {
						prefix = first
					}
					line := NewLine()
					line.Push(prefix, Style{})
					line.Push(row, Style{})
					out = append(out, line)
					firstLine = false
				}
				i = end
				continue
			}
		}
		prefix := rest
		if firstLine {
			prefix = first
		}
		out = append(out, wrapStyled(inlineSpans(lineText, p), width, prefix, rest)...)
		firstLine = false
		i++
	}
	if len(out) == 0 {
		out = append(out, PlainLine(first))
	}
	return out
}

// inlineSpans 把一行拆为带样式的片段：`code`、**粗体**、标题和
// 项目符号标记。
func inlineSpans(lineText string, p Palette) []Span {
	if strings.HasPrefix(lineText, "#") {
		body := strings.TrimSpace(strings.TrimLeft(lineText, "#"))
		return []Span{{Text: body, Style: Style{Bold: true}}}
	}
	if strings.HasPrefix(lineText, "- ") || strings.HasPrefix(lineText, "* ") {
		rest := parseInline(lineText[2:], p)
		out := []Span{{Text: "- ", Style: p.accent()}}
		return append(out, rest...)
	}
	return parseInline(lineText, p)
}

func parseInline(textValue string, p Palette) []Span {
	var spans []Span
	var buffer strings.Builder
	flush := func() {
		if buffer.Len() > 0 {
			spans = append(spans, Span{Text: buffer.String()})
			buffer.Reset()
		}
	}
	runes := []rune(textValue)
	for i := 0; i < len(runes); i++ {
		switch {
		case runes[i] == '`':
			end := i + 1
			for end < len(runes) && runes[end] != '`' {
				end++
			}
			if end < len(runes) && end > i+1 {
				flush()
				spans = append(spans, Span{Text: string(runes[i+1 : end]), Style: p.code()})
				i = end
				continue
			}
			buffer.WriteRune(runes[i])
		case runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '*':
			end := i + 2
			for end+1 < len(runes) && !(runes[end] == '*' && runes[end+1] == '*') {
				end++
			}
			if end+1 < len(runes) && end > i+2 {
				flush()
				spans = append(spans, Span{Text: string(runes[i+2 : end]), Style: Style{Bold: true}})
				i = end + 1
				continue
			}
			buffer.WriteRune(runes[i])
		default:
			buffer.WriteRune(runes[i])
		}
	}
	flush()
	if len(spans) == 0 {
		spans = []Span{{Text: ""}}
	}
	return spans
}

// styledCluster 是带有其所属 span 样式的一个字素簇。
// 换行按簇而非 rune 进行，因此 ZWJ emoji、旗帜或
// 组合序列绝不会被拆到两行（见 screen.go 中的 Wrap）。
type styledCluster struct {
	text  string
	s     Style
	cells int
}

// splitGraphemes 按顺序返回 s 的字素簇。nextGrapheme 是
// 是簇边界和单元格宽度的唯一权威；在这里复用它
// 使记录换行与 TextWidth/Clip/Wrap 保持一致。
func splitGraphemes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for i := 0; i < len(s); {
		next, _ := nextGrapheme(s, i)
		out = append(out, s[i:next])
		i = next
	}
	return out
}

// wrapStyled 对带样式的片段换行：有空格时在空格处断行，
// 否则硬断（这是中文的常见路径）。断点落在
// 字素簇之间，使多 rune 的 emoji 保持完整。
func wrapStyled(spans []Span, width int, first, rest string) []Line {
	prefixWidth := maxInt(TextWidth(first), TextWidth(rest))
	bodyWidth := maxInt(width-prefixWidth, 1)

	clusters := make([]styledCluster, 0, 32)
	for _, span := range spans {
		for _, text := range splitGraphemes(span.Text) {
			clusters = append(clusters, styledCluster{text: text, s: span.Style, cells: TextWidth(text)})
		}
	}

	var out []Line
	lineStart := 0
	used := 0
	lastSpace := -1
	for i := 0; i < len(clusters); i++ {
		cell := clusters[i].cells
		if used+cell > bodyWidth && i > lineStart {
			end := i
			next := i
			if lastSpace > lineStart {
				end = lastSpace
				next = lastSpace + 1
			}
			out = append(out, buildStyledLine(clusters[lineStart:end], prefixFor(len(out), first, rest)))
			lineStart = next
			used = 0
			for j := lineStart; j < i; j++ {
				used += clusters[j].cells
			}
			lastSpace = -1
		}
		used += cell
		if clusters[i].text == " " {
			lastSpace = i
		}
	}
	out = append(out, buildStyledLine(clusters[lineStart:], prefixFor(len(out), first, rest)))
	return out
}

func prefixFor(index int, first, rest string) string {
	if index == 0 {
		return first
	}
	return rest
}

func buildStyledLine(chunk []styledCluster, prefix string) Line {
	line := NewLine()
	line.Push(prefix, Style{})
	for i := 0; i < len(chunk); {
		j := i
		var builder strings.Builder
		for j < len(chunk) && chunk[j].s == chunk[i].s {
			builder.WriteString(chunk[j].text)
			j++
		}
		line.Push(builder.String(), chunk[i].s)
		i = j
	}
	return line
}

// wrappedBlock 用每行都带的前缀包装纯文本。
func wrappedBlock(body string, width int, first, rest string, style Style) []Line {
	var out []Line
	for _, paragraph := range strings.Split(body, "\n") {
		for _, segment := range Wrap(paragraph, width) {
			line := NewLine()
			line.Push(prefixFor(len(out), first, rest), style)
			line.Push(segment, style)
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = append(out, PlainLine(first))
	}
	return out
}

// toolLines 渲染 name(args-summary) 加上截断的输出预览。
func toolLines(entry Entry, width int, expanded bool, p Palette) []Line {
	glyph := toolMark
	style := p.tool()
	switch {
	case entry.Failed:
		glyph, style = "✖ ", p.failure()
	case entry.Running:
		glyph, style = "◐ ", p.warning()
	}
	head := NewLine()
	head.Push(glyph, style)
	label := entry.Name
	if summary := summarizeArgs(entry.Args); summary != "" {
		label += "(" + summary + ")"
	} else {
		label += "()"
	}
	head.Push(Clip(label, maxInt(width-2, 1)), Style{Bold: true})
	out := []Line{head}

	limit := 3
	if expanded {
		limit = 20
	}
	if oldText, newText, ok := editTexts(entry.Name, entry.Args); ok {
		return append(out, diffPreviewLines(DiffLines(oldText, newText), limit, width, p)...)
	}
	if entry.Output != "" {
		if StructuredToolResults {
			out = append(out, structuredOutputLines(entry.Output, limit, width, p)...)
			return out
		}
		bodyWidth := maxInt(width-2, 1)
		rest := strings.TrimRight(entry.Output, "\n")
		shown := 0
		for rest != "" && shown < limit {
			row := rest
			if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
				row, rest = rest[:nl], rest[nl+1:]
			} else {
				rest = ""
			}
			// 只折叠与剩余行数一样多的片段。最后
			// 一个片段被截断，而不是把任意长的行折叠进
			// 每个片段，直到限额检查拒绝它们。
			for _, segment := range wrapLimited(row, bodyWidth, limit-shown) {
				line := NewLine()
				line.Push("  ", Style{})
				line.Push(Clip(segment, bodyWidth), p.muted())
				out = append(out, line)
				shown++
			}
		}
		if remaining := countLines(rest); remaining > 0 {
			line := NewLine()
			line.Push("  ", Style{})
			line.Push("… "+strconv.Itoa(remaining)+" more lines", p.dim())
			out = append(out, line)
		}
	}
	return out
}

// editCall 同时覆盖 edit schema 和 multi_edit 批次 schema。Edits
// 递归保存批次中每个文件的调用。
type editCall struct {
	Old     string     `json:"old"`
	New     string     `json:"new"`
	OldText string     `json:"old_text"`
	NewText string     `json:"new_text"`
	Edits   []editCall `json:"edits"`
}

// editTexts 返回 edit 或 multi_edit 调用的 before/after 文本。
// 非 edit 工具、格式错误的 JSON 或缺失的一侧返回 ok=false，使
// 调用方保留原来的输出预览渲染。
func editTexts(name, args string) (oldText, newText string, ok bool) {
	if name != "edit" && name != "multi_edit" {
		return "", "", false
	}
	var call editCall
	if err := json.Unmarshal([]byte(args), &call); err != nil {
		return "", "", false
	}
	sides := func(e editCall) (string, string) {
		old, new := e.Old, e.New
		if old == "" {
			old = e.OldText
		}
		if new == "" {
			new = e.NewText
		}
		return old, new
	}
	if name == "multi_edit" {
		for _, e := range call.Edits {
			old, new := sides(e)
			if old == "" || new == "" {
				continue
			}
			if oldText != "" {
				oldText += "\n"
			}
			if newText != "" {
				newText += "\n"
			}
			oldText, newText = oldText+old, newText+new
		}
		return oldText, newText, oldText != "" && newText != ""
	}
	oldText, newText = sides(call)
	return oldText, newText, oldText != "" && newText != ""
}

// diffPreviewLines 用与普通工具输出预览相同的行预算和 "N more lines"
// 标记渲染差异。相邻的 "-"/"+" 行按词
// 对齐，使小改动只高亮变化的词。
func diffPreviewLines(diffs []DiffLine, limit, width int, p Palette) []Line {
	var out []Line
	bodyWidth := maxInt(width-4, 1)
	shown := 0
	for shown < limit && shown < len(diffs) {
		diff := diffs[shown]
		// 仅当两行都仍在预算内时才配对，使行数
		// 和 "N more lines" 结果与整行基准一致。
		if diff.Kind == DiffDel && shown+1 < limit && shown+1 < len(diffs) &&
			diffs[shown+1].Kind == DiffAdd {
			oldSegs, newSegs := WordDiff(diff.Text, diffs[shown+1].Text)
			out = append(out,
				wordDiffRow("- ", oldSegs, bodyWidth, p, false),
				wordDiffRow("+ ", newSegs, bodyWidth, p, true),
			)
			shown += 2
			continue
		}
		out = append(out, plainDiffRow(diff, bodyWidth, p))
		shown++
	}
	if remaining := len(diffs) - shown; remaining > 0 {
		line := NewLine()
		line.Push("  ", Style{})
		line.Push("… "+strconv.Itoa(remaining)+" more lines", p.dim())
		out = append(out, line)
	}
	return out
}

// plainDiffRow 用单个整行 span 渲染不成对的差异行，这是
// 孤立删除或新增仍然回退到的原始行为。
func plainDiffRow(diff DiffLine, bodyWidth int, p Palette) Line {
	line := NewLine()
	line.Push("  ", Style{})
	switch diff.Kind {
	case DiffAdd:
		line.Push("+ ", p.tool())
		line.Push(Clip(diff.Text, bodyWidth), p.tool())
	case DiffDel:
		line.Push("- ", p.failure())
		line.Push(Clip(diff.Text, bodyWidth), p.failure())
	default:
		line.Push("  ", p.muted())
		line.Push(Clip(diff.Text, bodyWidth), p.muted())
	}
	return line
}

// wordDiffRow 渲染成对 del/add 行的一侧。未变化的片段
// 保持行的普通样式；变化的片段获得强背景色，使
// 被编辑的词突出。各片段共享一个按显示单元格累加的宽度预算，
// 越过边界的片段在字素边界处被截断，而不是在 rune 中间切开
// （使 CJK 和 emoji 保持完整）。
func wordDiffRow(marker string, segs []WordSegment, bodyWidth int, p Palette, add bool) Line {
	line := NewLine()
	line.Push("  ", Style{})
	base, changed := p.failure(), changedWordStyle(p, false)
	if add {
		base, changed = p.tool(), changedWordStyle(p, true)
	}
	line.Push(marker, base)
	used := 0
	for _, seg := range segs {
		if used >= bodyWidth {
			break
		}
		text, style := seg.Text, base
		if seg.Changed {
			style = changed
		}
		cells := TextWidth(text)
		if room := bodyWidth - used; cells > room {
			line.Push(Clip(text, room), style)
			break
		}
		line.Push(text, style)
		used += cells
	}
	return line
}

// changedWordStyle 给变化的词一个彩色背景带。它复用
// 失败/工具的调色板颜色，使主题和 NO_COLOR 的降级方式
// 与渲染器的其余部分一致。
func changedWordStyle(p Palette, add bool) Style {
	if p.noColor {
		return Style{}
	}
	if add {
		return Style{Bg: p.Tool, Bold: true}
	}
	return Style{Bg: p.Error, Bold: true}
}

// countLines 统计行数，而不分配 strings.Split 会为
// 过大工具输出构建的 []string。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// wrapFirst 镜像 Wrap 的一步：返回第一个折叠后的片段和
// 未消费的剩余部分，一找到第一个断点就停止。它
// 通过 nextGrapheme 遍历字素簇 —— 与 Wrap、Clip
// 和 Buffer.WriteString 使用的同一权威 —— 使 ZWJ emoji、旗帜或组合序列
// 绝不会被撕到两个预览行之间。在这里用 RuneWidth 逐个 rune 测量
// 过去会在行尾留下悬空的连接符或半面旗帜。
func wrapFirst(text string, width int) (string, string) {
	if width < 1 {
		width = 1
	}
	var cur []string         // grapheme clusters, in order
	used, lastSpace := 0, -1 // lastSpace indexes cur
	for pos := 0; pos < len(text); {
		next, cell := nextGrapheme(text, pos)
		cluster := text[pos:next]
		if used+cell > width && len(cur) > 0 {
			if lastSpace >= 1 {
				head := strings.TrimRight(strings.Join(cur[:lastSpace], ""), " ")
				tail := strings.Join(cur[lastSpace:], "")
				for len(tail) > 0 && tail[0] == ' ' {
					tail = tail[1:]
				}
				return head, tail + text[pos:]
			}
			return strings.Join(cur, ""), text[pos:]
		}
		cur = append(cur, cluster)
		used += cell
		if cluster == " " {
			lastSpace = len(cur) - 1
		}
		pos = next
	}
	return strings.Join(cur, ""), ""
}

// wrapLimited 最多折叠 maxLines 个片段；最后一个被截断，使
// 单个巨大行的开销为 O(maxLines*width)，而不是 O(行长度) 个片段。
func wrapLimited(text string, width, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if width < 1 {
		width = 1
	}
	if text == "" {
		return []string{""}
	}
	var out []string
	rest := text
	for len(out) < maxLines-1 {
		head, tail := wrapFirst(rest, width)
		if head == "" && tail == "" {
			return out
		}
		out = append(out, head)
		if tail == "" {
			return out
		}
		rest = tail
	}
	out = append(out, Clip(rest, width))
	return out
}

// summarizeArgs 选出唯一值得展示的参数：让工具调用一眼可辨的
// path、command 或 query。
func summarizeArgs(args string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err == nil {
		for _, key := range []string{"path", "command", "query", "pattern", "url", "name", "uri", "job"} {
			if value, ok := decoded[key].(string); ok && value != "" {
				return Clip(collapseSpace(value), 48)
			}
		}
	}
	flat := collapseSpace(args)
	if flat == "{}" || flat == "null" {
		return ""
	}
	return Clip(flat, 48)
}

func collapseSpace(textValue string) string {
	return strings.Join(strings.Fields(textValue), " ")
}
