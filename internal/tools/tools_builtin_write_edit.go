package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---- write ----

// writeTool 在可选格式化钩子启用时，先把请求内容交给钩子，再把最终字节落盘，
// 并在结果文案中附上基于最终内容计算的差异摘要。未启用钩子时行为与原来一致。
type writeTool struct {
	format FormatHook
}

func (writeTool) Name() string        { return "write" }
func (writeTool) Description() string { return "Create or overwrite a file with the given content." }
func (writeTool) Mutating() bool      { return true }
func (writeTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`
}

func (t writeTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("write: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	unlock := lockFileMutation(path)
	defer unlock()
	// 读前写新鲜度（opt-in，由 app 注入账本）：外部改动后拒绝基于陈旧快照的写入。
	if f := FileFreshnessFrom(ctx); f != nil {
		if warning := f.StaleWarning(path); warning != "" {
			return "", fmt.Errorf("write: %s", warning)
		}
	}
	before, readErr := os.ReadFile(path)
	// 生成文件保护（opt-in ARGENT_BLOCK_GENERATED_EDITS，默认关闭）。
	if err := checkGeneratedFile(path, before); err != nil {
		return "", err
	}
	after := []byte(args.Content)
	// 重写相同的字节是空操作：跳过系统调用，使文件的
	// modTime 保持不变（也避免不必要的监视器抖动）。
	// 缺失或不可读的目标会落到普通的创建/写入流程。
	if readErr == nil && bytes.Equal(before, after) {
		return fmt.Sprintf("%s already contains the requested content; no write needed", args.Path), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	result := fmt.Sprintf("wrote %d bytes to %s", len(after), args.Path)
	final, result := t.formatOutput(path, before, after, result)
	// 写入前 Go 语法劣化检查（opt-in ARGENT_PARSE_REGRESSION_GUARD，默认关闭）。
	if err := checkParseRegression(path, before, final); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, final, 0o644); err != nil {
		return "", err
	}
	if warning := verifyWriteDrift(path, final); warning != "" {
		result += "\n" + warning
	}
	if f := FileFreshnessFrom(ctx); f != nil {
		f.MarkWrite(path)
	}
	return result, nil
}

// formatOutput 在写入前应用可选的格式化钩子，返回真正要落盘的字节与结果文案。
// diskPath 供钩子定位文件；result 已经用模型传入的路径构建完成。
// 未配置钩子时原样返回，确保默认路径逐字节兼容。
func (t writeTool) formatOutput(diskPath string, before, after []byte, result string) ([]byte, string) {
	if t.format == nil {
		return after, result
	}
	formatted := t.format(diskPath, after)
	summary := formatAwareDiff(string(before), string(after), string(formatted))
	if len(formatted) > 0 && !bytes.Equal(formatted, after) {
		return formatted, result + " (" + summary + ")"
	}
	return after, result + " (" + summary + ")"
}

// ---- edit ----

// editTool 在可选格式化钩子启用时，先把编辑结果交给钩子，再把最终字节落盘，
// 并在结果文案中附上基于最终内容计算的差异摘要。未启用钩子时行为与原来一致。
type editTool struct {
	format FormatHook
}

func (editTool) Name() string { return "edit" }
func (editTool) Description() string {
	return "Replace one unique occurrence of old text with new text."
}
func (editTool) Mutating() bool { return true }
func (editTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"},"repair":{"type":"boolean","description":"opt-in graded repair ladder for missing or non-literal matches"},"anchor":{"type":"string","description":"exact original text the model saw"},"anchor_hash":{"type":"string","description":"first 12 hex chars of sha256(anchor bytes)"},"hashline":{"type":"boolean","description":"opt-in hashline anchoring with stale-anchor recovery"}},"required":["path"]}`
}

