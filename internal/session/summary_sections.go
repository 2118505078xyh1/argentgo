package session

import "strings"

// preservedSectionMaxRunes 是合并回重建摘要时单段的最大 rune 数。多轮压缩中早期
// Goal / Tool activity 会像滚雪球一样永久保留，因此合并前先有界裁剪：新历史优先，
// 旧章节只保留开头一部分，超出部分用一个有界标记替代（CJK 安全，按 rune 切）。
const preservedSectionMaxRunes = 2048

// legacySectionMarkers 是早期模板留下的格式化标记。命中时按新详略级别重写一次，
// 把逐文件 changelog / 复述的 AGENTS.md 等过时细节折叠掉，避免它们跨多轮永久携带。
var legacySectionMarkers = []string{
	"### ",           // 早期按文件/按次 edit 拆出的三级标题
	"AGENTS.md",      // 复述的规则文件内容
	"CLAUDE.md",      // 同上
	"[checkpoint",    // 逐文件快照标记
	"Checkpoint:",    // 同上（英文写法）
	"## Changelog",   // 逐次编辑流水
	"changelog:",     // 同上
	"edit #",         // 逐次 edit 计数
	"2024-", "2025-", // 早期带日期的流水行
}

const (
	legacySectionKeepRunes = 512 // 重写后保留的正文上限
	legacySectionKeepLines = 8   // 重写后保留的正文行数上限
	legacySectionNotice    = "\n- [旧章节已收敛：新历史优先，过时细节已折叠]\n"
	trimmedSectionNotice   = "\n- [章节已按 2048 rune 上限裁剪]\n"
)

// SummarySections 是从压缩摘要中解析出的两个易丢失章节：目标（Goal）与工具活动（Tool activity）。
//
// Goal / ToolActivity 保存完整的章节块——含 "## ..." 标题行、其后直到下一个标题行之前的
// 全部内容以及结尾换行——因此可以被 PreserveSections 原样写回。HasGoal / HasActivity
// 指示原摘要中是否出现了对应章节（出现空章节也算存在）。
//
// 本结构只描述解析结果。ParseSummarySections 与 PreserveSections 都是纯函数：
// 不改动入参，不读取或修改全局状态，结果只取决于入参。
type SummarySections struct {
	Goal         string
	ToolActivity string
	HasGoal      bool
	HasActivity  bool
}

// 两个章节的规范标题，与 Summarize / buildCompactionSummary 写入的标题一致。
const (
	summaryGoalTitle     = "Goal"
	summaryActivityTitle = "Tool activity"
)

// ParseSummarySections 从 Summarize 的原始摘要文本中按 "## Goal" / "## Tool activity"
// 标题边界提取两段。章节范围从标题行起，到下一个 markdown 标题行（或文本结尾）止。
//
// 文本按行切分而非按字节切片，因此 CJK 内容不会被截断；标题匹配忽略大小写与首尾
// 空白/冒号，并接受常用中文别名（"目标"、"工具活动" 等）。同名章节出现多次时只取第一处。
// 本函数是纯函数，不改动 summary。
func ParseSummarySections(summary string) SummarySections {
	lines := strings.Split(summary, "\n")
	var out SummarySections
	for i := 0; i < len(lines); i++ {
		title, ok := summarySectionTitle(lines[i])
		if !ok {
			continue
		}
		end := i + 1
		for end < len(lines) && !summaryIsHeading(lines[end]) {
			end++
		}
		switch {
		case isSummaryGoalTitle(title) && !out.HasGoal:
			out.HasGoal = true
			out.Goal = summarySectionBlock(lines, i, end)
		case isSummaryActivityTitle(title) && !out.HasActivity:
			out.HasActivity = true
			out.ToolActivity = summarySectionBlock(lines, i, end)
		}
		// 整段已处理，直接跳到下一个标题行；节内出现的 "- ## Foo" 是项目符号，不会被当成标题。
		i = end - 1
	}
	return out
}

// PreserveSections 把 orig 中的 Goal / Tool activity 两段原样合并回 rebuilt。
//
// buildCompactionSummary 硬编码 Goal=(none) 且完全不写 Tool activity，因此当 rebuilt
// 已有同名章节时用 orig 的整段替换，缺失时则追加到末尾。两段都缺失时按 Goal、Tool activity
// 的固定顺序处理，与它们在 orig 中的先后顺序无关。orig 不含这两段时原样返回 rebuilt。
//
// 本函数是纯函数且幂等：对结果再次调用得到相同字符串。
func PreserveSections(rebuilt, orig string) string {
	origSections := ParseSummarySections(orig)
	if !origSections.HasGoal && !origSections.HasActivity {
		return rebuilt
	}
	result := rebuilt
	if origSections.HasGoal {
		result = mergeSummarySection(result, summaryGoalTitle, boundPreservedSection(origSections.Goal))
	}
	if origSections.HasActivity {
		result = mergeSummarySection(result, summaryActivityTitle, boundPreservedSection(origSections.ToolActivity))
	}
	return result
}

