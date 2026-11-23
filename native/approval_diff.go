package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// nativeApprovalDiff 控制原生审批卡是否渲染 diff。默认开启：只要审批参数
// 能提取出编辑信息，审批卡就显示逐行 diff 与摘要；只有
// ARGENT_NATIVE_APPROVAL_DIFF=0 时才作为紧急回退关闭。
var nativeApprovalDiff atomic.Bool

func init() {
	// 测试不调用 main()，因此这里把默认值设为开启，保证默认帧也带 diff。
	nativeApprovalDiff.Store(true)
}

// configureApprovalDiff 按注入的 env 设置审批 diff 显示门控。env 由调用方
// 注入而非直接读 os.Getenv，使渲染路径保持无环境副作用，且测试可精确控制
// 开关。env 为 nil、变量为空或任何非 "0" 值都开启；仅修剪后精确等于 "0"
// 时关闭（紧急回退）。与 internal/tui.ConfigureApprovalDiff 的语义一致。
func configureApprovalDiff(env func(string) string) {
	nativeApprovalDiff.Store(env == nil || strings.TrimSpace(env("ARGENT_NATIVE_APPROVAL_DIFF")) != "0")
}

// approvalDiffVisible 报告审批卡当前是否允许渲染 diff 摘要。
func approvalDiffVisible() bool { return nativeApprovalDiff.Load() }

const (
	// approvalDiffMaxPreviewLines 约束预览的最大行数（含溢出提示行）。
	approvalDiffMaxPreviewLines = 5
	// approvalDiffMaxPathRunes 限制单行里路径的显示长度（按 rune 计）。
	// 用 []rune 截断确保 CJK/emoji 不会被切成半个字形。
	approvalDiffMaxPathRunes = 32
	// approvalDiffMaxLCSLines 与 tools 侧的 diff 计数同量级：超过该规模时
	// 回退到整文件计数，避免为审批预览分配平方级的表。
	approvalDiffMaxLCSLines = 2000
)

// approvalSummary 是一次文件编辑审批的、面向展示的差异摘要。只承载渲染
// 所需的最小信息；统计与文案生成都是纯函数，可独立测试，不依赖 UI 状态。
type approvalSummary struct {
	LinesAdded   int
	LinesRemoved int
	FilesChanged int
	Preview      string
}

// empty 报告该摘要是否为零值（没有可供渲染的数据）。
func (s approvalSummary) empty() bool {
	return s.FilesChanged == 0 && s.LinesAdded == 0 && s.LinesRemoved == 0 && s.Preview == ""
}

// summaryFileStat 是单个变更文件的统计，用于生成有界预览。
type summaryFileStat struct {
	path           string
	added, removed int
}

// buildApprovalSummary 从工具名与 JSON 参数中提取面向展示的 diff 摘要。
// 只支持变更型文件工具；其余工具（bash/read 等）与解析失败的参数都返回
// 零值摘要，调用方据此跳过渲染，使默认帧保持原样。
//
// 它是纯函数：不修改入参，不读取环境或时钟；预览路径按 rune 截断，
// CJK 文件名不会被切坏，行数始终不超过 approvalDiffMaxPreviewLines。
func buildApprovalSummary(name, args string) approvalSummary {
	switch name {
	case "write", "write_file":
		return summaryFromWrite(args)
	case "edit":
		return summaryFromEdit(args)
	case "multi_edit":
		return summaryFromMultiEdit(args)
	case "apply_patch":
		return summaryFromPatch(args)
	}
	return approvalSummary{}
}

// summaryFromWrite 处理 write 的 {"path":...,"content":...}：
// 内容作为新增行计入，删除行为 0（这里只有目标快照，没有磁盘旧内容）。
func summaryFromWrite(args string) approvalSummary {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return approvalSummary{}
	}
	added, removed := approvalDiffLineCounts("", a.Content)
	return summaryFromStats([]summaryFileStat{{path: a.Path, added: added, removed: removed}})
}

// editFields 是一次编辑的规范化字段。old/new 的别名与 hashline 的 anchor
// 都在这里归一，摘要与逐行 diff 共用，避免两条路径对未来新别名产生分歧。
type editFields struct {
	path string
	old  string
	new  string
}