func (t editTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path       string `json:"path"`
		Old        string `json:"old"`
		New        string `json:"new"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		Repair     bool   `json:"repair"`
		Hashline   bool   `json:"hashline"`
		Anchor     string `json:"anchor"`
		AnchorHash string `json:"anchor_hash"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("edit: bad arguments: %w", err)
	}
	if args.Old == "" {
		args.Old = args.OldText
	}
	if args.New == "" {
		args.New = args.NewText
	}
	// 在 hashline 模式下，锚点是权威的原始文本，因此它可以
	// 代替 old。这由 Hashline 控制，使功能关闭时默认路径
	// 保持逐字节不变。
	if args.Hashline && args.Old == "" && args.Anchor != "" {
		args.Old = args.Anchor
	}
	if args.Old == "" {
		return "", fmt.Errorf("edit: old must not be empty")
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	unlock := lockFileMutation(path)
	defer unlock()
	// 读前写新鲜度（opt-in，由 app 注入账本）：外部改动后拒绝基于陈旧快照的编辑。
	if f := FileFreshnessFrom(ctx); f != nil {
		if warning := f.StaleWarning(path); warning != "" {
			return "", fmt.Errorf("edit: %s", warning)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// 生成文件保护（opt-in ARGENT_BLOCK_GENERATED_EDITS，默认关闭）。
	if err := checkGeneratedFile(path, data); err != nil {
		return "", err
	}
	text := string(data)

	var out string
	// opt-in 的 hashline 锚定。它只改变 old -> new 的解析方式；
	// 路径解析、变更锁和下面的写入都不变。
	switch {
	case args.Hashline && (args.Anchor != "" || args.AnchorHash != ""):
		out, err = resolveHashlineEdit(text, args.Path, args.Anchor, args.AnchorHash, args.Old, args.New)
		if err != nil {
			return "", err
		}
	case args.Repair:
		var ok bool
		switch strings.Count(text, args.Old) {
		case 1:
			out, ok = strings.Replace(text, args.Old, args.New, 1), true
		case 0:
			out, ok = repairReplace(text, args.Old, args.New)
		default:
			// 已出现多次的字面字符串具有歧义；
			// 阶梯中的任何一级都不能替我们选中其中某个出现位置。
			ok = false
		}
		if !ok {
			err := editRepairError(text, args.Old)
			if replaceAlreadyPresent(text, args.New) {
				// 替换文本已在文件里：模型很可能重复发送了同一个编辑。
				// 追加提示，避免它继续触发模糊修复并做无效重试。
				err = fmt.Errorf("%w; %s", err, alreadyAppliedHint(args.Path))
			}
			return "", err
		}
	default:
		count := strings.Count(text, args.Old)
		switch count {
		case 0:
			return "", fmt.Errorf("edit: old text not found in %s", args.Path)
		case 1:
			out = strings.Replace(text, args.Old, args.New, 1)
		default:
			// 计数短语与冻结的 Rust 构建的工具错误一致，
			// eval 套件正是对此断言；其余部分解释模型必须做什么。
			return "", fmt.Errorf("edit: old text appears %d times in %s (not unique); a unique match is required", count, args.Path)
		}
	}

	final, result := t.formatOutput(path, args.Path, data, []byte(out))
	// 写入前 Go 语法劣化检查（opt-in ARGENT_PARSE_REGRESSION_GUARD，默认关闭）。
	if err := checkParseRegression(path, data, final); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, final, 0o644); err != nil {
		return "", err
	}
	if warning := verifyWriteDrift(path, final); warning != "" {
		result += "\n" + warning
	}
	if f := FileFreshnessFrom(ctx); f != nil {
		f.MarkWrite(path)
	}
	return result, nil
}

// formatOutput 在写入前应用可选的格式化钩子，返回真正要落盘的字节与结果文案。
// diskPath 供钩子定位文件，displayPath 是模型传入的路径（用于结果文案）。
// 未配置钩子时原样返回，确保默认路径逐字节兼容。
func (t editTool) formatOutput(diskPath, displayPath string, before, after []byte) ([]byte, string) {
	result := "edited " + displayPath
	if t.format == nil {
		return after, result
	}
	formatted := t.format(diskPath, after)
	summary := formatAwareDiff(string(before), string(after), string(formatted))
	if len(formatted) > 0 && !bytes.Equal(formatted, after) {
		return formatted, result + " (" + summary + ")"
	}
	return after, result + " (" + summary + ")"
}

// hashlineHash 返回 s 的精确字节的 SHA-256 摘要的
// 前 12 个十六进制字符。模型必须复现的契约是：取它看到的原文
// （逐字节，包括换行），对这些字节做 SHA-256，将
// 32 字节摘要十六进制编码，并保留前 12 个小写十六进制字符。
func hashlineHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// resolveHashlineEdit 解析一种 opt-in 的、内容哈希锚定的编辑，返回
// 文件的完整替换内容（写入由调用方负责）。
//
// 解析顺序：
//  1. 如果提供了 anchor_hash，它必须等于 hashlineHash(anchor)。不匹配
//     是可恢复的"锚点过期"错误：文件在读取后发生了变化。
//  2. 锚点的唯一字面出现位置立即胜出。行号偏移
//     在这里无关紧要，因为匹配基于内容而非行号。
//  3. 如果锚点不存在，但其哈希已验证（或未提供哈希），
//     则重试分级修复阶梯以恢复略有偏移的内容。
//  4. 否则，"已不存在"错误会列出最相似的行。
func resolveHashlineEdit(content, path, anchor, anchorHash, old, newText string) (string, error) {
	if anchorHash != "" {
		want := hashlineHash(anchor)
		if !strings.EqualFold(anchorHash, want) {
			return "", fmt.Errorf("edit: stale anchor: content changed since it was read (expected hash %s, got %s)", want, anchorHash)
		}
	}

	// 锚点是权威的原始文本；当模型省略 anchor 时回退到 old
	// （调用方仍只在设置了锚点时才路由到这里）。
	oldText := anchor
	if oldText == "" {
		oldText = old
	}

	count := strings.Count(content, oldText)
	switch count {
	case 1:
		return strings.Replace(content, oldText, newText, 1), nil
	case 0:
		// 未找到锚点：尝试修复阶梯以恢复过期锚点。
	default:
		return "", fmt.Errorf("edit: stale anchor appears %d times in %s (not unique); file unchanged", count, path)
	}

	if out, ok := repairReplace(content, oldText, newText); ok {
		return out, nil
	}

	msg := "edit: stale anchor no longer present in " + path + "; file unchanged"
	if s := formatSimilarLines(content, oldText); s != "" {
		msg += "; " + s
	}
	return "", errors.New(msg)
}

// repairReplace 依次遍历分级的 Aider 风格修复阶梯：
//  1. 精确字面匹配（仅限唯一者 —— 绝不猜测有歧义的情况）；
//  2. 仅尾随空白不匹配，逐行进行（保留原始字节）；
//  3. 仅前导空白不匹配，把文件的缩进重新应用到 new；
//  4. old（及 new）中单个多余的前导/尾随空行；
//  5. "..." 省略，每个非空片段都在内容中唯一锚定；
//  6. 标点归一化（弯引号、破折号、NBSP/全角空格）
//     只应用于双方以作比较；匹配到的原始字节
//     被拼接移除，new 按原样插入。
//
// 它是纯函数：无 I/O、无锁、无策略检查。文件由调用方负责。
// 布尔值报告是否有某级产生了可用的替换，以便
// 所有级别都失败时调用方能构建诊断。
func repairReplace(content, old, new string) (string, bool) {
	switch strings.Count(content, old) {
	case 1:
		return strings.Replace(content, old, new, 1), true
	case 0:
		// 继续落到模糊级别
	default:
		return "", false // ambiguous exact match: refuse to guess
	}
	if out, ok := repairTrailingWhitespace(content, old, new); ok {
		return out, true
	}
	if out, ok := repairIndent(content, old, new); ok {
		return out, true
	}
	if out, ok := repairBlankLine(content, old, new); ok {
		return out, true
	}
	if out, ok := repairElision(content, old, new); ok {
		return out, true
	}
	if out, ok := repairPunctuation(content, old, new); ok {
		return out, true
	}
	return "", false
}

// repairTrailingWhitespace 在丢弃 old 每一行以及 content 每个等宽窗口中
// 的尾部水平空白后再进行匹配。
// 这覆盖了模型回显某个块时带多余（或
// 缺失）尾随空格/制表符的常见情况。只忽略尾随空白；字节
// 偏移基于原始内容计算，使匹配行的
// 原始字节 —— 包括其行尾 —— 在拼接之外保持
// 完好。有歧义的匹配被拒绝（与精确字面级别一致）。
func repairTrailingWhitespace(content, old, new string) (string, bool) {
	const trailing = " \t\r" // ASCII space/tab plus CR (for CRLF files); never NBSP/full-width, those belong to the punctuation rung
	oldLines := strings.Split(old, "\n")
	oldNorm := make([]string, len(oldLines))
	for i, ln := range oldLines {
		oldNorm[i] = strings.TrimRight(ln, trailing)
	}
	width := len(oldLines)

	contentLines := strings.Split(content, "\n")
	if len(contentLines) < width {
		return "", false
	}

	start := -1
	for i := 0; i+width <= len(contentLines); i++ {
		ok := true
		for k := 0; k < width; k++ {
			if strings.TrimRight(contentLines[i+k], trailing) != oldNorm[k] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if start >= 0 {
			return "", false // two candidate windows: ambiguous
		}
		start = i
	}
	if start < 0 {
		return "", false
	}

	// 首个匹配行的字节偏移，以及最后匹配
	// 行原始字节的结束位置。strings.Split 移除了 "\n" 分隔符，因此
	// 每个都要补回一个字节；最后一行尾随的 "\n"（如有）留在
	// content[endByte:] 中，逐字节保留原始行尾。
	startByte := 0
	for i := 0; i < start; i++ {
		startByte += len(contentLines[i]) + 1
	}
	last := start + width - 1
	endByte := startByte
	for i := start; i <= last; i++ {
		endByte += len(contentLines[i])
		if i < last {
			endByte++ // the "\n" between matched lines
		}
	}
	return content[:startByte] + new + content[endByte:], true
}

// commonIndent 返回每个非空行共享的最长前导空白前缀。
// 空行被忽略，以免某一行破坏该前缀。
func commonIndent(lines []string) string {
	indent := ""
	seen := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lead := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		if !seen {
			indent, seen = lead, true
			continue
		}
		i := 0
		for i < len(indent) && i < len(lead) && indent[i] == lead[i] {
			i++
		}
		indent = indent[:i]
	}
	return indent
}

func stripIndent(lines []string, indent string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			out[i] = "" // blank lines carry no usable indentation
			continue
		}
		out[i] = strings.TrimPrefix(ln, indent)
	}
	return out
}