// boundPreservedSection 在合并前对 orig 的单段做有界收敛，遵循「新历史优先」：
// 先检测早期模板标记并按新详略级别重写一次，再按 preservedSectionMaxRunes 裁剪。
// 两段都只在超出上界/命中 legacy 标记时改变，短章节逐字节保留。函数幂等：
// 重写/裁剪生成的标记行会被识别，重复调用不会再次叠加。
func boundPreservedSection(block string) string {
	if block == "" {
		return block
	}
	block = rewriteLegacySummarySection(block)
	return capSummarySection(block, preservedSectionMaxRunes)
}

// rewriteLegacySummarySection 把命中早期格式化标记的章节折叠为新详略级别：
// 保留标题行与开头少量仍有信息量的正文，其余以一条有界提示替代。新历史（rebuilt）
// 会覆盖旧章节，因此这里刻意不保留逐文件 changelog / 复述的规则文件全文。
func rewriteLegacySummarySection(block string) string {
	if !hasLegacySectionMarker(block) || strings.Contains(block, legacySectionNotice) {
		return block
	}
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) == 0 {
		return block
	}
	heading := lines[0]
	used := len([]rune(heading))
	out := make([]string, 0, legacySectionKeepLines+2)
	out = append(out, heading)
	kept := 0
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if kept >= legacySectionKeepLines || used+len([]rune(line)) > legacySectionKeepRunes {
			break
		}
		// 命中 legacy 标记的行是流水/复述，直接丢弃，不占保留额度。
		if lineHasLegacyMarker(line) {
			continue
		}
		out = append(out, line)
		used += len([]rune(line))
		kept++
	}
	return strings.Join(out, "\n") + legacySectionNotice
}

// hasLegacySectionMarker / lineHasLegacyMarker 报告章节（或其中一行）是否带早期模板标记。
// 标题行本身不参与逐行匹配（例如 "## Changelog" 标题由 heading 单独判断）。
func hasLegacySectionMarker(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		if lineHasLegacyMarker(line) {
			return true
		}
	}
	return false
}

func lineHasLegacyMarker(line string) bool {
	for _, marker := range legacySectionMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// capSummarySection 按 rune 上限裁剪单个章节：只保留开头的完整行，超出部分用一条
// 有界提示替代。已带裁剪提示的块原样返回，保证幂等；短块原样返回，逐字节不变。
func capSummarySection(block string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(block)) <= maxRunes || strings.Contains(block, trimmedSectionNotice) {
		return block
	}
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	out := make([]string, 0, len(lines))
	used := 0
	for _, line := range lines {
		n := len([]rune(line)) + 1 // +1 为行尾换行
		if len(out) > 0 && used+n > maxRunes {
			break
		}
		out = append(out, line)
		used += n
	}
	return strings.Join(out, "\n") + trimmedSectionNotice
}

// mergeSummarySection 将单个章节块合并进 text：已有同名章节则整段替换，否则追加到末尾。
// 替换保留段前的原有空行；追加时保证段前有一个空行，块本身自带的结尾换行原样保留。
func mergeSummarySection(text, title, block string) string {
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for i := 0; i < len(lines); i++ {
		t, ok := summarySectionTitle(lines[i])
		if !ok || !summaryTitleMatches(t, title) {
			continue
		}
		end = i + 1
		for end < len(lines) && !summaryIsHeading(lines[end]) {
			end++
		}
		start = i
		break
	}

	blockLines := strings.Split(block, "\n")
	if start >= 0 {
		merged := make([]string, 0, len(lines)-(end-start)+len(blockLines))
		merged = append(merged, lines[:start]...)
		merged = append(merged, blockLines...)
		merged = append(merged, lines[end:]...)
		return strings.Join(merged, "\n")
	}

	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" && !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	return text + block
}

// summarySectionTitle 识别 markdown 标题行并返回其标题。只接受行首的 "##"
// （允许更多 '#'），因此项目符号里的 "- ## Goal" 不会被误认作标题。
func summarySectionTitle(line string) (string, bool) {
	if !strings.HasPrefix(line, "##") {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(line, "#"))
	if title == "" {
		return "", false
	}
	return title, true
}

func summaryIsHeading(line string) bool {
	_, ok := summarySectionTitle(line)
	return ok
}

// summarySectionBlock 把 [start, end) 行拼成章节块。段尾的空行被归一化掉，
// 块统一以单个换行结尾，便于原样写回而不产生多余空行；标题与内容行本身逐字保留。
func summarySectionBlock(lines []string, start, end int) string {
	content := lines[start:end]
	for len(content) > 0 && strings.TrimSpace(content[len(content)-1]) == "" {
		content = content[:len(content)-1]
	}
	return strings.Join(content, "\n") + "\n"
}

// normalizeSummaryTitle 去掉标题两端的装饰字符并统一大小写，便于中英文匹配。
func normalizeSummaryTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(title, ":： \t")))
}

func isSummaryGoalTitle(title string) bool {
	switch normalizeSummaryTitle(title) {
	case "goal", "goals", "目标", "目的":
		return true
	}
	return false
}

func isSummaryActivityTitle(title string) bool {
	switch normalizeSummaryTitle(title) {
	case "tool activity", "tool activities", "toolactivity", "工具活动", "工具活动记录":
		return true
	}
	return false
}

func summaryTitleMatches(title, want string) bool {
	if want == summaryGoalTitle {
		return isSummaryGoalTitle(title)
	}
	return isSummaryActivityTitle(title)
}
