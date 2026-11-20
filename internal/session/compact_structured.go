package session

import (
	"fmt"
	"strings"
)

// StructuredSummary 是压缩摘要的机器可读投影。
//
// Compaction.Summary 历来是一个自由格式的 markdown 字符串（见 Summarize）。
// 这个 DTO 是对该文本的附加、可选视图：它让后续的 UI 或搜索索引能够消费进度、
// 不确定事项和用户操作，而无需重新实现 markdown 解析器。压缩写入路径不使用它——
// Summarize 继续输出与以前逐字节相同的自由文本。
type StructuredSummary struct {
	Progress      []string `json:"progress,omitempty"`
	Uncertainties []string `json:"uncertainties,omitempty"`
	UserActions   []string `json:"userActions,omitempty"`
}

// structuredBucket 是一个 markdown 章节标题的内部目标。
type structuredBucket int

const (
	bucketNone structuredBucket = iota
	bucketProgress
	bucketUncertainties
	bucketUserActions
)

// summaryHeadingBuckets 将归一化后的章节标题映射到 StructuredSummary 的三个字段之一。
// 它刻意覆盖三类：
//
//   - RenderStructuredSummary 输出的规范结构化标题；
//   - Summarize 一直写入的旧标题（"Goal"、"Failures"、"Next steps" 等），
//     使现有的自由文本可以原样解析；
//   - 常见的中文等价标题，因为摘要常常以简体中文书写。
//
// 不在表中的标题被视为结构而非内容：其章节会被跳过，本身不会让 ParseStructuredSummary 成功。
// 这使格式漂移能回退到"非结构化"，而不是产生一个具有误导性的部分 DTO。
var summaryHeadingBuckets = map[string]structuredBucket{
	// 规范的结构化标题。
	"progress":      bucketProgress,
	"uncertainties": bucketUncertainties,
	"uncertainty":   bucketUncertainties,
	"user actions":  bucketUserActions,
	"user action":   bucketUserActions,

	// Summarize 的旧版章节。
	"goal":          bucketProgress,
	"goals":         bucketProgress,
	"files changed": bucketProgress,
	"tool activity": bucketProgress,
	"failures":      bucketUncertainties,
	"failure":       bucketUncertainties,
	"next steps":    bucketUserActions,
	"next step":     bucketUserActions,

	// 中文等价标题。
	"进展":    bucketProgress,
	"进度":    bucketProgress,
	"已完成":   bucketProgress,
	"目标":    bucketProgress,
	"文件变更":  bucketProgress,
	"修改文件":  bucketProgress,
	"工具活动":  bucketProgress,
	"不确定性":  bucketUncertainties,
	"不确定事项": bucketUncertainties,
	"不确定":   bucketUncertainties,
	"失败":    bucketUncertainties,
	"错误":    bucketUncertainties,
	"用户操作":  bucketUserActions,
	"用户行动":  bucketUserActions,
	"下一步":   bucketUserActions,
	"后续":    bucketUserActions,
	"待办":    bucketUserActions,
}

// normalizeSummaryHeading 识别 markdown 标题行并返回其归一化标题。
// 只有当一行的第一个非空白字符是 '#' 时，它才是标题；普通标签（"Progress:"）和粗体文本
// （"**Progress**"）都不是——格式漂移必须能被检测到，而不是被静默接受。
func normalizeSummaryHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed[0] != '#' {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	title = strings.Join(strings.Fields(title), " ") // 折叠空白，包括 U+3000
	title = strings.TrimRight(title, ":：")
	title = strings.ToLower(title)
	return title, true
}

// summaryBullet 提取 "- "、"* " 或 "• " 项目符号的文本。空条目和 "(none)" 形式的
// 占位条目不是内容，会被丢弃。跳过占位条目使 Render -> Parse 成为干净的往返，
// 因为渲染器用它们来让空章节对人类读者保持可见。
func summaryBullet(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "• "} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		item := strings.TrimSpace(line[len(prefix):])
		if item == "" || isSummaryNoneMarker(item) {
			return "", false
		}
		return item, true
	}
	return "", false
}

func isSummaryNoneMarker(item string) bool {
	switch strings.ToLower(strings.TrimSpace(item)) {
	case "(none)", "none", "n/a", "(无)", "（无）", "无":
		return true
	}
	return false
}

// ParseStructuredSummary 是一个尽力而为的纯解析器，把自由文本的压缩摘要投影到
// StructuredSummary。布尔值报告是否找到了任何*可识别的*章节标题：false 表示文本不包含
// 结构化信息（为空、只有散文或格式已漂移），调用方应继续使用原始自由文本。
// 当某个章节存在但没有条目时，true 的结果中可能包含空字段。
//
// 条目按出现顺序收集并去重。无法识别的章节（包括开头的 "## Compaction" 元数据块）会被忽略。
func ParseStructuredSummary(text string) (StructuredSummary, bool) {
	var out StructuredSummary
	seenProgress := make(map[string]bool)
	seenUncertainties := make(map[string]bool)
	seenUserActions := make(map[string]bool)

	current := bucketNone
	found := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if title, ok := normalizeSummaryHeading(trimmed); ok {
			current = summaryHeadingBuckets[title] // 未知标题 -> bucketNone
			if current != bucketNone {
				found = true
			}
			continue
		}
		if current == bucketNone {
			continue
		}
		item, ok := summaryBullet(trimmed)
		if !ok {
			continue
		}
		var dest *[]string
		var seen map[string]bool
		switch current {
		case bucketProgress:
			dest, seen = &out.Progress, seenProgress
		case bucketUncertainties:
			dest, seen = &out.Uncertainties, seenUncertainties
		case bucketUserActions:
			dest, seen = &out.UserActions, seenUserActions
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		*dest = append(*dest, item)
	}
	if !found {
		return StructuredSummary{}, false
	}
	return out, true
}

// RenderStructuredSummary 以 ParseStructuredSummary 能理解的规范 markdown 形式
// 渲染 StructuredSummary。空字段输出为 "- (none)"，使人类可读文本保持熟悉的 Summarize 布局，
// 同时 ParseStructuredSummary 仍能将它们往返为空切片。
func RenderStructuredSummary(s StructuredSummary) string {
	var b strings.Builder
	write := func(title string, items []string) {
		fmt.Fprintf(&b, "\n## %s\n", title)
		if len(items) == 0 {
			b.WriteString("- (none)\n")
			return
		}
		for _, item := range items {
			// 每行保持一个项目符号：内嵌的换行看起来会像新的章节正文，从而破坏往返。
			one := strings.TrimSpace(item)
			one = strings.ReplaceAll(one, "\r", " ")
			one = strings.ReplaceAll(one, "\n", " ")
			fmt.Fprintf(&b, "- %s\n", one)
		}
	}
	write("Progress", s.Progress)
	write("Uncertainties", s.Uncertainties)
	write("User actions", s.UserActions)
	return b.String()
}

// Structured 是 c.Summary 的只读、可选投影。它不修改记录，也不被任何压缩写入路径调用。
func (c Compaction) Structured() (StructuredSummary, bool) {
	return ParseStructuredSummary(c.Summary)
}