func indentLines(lines []string, indent string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		if ln == "" {
			out[i] = "" // avoid trailing whitespace on blank lines
		} else {
			out[i] = indent + ln
		}
	}
	return out
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// repairIndent 在移除双方共同的前导缩进后匹配块，
// 然后把文件中找到的缩进重新应用到新文本。
func repairIndent(content, old, new string) (string, bool) {
	oldLines := strings.Split(old, "\n")
	strippedOld := stripIndent(oldLines, commonIndent(oldLines))
	newLines := strings.Split(new, "\n")
	strippedNew := stripIndent(newLines, commonIndent(newLines))

	contentLines := strings.Split(content, "\n")
	width := len(strippedOld)
	if width == 0 || len(contentLines) < width {
		return "", false
	}

	start := -1
	fileIndent := ""
	for i := 0; i+width <= len(contentLines); i++ {
		window := contentLines[i : i+width]
		indent := commonIndent(window)
		if equalLines(stripIndent(window, indent), strippedOld) {
			if start >= 0 {
				return "", false // two candidate blocks: ambiguous
			}
			start, fileIndent = i, indent
		}
	}
	if start < 0 {
		return "", false
	}

	repl := indentLines(strippedNew, fileIndent)
	out := make([]string, 0, len(contentLines)-width+len(repl))
	out = append(out, contentLines[:start]...)
	out = append(out, repl...)
	out = append(out, contentLines[start+width:]...)
	return strings.Join(out, "\n"), true
}