// parseEditFields 解析单处编辑参数。old/new 缺省时回退到 old_text/new_text，
// old 仍为空时回退到 anchor（hashline 模式下锚点即原始文本）。path 可以为空
// （multi_edit 的单处编辑常省略，由顶层 path 兜底）；新旧都为空时 ok 为 false。
func parseEditFields(raw []byte) (editFields, bool) {
	var a struct {
		Path    string `json:"path"`
		Old     string `json:"old"`
		New     string `json:"new"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
		Anchor  string `json:"anchor"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return editFields{}, false
	}
	old := a.Old
	if old == "" {
		old = a.OldText
	}
	if old == "" {
		old = a.Anchor // hashline 模式下锚点即原始文本
	}
	next := a.New
	if next == "" {
		next = a.NewText
	}
	if old == "" && next == "" {
		return editFields{}, false
	}
	return editFields{path: a.Path, old: old, new: next}, true
}

// parseMultiEditFields 解析 multi_edit 的 edits 数组；每处的 path 为空时
// 回退到顶层 path。空编辑与解析失败项被跳过。
func parseMultiEditFields(args string) []editFields {
	var a struct {
		Path  string            `json:"path"`
		Edits []json.RawMessage `json:"edits"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return nil
	}
	out := make([]editFields, 0, len(a.Edits))
	for _, raw := range a.Edits {
		f, ok := parseEditFields(raw)
		if !ok {
			continue
		}
		if f.path == "" {
			f.path = a.Path
		}
		out = append(out, f)
	}
	return out
}

// summaryFromEdit 处理 edit 的 old/new（含 old_text/new_text 别名与
// hashline 模式的 anchor），用有界 LCS 统计改动行数。
func summaryFromEdit(args string) approvalSummary {
	f, ok := parseEditFields([]byte(args))
	if !ok || f.path == "" {
		return approvalSummary{}
	}
	added, removed := approvalDiffLineCounts(f.old, f.new)
	return summaryFromStats([]summaryFileStat{{path: f.path, added: added, removed: removed}})
}

// summaryFromMultiEdit 处理 multi_edit 的 edits 数组：以每处编辑的 path
// （回退到顶层 path）为键聚合增删行数。edits 为空或解析失败时返回空摘要。
func summaryFromMultiEdit(args string) approvalSummary {
	fields := parseMultiEditFields(args)
	if len(fields) == 0 {
		return approvalSummary{}
	}
	var stats []summaryFileStat
	index := make(map[string]int)
	for _, f := range fields {
		i, ok := index[f.path]
		if !ok {
			i = len(stats)
			index[f.path] = i
			stats = append(stats, summaryFileStat{path: f.path})
		}
		added, removed := approvalDiffLineCounts(f.old, f.new)
		stats[i].added += added
		stats[i].removed += removed
	}
	return summaryFromStats(stats)
}

// summaryFromPatch 处理 apply_patch 的 input（或兼容别名 patch）文本：
// 从 *** Add/Delete/Update File 指令取路径，从 hunk 的 +/- 行取增删计数。
func summaryFromPatch(args string) approvalSummary {
	var a struct {
		Input string `json:"input"`
		Patch string `json:"patch"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return approvalSummary{}
	}
	text := a.Input
	if text == "" {
		text = a.Patch
	}
	if strings.TrimSpace(text) == "" {
		return approvalSummary{}
	}
	var stats []summaryFileStat
	index := make(map[string]int)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		var path string
		switch {
		case strings.HasPrefix(trimmed, "*** Add File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Add File:"))
		case strings.HasPrefix(trimmed, "*** Delete File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Delete File:"))
		case strings.HasPrefix(trimmed, "*** Update File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
		}
		if path != "" {
			if _, ok := index[path]; !ok {
				index[path] = len(stats)
				stats = append(stats, summaryFileStat{path: path})
			}
			continue
		}
		if len(stats) == 0 {
			continue // 还没进入任何文件段
		}
		last := &stats[len(stats)-1]
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			last.added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			last.removed++
		}
	}
	return summaryFromStats(stats)
}

