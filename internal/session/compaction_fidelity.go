package session

import "strings"

// defaultCompactionMinScore 是 IsCompactionLossy 在 minScore<=0 时采用的默认覆盖度阈值。
const defaultCompactionMinScore = 0.75

// CompactionFidelity 量化一条压缩摘要对四个可靠性要素的覆盖情况：
// 目标（Goal）、改动文件（Files changed）、失败（Failures）与下一步（Next steps）。
// 它只描述度量结果，既不改动入参，也不触达磁盘。
type CompactionFidelity struct {
	HasGoal      bool
	ChangedFiles int
	FailureCount int
	NextSteps    int
	Score        float64
	Complete     bool
}

// compactionSection 标识摘要中的一个章节。sectionOther 用于 "Compaction"、"Tool activity"
// 等与四要素无关的章节，从而确保它们下面的项目符号不被误计。
type compactionSection int

const (
	sectionNone compactionSection = iota
	sectionGoal
	sectionFiles
	sectionFailures
	sectionNext
	sectionOther
)

// MeasureCompactionFidelity 解析压缩摘要文本，检测四要素是否存在并统计其数量。
//
// Summarize 产出的摘要使用 markdown 章节标题（"## Goal" 等）与 "- " 项目符号，
// 空章节写作 "- (none)"。本函数按标题识别章节、按项目符号计数，并跳过占位符
// 与被误提升为项目符号的标题行。除英文标题外也识别常用中文标题，以兼容 CJK 场景。
//
// Score 为四要素的覆盖度：每个存在的要素贡献 0.25，取值为 0、0.25、0.5、0.75、1；
// Complete 表示四要素齐全。本函数是纯函数，不改动入参，也不读取任何全局状态。
func MeasureCompactionFidelity(summary string) CompactionFidelity {
	var fidelity CompactionFidelity

	current := sectionNone
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if section, ok := compactionSectionFromHeading(line); ok {
			current = section
			continue
		}
		item, ok := compactionBulletItem(line)
		if !ok || isCompactionEmptyMarker(item) || strings.HasPrefix(item, "#") {
			// "- (none)" 占位与误提升的标题（"- ## Next steps"）都不是要素内容。
			continue
		}
		switch current {
		case sectionGoal:
			fidelity.HasGoal = true
		case sectionFiles:
			fidelity.ChangedFiles++
		case sectionFailures:
			fidelity.FailureCount++
		case sectionNext:
			fidelity.NextSteps++
		}
	}

	present := 0
	if fidelity.HasGoal {
		present++
	}
	if fidelity.ChangedFiles > 0 {
		present++
	}
	if fidelity.FailureCount > 0 {
		present++
	}
	if fidelity.NextSteps > 0 {
		present++
	}
	fidelity.Score = float64(present) / 4
	fidelity.Complete = present == 4
	return fidelity
}

// IsCompactionLossy 报告摘要的要素覆盖度是否低于阈值，视为有损。
// minScore<=0 时使用默认值 defaultCompactionMinScore（0.75）。
// Score 恰好等于阈值时不算有损（严格小于才判为有损）。
func IsCompactionLossy(summary string, minScore float64) bool {
	if minScore <= 0 {
		minScore = defaultCompactionMinScore
	}
	return MeasureCompactionFidelity(summary).Score < minScore
}

// compactionSectionFromHeading 将 markdown 标题行映射到四要素章节。
// 匹配的标题会被识别为 sectionOther，使后续项目符号不会被算进任何要素。
func compactionSectionFromHeading(line string) (compactionSection, bool) {
	if line == "" || line[0] != '#' {
		return sectionNone, false
	}
	title := strings.TrimSpace(strings.TrimLeft(line, "#"))
	title = strings.TrimSpace(strings.Trim(strings.ToLower(title), ":： "))
	switch title {
	case "goal", "goals", "目标", "目的":
		return sectionGoal, true
	case "files changed", "changed files", "files", "改动文件", "修改文件", "变更文件", "文件":
		return sectionFiles, true
	case "failures", "failure", "失败", "错误", "失败与错误":
		return sectionFailures, true
	case "next steps", "next step", "next", "下一步", "后续步骤", "后续", "待办", "todo":
		return sectionNext, true
	default:
		return sectionOther, true
	}
}

// compactionBulletItem 解析一条项目符号行，返回去掉符号与空白后的内容。
// 仅接受 "- "、"* "、"+ " 三种前缀；标题行（以 '#' 开头）不会被当作项目符号。
func compactionBulletItem(line string) (string, bool) {
	if line == "" {
		return "", false
	}
	switch line[0] {
	case '-', '*', '+':
	default:
		return "", false
	}
	item := strings.TrimSpace(line[1:])
	if item == "" || strings.Trim(item, "-*+") == "" {
		return "", false
	}
	return item, true
}

// isCompactionEmptyMarker 判断项目符号内容是否为"空章节"占位符。
func isCompactionEmptyMarker(item string) bool {
	switch strings.ToLower(item) {
	case "(none)", "none", "无", "(无)", "（无）":
		return true
	default:
		return false
	}
}
