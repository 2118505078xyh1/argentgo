package session

import "strings"

// 压缩摘要固定 schema 需要的最小小节。EnsureSummarySections 只补齐缺失的骨架，
// 不重写已有小节，也不负责生成 Highlights（该段由模型侧填充）。
const (
	schemaTitleGoal  = "## Goal"
	schemaTitleState = "## State"
	schemaTitleNext  = "## Next"
	schemaTitleFiles = "## Files"

	// 行号/命令等自由文本写入 Files 段前裁剪到的符文上限，避免单行过长。
	schemaLineLimit = 100
)

// 空骨架：用包内既有的 "- (none)" 占位约定表示"有结构但无内容"，
// 使 Generate 后的摘要对 SummaryIsEmpty 仍判定为空，供上层触发重试。
const (
	schemaGoalSkeleton  = "## Goal\n- (none)\n"
	schemaStateSkeleton = "## State\n- Done: (none)\n- InProgress: (none)\n- Blocked: (none)\n"
	schemaNextSkeleton  = "## Next\n- (none)\n"
)

// FileOp 是从工具事件中提取的一条文件操作记录，供 Files 段机械生成使用。
// Path 为文件路径，Edited 标识是否为修改操作（false 视为读取），
// Lines 为行号或命令等自由文本（写入前会被裁剪到 schemaLineLimit 个符文）。
type FileOp struct {
	Path   string
	Edited bool
	Lines  string
}

// EnsureSummarySections 检测摘要中缺失的 Goal / State / Next / Files 小节，
// 仅把缺失的小节按固定顺序（Goal、State、Next、Files）追加到末尾；已有小节
// 的标题与正文逐字保留，不做重排或改写。
//
// 四个小节各自有规范标题，同时接受常见别名（如 "Next steps"、"Files changed"、
// "目标"、"状态"）作为"已存在"，避免产生重复标题。只有 Files 段缺失时才用
// fileOps 机械生成内容：按 Read / Edited 分类，每条一行，行号/命令裁剪到 100 字符。
//
// 本函数是纯函数：不修改 fileOps，不读全局状态，输出只取决于入参，且对相同入参
// 恒定返回相同字符串。
func EnsureSummarySections(summary string, fileOps []FileOp) string {
	result := summary
	if !schemaHasSection(result, schemaTitleGoal) {
		result = schemaAppendSection(result, schemaGoalSkeleton)
	}
	if !schemaHasSection(result, schemaTitleState) {
		result = schemaAppendSection(result, schemaStateSkeleton)
	}
	if !schemaHasSection(result, schemaTitleNext) {
		result = schemaAppendSection(result, schemaNextSkeleton)
	}
	if !schemaHasSection(result, schemaTitleFiles) {
		result = schemaAppendSection(result, schemaFilesSection(fileOps))
	}
	return result
}

// SummaryIsEmpty 判定摘要是否"无实质内容"：空白、仅有标题、仅有占位符
// （"- (none)"、"- Done: (none)"、"- Read:" 等）都视为空。
// 供上层据此决定是否重试模型摘要。
func SummaryIsEmpty(summary string) bool {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !schemaIsEmptyContent(line) {
			return false
		}
	}
	return true
}

// schemaSectionAliases 是各规范小节标题的别名表（小写、无前导 '#'、无首尾冒号）。
// 命中任一别名即认为该小节已存在，不再补骨架。
var schemaSectionAliases = map[string][]string{
	schemaTitleGoal:  {"goal", "goals", "目标", "目的"},
	schemaTitleState: {"state", "状态", "status"},
	schemaTitleNext:  {"next", "next steps", "nextstep", "下一步", "后续"},
	schemaTitleFiles: {"files", "files changed", "文件", "修改文件"},
}

// schemaHasSection 判断 summary 中是否出现指定规范小节的标题或其别名。
// 只识别行首的 "##"（允许更多 '#'），因此项目符号里的 "- ## Goal" 不算标题。
func schemaHasSection(summary, title string) bool {
	aliases := schemaSectionAliases[title]
	for _, line := range strings.Split(summary, "\n") {
		t, ok := schemaHeadingTitle(line)
		if !ok {
			continue
		}
		for _, alias := range aliases {
			if t == alias {
				return true
			}
		}
	}
	return false
}

// schemaHeadingTitle 提取 markdown 二级及以上标题的标题文本，统一为小写，
// 去掉首尾 '#'、空白与冒号。非标题行返回 false。
func schemaHeadingTitle(line string) (string, bool) {
	if !strings.HasPrefix(line, "#") {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(line, "#"))
	title = strings.TrimSpace(strings.Trim(title, ":："))
	if title == "" {
		return "", false
	}
	return strings.ToLower(title), true
}

// schemaAppendSection 把 block 追加到 text 末尾，并保证两者之间恰好有一个空行。
// 它只追加换行，绝不裁剪已有文本，因此 text 的内容字节作为前缀原样保留。
func schemaAppendSection(text, block string) string {
	if text == "" {
		return block
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	return text + block
}

// schemaFilesSection 依据 fileOps 生成完整的 Files 段。
// Read 在前、Edited 在后；同一类别保持 fileOps 的原始顺序。路径与行文本都先
// 折叠为单行，行文本再裁剪到 schemaLineLimit 个符文（按 rune 切分，CJK 安全）。
func schemaFilesSection(fileOps []FileOp) string {
	var b strings.Builder
	b.WriteString(schemaTitleFiles + "\n")
	for _, category := range []struct {
		label  string
		edited bool
	}{
		{label: "Read", edited: false},
		{label: "Edited", edited: true},
	} {
		b.WriteString("- " + category.label + ":\n")
		n := 0
		for _, op := range fileOps {
			if op.Edited != category.edited {
				continue
			}
			entry := schemaOneLine(op.Path)
			if entry == "" {
				continue
			}
			if lines := schemaTruncateRunes(op.Lines, schemaLineLimit); lines != "" {
				entry += ": " + lines
			}
			b.WriteString("  - " + entry + "\n")
			n++
		}
		if n == 0 {
			b.WriteString("  - (none)\n")
		}
	}
	return b.String()
}

// schemaOneLine 把换行与多余空白折叠为单行，防止路径或行文本注入伪造的标题行。
func schemaOneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// schemaTruncateRunes 将文本折叠为单行并裁剪到 limit 个符文。
// 按 rune 切分，不会把多字节 UTF-8（含 CJK）截断在字符中间。
func schemaTruncateRunes(s string, limit int) string {
	s = schemaOneLine(s)
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit]))
}

// schemaIsEmptyContent 判断一行内容是否只是占位符或空标签。
// 支持 "Done: (none)" / "Read:" 这类"标签 + 空值"形式。
func schemaIsEmptyContent(line string) bool {
	item := strings.TrimSpace(strings.TrimLeft(line, " \t-*+•"))
	if schemaEmptyMarker(item) {
		return true
	}
	if i := strings.IndexAny(item, ":："); i >= 0 {
		return schemaEmptyMarker(item[i+1:])
	}
	return false
}

func schemaEmptyMarker(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "(none)", "none", "n/a", "无", "(无)", "（无）", "(空)":
		return true
	}
	return false
}