// summaryFromStats 聚合各文件统计并生成有界预览。stats 为空时返回零值。
func summaryFromStats(stats []summaryFileStat) approvalSummary {
	if len(stats) == 0 {
		return approvalSummary{}
	}
	s := approvalSummary{FilesChanged: len(stats)}
	for _, st := range stats {
		s.LinesAdded += st.added
		s.LinesRemoved += st.removed
	}
	s.Preview = buildApprovalPreview(stats)
	return s
}

// buildApprovalPreview 生成有界预览：每个变更文件一行 `+A/-R in path`，
// 行数不超过 approvalDiffMaxPreviewLines，超出时最后一行以省略提示收尾。
// 路径按 rune 截断，CJK 文件名不会被切坏。
func buildApprovalPreview(stats []summaryFileStat) string {
	var b strings.Builder
	for i, st := range stats {
		if i >= approvalDiffMaxPreviewLines {
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == approvalDiffMaxPreviewLines-1 && len(stats) > approvalDiffMaxPreviewLines {
			b.WriteString(tr("… 还有 "))
			b.WriteString(strconv.Itoa(len(stats) - i))
			b.WriteString(tr(" 个文件"))
			break
		}
		path := st.path
		if path == "" {
			path = "?"
		}
		b.WriteByte('+')
		b.WriteString(strconv.Itoa(st.added))
		b.WriteString("/-")
		b.WriteString(strconv.Itoa(st.removed))
		b.WriteString(" in ")
		b.WriteString(truncateRunes(path, approvalDiffMaxPathRunes))
	}
	return b.String()
}

// truncateRunes 按 rune 截断到最多 max 个，保证结果始终是合法 UTF-8，
// 不会切断 CJK 汉字或 emoji 等多字节字形。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// approvalDiffLineCounts 返回两个文本之间的 (新增, 删除) 行数。
// 使用有界 LCS，使未变化的行不被计为编辑；滚动数组把空间降到 O(行数)。
func approvalDiffLineCounts(before, after string) (added, removed int) {
	a := approvalDiffSplitLines(before)
	b := approvalDiffSplitLines(after)
	if len(a) > approvalDiffMaxLCSLines || len(b) > approvalDiffMaxLCSLines ||
		len(a)*len(b) > approvalDiffMaxLCSLines*approvalDiffMaxLCSLines {
		return len(b), len(a)
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for j := 1; j <= len(b); j++ {
		for i := 1; i <= len(a); i++ {
			if a[i-1] == b[j-1] {
				cur[i] = prev[i-1] + 1
			} else if prev[i] >= cur[i-1] {
				cur[i] = prev[i]
			} else {
				cur[i] = cur[i-1]
			}
		}
		prev, cur = cur, prev
	}
	common := prev[len(a)]
	return len(b) - common, len(a) - common
}

// approvalDiffSplitLines 按 LF 切行并规范化 CRLF，去掉末尾换行带来的空行。
func approvalDiffSplitLines(s string) []string {
	text := strings.ReplaceAll(s, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// renderApprovalSummary 生成审批卡上的稳定中文一行摘要。零值摘要返回空串，
// 调用方据此跳过渲染；非零点值始终得到同一输出，不读取时钟或环境。
func renderApprovalSummary(s approvalSummary) string {
	if s.empty() {
		return ""
	}
	return fmt.Sprintf(tr("diff：%d 个文件，+%d/-%d 行"), s.FilesChanged, s.LinesAdded, s.LinesRemoved)
}

// ---- 逐行 diff 数据 ------------------------------------------------------
// 这里只生成纯数据，不碰 UI。main.go 的审批栏按 Kind 上色并补 +/- 标记，
// 复用内建 TUI 的统一 diff 视觉范式；native 仍只依赖 mygo 与标准库。

// approvalDiffLineKind 是逐行 diff 中一行的角色。
type approvalDiffLineKind uint8

const (
	approvalLineContext approvalDiffLineKind = iota
	approvalLineAdd
	approvalLineDel
)

// approvalDiffLine 是逐行 diff 的一行。Text 不带 +/- 标记，标记由渲染器
// 依据 Kind 添加，因此同一份数据既能用于彩色 UI 也能用于纯文本输出。
type approvalDiffLine struct {
	Kind approvalDiffLineKind
	Text string
}

// Sign 返回统一 diff 中该行的单字符标记。
func (l approvalDiffLine) Sign() string {
	switch l.Kind {
	case approvalLineAdd:
		return "+"
	case approvalLineDel:
		return "-"
	default:
		return " "
	}
}

// approvalDiffFile 是一个文件的逐行 diff 块。
type approvalDiffFile struct {
	Path  string
	Lines []approvalDiffLine
}

const (
	// approvalLineDiffMaxLines 限制单个文件块的行数，超出的行以省略行收尾。
	approvalLineDiffMaxLines = 40
	// approvalLineDiffMaxRunes 限制单行显示宽度（按 rune 计，CJK 不切坏）。
	approvalLineDiffMaxRunes = 120
	// approvalLineDiffMaxLCSCells 限制 LCS 表大小；超过时降级为整段删除+新增。
	approvalLineDiffMaxLCSCells = 64 * 1024
)

// buildApprovalLineDiff 从工具名与 JSON 参数生成有界的逐行 diff 数据。
// 只支持变更型文件工具；非编辑工具与解析失败返回 nil，调用方据此跳过渲染。
// 它是纯函数，不读环境或时钟，输出顺序稳定。
func buildApprovalLineDiff(name, args string) []approvalDiffFile {
	switch name {
	case "write", "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
			return nil
		}
		return []approvalDiffFile{{Path: a.Path, Lines: boundedLineDiff("", a.Content)}}
	case "edit":
		f, ok := parseEditFields([]byte(args))
		if !ok || f.path == "" {
			return nil
		}
		return []approvalDiffFile{{Path: f.path, Lines: boundedLineDiff(f.old, f.new)}}
	case "multi_edit":
		fields := parseMultiEditFields(args)
		if len(fields) == 0 {
			return nil
		}
		var out []approvalDiffFile
		index := make(map[string]int)
		for _, f := range fields {
			i, ok := index[f.path]
			if !ok {
				i = len(out)
				index[f.path] = i
				out = append(out, approvalDiffFile{Path: f.path})
			}
			out[i].Lines = appendBoundedLines(out[i].Lines, boundedLineDiff(f.old, f.new))
		}
		return out
	case "apply_patch":
		var a struct {
			Input string `json:"input"`
			Patch string `json:"patch"`
		}
		if json.Unmarshal([]byte(args), &a) != nil {
			return nil
		}
		text := a.Input
		if text == "" {
			text = a.Patch
		}
		return lineDiffFromPatch(text)
	}
	return nil
}

// boundedLineDiff 生成一段文本的上下文/新增/删除行差异并施加显示上界。
func boundedLineDiff(before, after string) []approvalDiffLine {
	return capDiffLines(approvalLineDiff(before, after))
}

// approvalLineDiff 返回 before/after 的逐行差异。先修剪相同前后缀，再对
// 中间区域运行有界 LCS；超过上限时降级为整段删除+新增，保证不会分配
// 无界的表。与 internal/tui.DiffLines 的语义一致。
func approvalLineDiff(before, after string) []approvalDiffLine {
	oldLines, newLines := approvalDiffSplitLines(before), approvalDiffSplitLines(after)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	out := make([]approvalDiffLine, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines[:prefix] {
		out = append(out, approvalDiffLine{Kind: approvalLineContext, Text: line})
	}
	out = append(out, approvalLineDiffMiddle(oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix])...)
	for _, line := range oldLines[len(oldLines)-suffix:] {
		out = append(out, approvalDiffLine{Kind: approvalLineContext, Text: line})
	}
	return out
}

// approvalLineDiffMiddle 对齐变化区域；超过 LCS 上限时降级为 del+add。
func approvalLineDiffMiddle(oldLines, newLines []string) []approvalDiffLine {
	n, m := len(oldLines), len(newLines)
	out := make([]approvalDiffLine, 0, n+m)
	if n == 0 {
		for _, line := range newLines {
			out = append(out, approvalDiffLine{Kind: approvalLineAdd, Text: line})
		}
		return out
	}
	if m == 0 {
		for _, line := range oldLines {
			out = append(out, approvalDiffLine{Kind: approvalLineDel, Text: line})
		}
		return out
	}
	if n*m > approvalLineDiffMaxLCSCells {
		for _, line := range oldLines {
			out = append(out, approvalDiffLine{Kind: approvalLineDel, Text: line})
		}
		for _, line := range newLines {
			out = append(out, approvalDiffLine{Kind: approvalLineAdd, Text: line})
		}
		return out
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, approvalDiffLine{Kind: approvalLineContext, Text: oldLines[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, approvalDiffLine{Kind: approvalLineDel, Text: oldLines[i]})
			i++
		default:
			out = append(out, approvalDiffLine{Kind: approvalLineAdd, Text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, approvalDiffLine{Kind: approvalLineDel, Text: oldLines[i]})
	}
	for ; j < m; j++ {
		out = append(out, approvalDiffLine{Kind: approvalLineAdd, Text: newLines[j]})
	}
	return out
}

// lineDiffFromPatch 把 apply_patch 文本映射为按文件分组的逐行 diff。补丁的
// +/- 行直接作为增删行，@@/*** 等元信息行跳过；*** Update File 指令用作块标题。
func lineDiffFromPatch(text string) []approvalDiffFile {
	var out []approvalDiffFile
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		var path string
		switch {
		case strings.HasPrefix(trimmed, "*** Add File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Add File:"))
		case strings.HasPrefix(trimmed, "*** Delete File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Delete File:"))
		case strings.HasPrefix(trimmed, "*** Update File:"):
			path = strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Update File:"))
		}
		if path != "" {
			out = append(out, approvalDiffFile{Path: path})
			continue
		}
		if len(out) == 0 || strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "***") {
			continue
		}
		last := &out[len(out)-1]
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			last.Lines = append(last.Lines, approvalDiffLine{Kind: approvalLineAdd, Text: line[1:]})
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			last.Lines = append(last.Lines, approvalDiffLine{Kind: approvalLineDel, Text: line[1:]})
		case strings.HasPrefix(line, " "):
			last.Lines = append(last.Lines, approvalDiffLine{Kind: approvalLineContext, Text: line[1:]})
		}
	}
	for i := range out {
		out[i].Lines = capDiffLines(out[i].Lines)
	}
	return out
}