// repairBlankLine 用移除 old（以及 new 中若存在）中一个
// 多余空行（前导或尾随）的方式重试字面匹配。
func repairBlankLine(content, old, new string) (string, bool) {
	type pair struct{ old, new string }
	var pairs []pair
	if v, ok := dropLeadingBlankLine(old); ok {
		nv := new
		if x, ok2 := dropLeadingBlankLine(new); ok2 {
			nv = x
		}
		pairs = append(pairs, pair{v, nv})
	}
	if v, ok := dropTrailingBlankLine(old); ok {
		nv := new
		if x, ok2 := dropTrailingBlankLine(new); ok2 {
			nv = x
		}
		pairs = append(pairs, pair{v, nv})
	}
	for _, p := range pairs {
		if strings.Count(content, p.old) == 1 {
			return strings.Replace(content, p.old, p.new, 1), true
		}
	}
	return "", false
}

func dropLeadingBlankLine(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && strings.TrimSpace(lines[0]) == "" {
		return strings.Join(lines[1:], "\n"), true
	}
	return s, false
}

func dropTrailingBlankLine(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	last := len(lines) - 1
	if last > 0 && strings.TrimSpace(lines[last]) == "" {
		return strings.Join(lines[:last], "\n"), true
	}
	return s, false
}