// capDiffLines 把行数限制在 approvalLineDiffMaxLines 内并逐行截断显示宽度。
// 超出的行用一条省略提示行收尾，保证审批卡不会撑满屏幕。
func capDiffLines(lines []approvalDiffLine) []approvalDiffLine {
	for i := range lines {
		lines[i].Text = truncateRunes(lines[i].Text, approvalLineDiffMaxRunes)
	}
	if len(lines) <= approvalLineDiffMaxLines {
		return lines
	}
	kept := append([]approvalDiffLine(nil), lines[:approvalLineDiffMaxLines-1]...)
	return append(kept, approvalDiffLine{Kind: approvalLineContext, Text: tr("… 已省略更多行")})
}

// appendBoundedLines 把多处以同一文件为目标的 diff 行拼接起来，仍受上界约束。
func appendBoundedLines(dst, more []approvalDiffLine) []approvalDiffLine {
	dst = append(dst, more...)
	return capDiffLines(dst)
}

// renderApprovalLineDiff 把逐行 diff 渲染为稳定的纯文本，供日志与测试使用；
// UI 渲染由 main.go 依据 Kind 上色，不调用此函数。
func renderApprovalLineDiff(files []approvalDiffFile) string {
	var b strings.Builder
	for fi, f := range files {
		if fi > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f.Path)
		for _, line := range f.Lines {
			b.WriteByte('\n')
			b.WriteString(line.Sign())
			b.WriteString(line.Text)
		}
	}
	return b.String()
}