// splitElision 按内容恰为 "..." 的行（TrimSpace 后）切分 s，
// 并报告是否至少存在一个标记。
func splitElision(s string) ([]string, bool) {
	lines := strings.Split(s, "\n")
	pieces := []string{}
	cur := []string{}
	markers := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "..." {
			pieces = append(pieces, strings.Join(cur, "\n"))
			cur = cur[:0]
			markers++
			continue
		}
		cur = append(cur, ln)
	}
	pieces = append(pieces, strings.Join(cur, "\n"))
	return pieces, markers > 0
}

// repairElision 把 old 的每个非空片段在 content 中恰好锚定一次，
// 然后在每个锚点处拼接对应的新片段，同时保留
// 锚点之间的文件文本（被省略的中间部分）。
func repairElision(content, old, new string) (string, bool) {
	oldPieces, hasOldMarkers := splitElision(old)
	newPieces, hasNewMarkers := splitElision(new)
	if !hasOldMarkers || !hasNewMarkers || len(oldPieces) != len(newPieces) {
		return "", false
	}

	var b strings.Builder
	cursor := 0
	for i, piece := range oldPieces {
		if strings.TrimSpace(piece) == "" {
			// 没有可固定此片段的锚点；在当前位置追加它。
			b.WriteString(newPieces[i])
			continue
		}
		if strings.Count(content, piece) != 1 {
			return "", false
		}
		rel := strings.Index(content[cursor:], piece)
		if rel < 0 {
			return "", false // pieces are out of order in the file
		}
		at := cursor + rel
		b.WriteString(content[cursor:at])
		b.WriteString(newPieces[i])
		cursor = at + len(piece)
	}
	b.WriteString(content[cursor:])
	return b.String(), true
}

// normalizePunctRune 把模型输出的常见排版标点映射到
// 其 ASCII 对应物。该映射是纯的逐 rune 替换：它从不
// 扩展或丢弃 rune，因此归一化后的序列与输入具有相同的 rune 长度。
// 这使比较具有确定性，并让调用方在扫描时跟踪
// 原始字节偏移，而无需触碰原始内容。
func normalizePunctRune(r rune) rune {
	switch r {
	case '\u2018', '\u2019', '\u201a', '\u201b': // single curly / low-high quotes
		return '\''
	case '\u201c', '\u201d', '\u201e', '\u201f': // double curly / low-high quotes
		return '"'
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212': // hyphen, non-breaking hyphen, figure dash, en/em dash, horizontal bar, minus sign
		return '-'
	case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000': // no-break, en..hair, narrow no-break, medium math, ideographic spaces
		return ' '
	default:
		return r
	}
}

// repairPunctuation 在把 old 和 content 的每个等长窗口都通过
// normalizePunctRune 归一化后重试匹配。归一化
// 只用于比较：一旦找到唯一窗口，文件其余部分的原始字节
// 会被逐字保留，new 按给定内容插入。
// 仅当恰好一个窗口符合条件时才接受匹配；有歧义时
// 绝不猜测，与精确字面级别一致。
func repairPunctuation(content, old, new string) (string, bool) {
	oldRunes := []rune(old)
	if len(oldRunes) == 0 {
		return "", false
	}
	normOld := make([]rune, len(oldRunes))
	for i, r := range oldRunes {
		normOld[i] = normalizePunctRune(r)
	}
	width := len(normOld)

	// 把 content 一次解码为带有字节偏移的 rune 对。range 一个
	// 字符串会得到 rune 起始偏移，因此在这些偏移处切片
	// 始终位于有效的 UTF-8 边界（无效字节解码为 RuneError，但
	// 仍按一个字节前进）。
	type positioned struct {
		r   rune
		off int
	}
	rs := make([]positioned, 0, len(content))
	for off, r := range content {
		rs = append(rs, positioned{r, off})
	}

	startRune := -1
	matches := 0
	for i := 0; i+width <= len(rs); i++ {
		ok := true
		for k := 0; k < width; k++ {
			if normalizePunctRune(rs[i+k].r) != normOld[k] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		matches++
		if matches > 1 {
			return "", false // ambiguous normalized match: refuse to guess
		}
		startRune = i
	}
	if matches == 0 {
		return "", false
	}

	startByte := rs[startRune].off
	endByte := len(content)
	if startRune+width < len(rs) {
		endByte = rs[startRune+width].off
	}
	return content[:startByte] + new + content[endByte:], true
}

// similarLines 返回 content 中与 sample 共享最多
// 词/rune 的最多 limit 个非空行。平局时保持文件顺序。它只使用 strings、
// rune 和 sort，使诊断保持无依赖且确定。
func similarLines(content, sample string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	type scored struct {
		line  string
		score int
	}
	var candidates []scored
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if score := similarityScore(sample, ln); score > 0 {
			candidates = append(candidates, scored{ln, score})
		}
	}
	if len(candidates) == 0 {
		// 没有任何共享标记：仍展示几行真实内容作为上下文。
		for _, ln := range strings.Split(content, "\n") {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			candidates = append(candidates, scored{ln, 0})
			if len(candidates) == limit {
				break
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.line)
	}
	return out
}

func similarityScore(sample, line string) int {
	a, b := strings.ToLower(sample), strings.ToLower(line)
	return commonMultiset(strings.Fields(a), strings.Fields(b))*8 + commonRunes([]rune(a), []rune(b))
}

func commonMultiset(a, b []string) int {
	counts := make(map[string]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	n := 0
	for _, v := range b {
		if counts[v] > 0 {
			counts[v]--
			n++
		}
	}
	return n
}

func commonRunes(a, b []rune) int {
	counts := make(map[rune]int, len(a))
	for _, r := range a {
		counts[r]++
	}
	n := 0
	for _, r := range b {
		if counts[r] > 0 {
			counts[r]--
			n++
		}
	}
	return n
}

func formatSimilarLines(content, sample string) string {
	lines := similarLines(content, sample, 5)
	if len(lines) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(lines))
	for _, ln := range lines {
		quoted = append(quoted, strconv.Quote(ln))
	}
	return "most similar lines: " + strings.Join(quoted, " | ")
}

func editRepairError(content, old string) error {
	msg := "edit repair failed: no unique match after 1 edit(s) attempted; file unchanged"
	if s := formatSimilarLines(content, old); s != "" {
		msg += "; " + s
	}
	return errors.New(msg)
}

// replaceAlreadyPresent 判断 newText 是否已经出现在 content 中。
//
// 这是给失败诊断用的纯函数：模型重复发送同一编辑时，old 已被移除而 new
// 往往还留在文件里。先用 strings.Contains 做精确匹配，再做一次空白归一化
// 比较，以覆盖缩进/换行被改动但内容实质相同的情况。空文本必须返回 false，
// 否则 strings.Contains 会把任意文件都判成“已应用”，造成误判。
func replaceAlreadyPresent(content, newText string) bool {
	if newText == "" {
		return false
	}
	// 精确匹配优先：避免归一化把不相关的空白差异误判为命中。
	if strings.Contains(content, newText) {
		return true
	}
	// strings.Fields 按 unicode.IsSpace 分割，天然支持 CJK 文本与全角空格；
	// 折叠成单个半角空格后，换行与缩进差异不再影响比较。
	normNew := strings.Join(strings.Fields(newText), " ")
	if normNew == "" {
		return false
	}
	normContent := strings.Join(strings.Fields(content), " ")
	return strings.Contains(normContent, normNew)
}

// alreadyAppliedHint 生成“替换文本已存在”的诊断文案。
//
// 文案沿用本文件其他工具错误的既有风格（英文、以工具名开头），注释保持中文。
// 调用方负责把它追加到既有诊断之后，不要替换原文案。
func alreadyAppliedHint(path string) string {
	return "edit: REPLACE text is already present in " + path +
		"; this edit may have already been applied; skip it and verify the file before retrying"
}

// multiEditRepairError 为失败的 multi_edit 批次构建保持原子性的诊断。
// multi_edit 只在最后写一次文件，因此失败的批次
// 会让文件保持原样：指令必须始终告诉
// 模型重新发送整个批次，且绝不能建议跳过那些
// 仅在内存中匹配的编辑。保持此措辞与
// multi_edit_plan.go 中的 multiEditBatchDiagnostic(..., atomic=true) 同步；两者都由
// 各自测试逐字节断言。
func multiEditRepairError(path string, failedIdx, total, applied int, content, sample string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "multi_edit: repair failed at edit %d of %d after %d edit(s) attempted in %s; file unchanged (atomic batch)", failedIdx, total, total, path)
	if applied == 0 {
		b.WriteString("; no edit matched; re-send the whole batch with the failing edit corrected")
	} else {
		fmt.Fprintf(&b, "; %d edit(s) matched cleanly in memory but no bytes were written (atomic batch); re-send the whole batch with the failing edit corrected", applied)
	}
	if s := formatSimilarLines(content, sample); s != "" {
		b.WriteString("; ")
		b.WriteString(s)
	}
	return errors.New(b.String())
}
