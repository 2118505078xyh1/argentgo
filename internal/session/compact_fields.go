package session

import (
	"fmt"
	"strings"
)

// StructuredSummaryV2 是压缩摘要的扩展只读投影。
//
// 它在原有 StructuredSummary 的三桶（Progress / Uncertainties / User actions）之外，
// 增加了 Objective / Requirements / Decisions / Work State / Relevant Files /
// Important Context 六个桶，以对齐常见压缩摘要模板的字段。这个类型是**新增**的：
// 原 StructuredSummary 与 ParseStructuredSummary / RenderStructuredSummary 保持不变，
// 因此现有 golden 与压缩写入路径逐字节不受影响。
//
// 与 StructuredSummary 一样，这里只提供可选投影：没有任何压缩写入路径调用本文件里的
// 解析/渲染函数，Compaction.Summary 仍是唯一的事实来源。
type StructuredSummaryV2 struct {
	Objective        []string `json:"objective,omitempty"`
	Requirements     []string `json:"requirements,omitempty"`
	Decisions        []string `json:"decisions,omitempty"`
	WorkState        []string `json:"workState,omitempty"`
	RelevantFiles    []string `json:"relevantFiles,omitempty"`
	ImportantContext []string `json:"importantContext,omitempty"`
	Progress         []string `json:"progress,omitempty"`
	Uncertainties    []string `json:"uncertainties,omitempty"`
	UserActions      []string `json:"userActions,omitempty"`
}

// structuredBucketV2 是扩展解析器里 markdown 章节标题的内部目标。
type structuredBucketV2 int

const (
	bucketNoneV2 structuredBucketV2 = iota
	bucketObjective
	bucketRequirements
	bucketDecisions
	bucketWorkState
	bucketRelevantFiles
	bucketImportantContext
	bucketProgressV2
	bucketUncertaintiesV2
	bucketUserActionsV2
	bucketCountV2
)

// legacyBucketToV2 把原三桶枚举翻译成扩展枚举，使 V2 能复用原有的标题映射而不修改原文件。
func legacyBucketToV2(b structuredBucket) structuredBucketV2 {
	switch b {
	case bucketProgress:
		return bucketProgressV2
	case bucketUncertainties:
		return bucketUncertaintiesV2
	case bucketUserActions:
		return bucketUserActionsV2
	default:
		return bucketNoneV2
	}
}

// summaryHeadingBucketsV2 将归一化后的章节标题映射到 StructuredSummaryV2 的字段。
// 它先完整继承 summaryHeadingBuckets（保持旧标题兼容），再叠加新增标题；同名的新标题
// 会覆盖旧映射（例如 "目标" 在 V2 中归到 Objective，而原解析器仍把它归到 Progress）。
//
// 不在表中的标题被视为结构而非内容：其章节会被跳过，本身不会让解析成功。
var summaryHeadingBucketsV2 = func() map[string]structuredBucketV2 {
	m := make(map[string]structuredBucketV2, len(summaryHeadingBuckets)+16)
	for title, b := range summaryHeadingBuckets {
		if v2 := legacyBucketToV2(b); v2 != bucketNoneV2 {
			m[title] = v2
		}
	}
	for title, b := range map[string]structuredBucketV2{
		// Objective。
		"objective":    bucketObjective,
		"objectives":   bucketObjective,
		"objective(s)": bucketObjective,
		"目标":           bucketObjective,
		"目的":           bucketObjective,

		// Requirements。
		"requirements": bucketRequirements,
		"requirement":  bucketRequirements,
		"需求":           bucketRequirements,

		// Decisions。
		"decisions": bucketDecisions,
		"decision":  bucketDecisions,
		"决策":        bucketDecisions,

		// Work State。
		"work state":    bucketWorkState,
		"workstate":     bucketWorkState,
		"working state": bucketWorkState,
		"工作区状态":         bucketWorkState,
		"工作进度":          bucketWorkState,

		// Relevant Files。
		"relevant files": bucketRelevantFiles,
		"related files":  bucketRelevantFiles,
		"相关文件":           bucketRelevantFiles,

		// Important Context。
		"important context": bucketImportantContext,
		"important":         bucketImportantContext,
		"重要上下文":             bucketImportantContext,
		"重要信息":              bucketImportantContext,
	} {
		m[title] = b
	}
	return m
}()

// bucket 返回 s 中对应桶的切片指针；bucketNoneV2 或未知值返回 nil。
func (s *StructuredSummaryV2) bucket(b structuredBucketV2) *[]string {
	switch b {
	case bucketObjective:
		return &s.Objective
	case bucketRequirements:
		return &s.Requirements
	case bucketDecisions:
		return &s.Decisions
	case bucketWorkState:
		return &s.WorkState
	case bucketRelevantFiles:
		return &s.RelevantFiles
	case bucketImportantContext:
		return &s.ImportantContext
	case bucketProgressV2:
		return &s.Progress
	case bucketUncertaintiesV2:
		return &s.Uncertainties
	case bucketUserActionsV2:
		return &s.UserActions
	default:
		return nil
	}
}

// ParseStructuredSummaryV2 是尽力而为的纯解析器，把自由文本的压缩摘要投影到
// StructuredSummaryV2。布尔值报告是否找到了任何*可识别的*章节标题：false 表示文本
// 不包含结构化信息，调用方应继续使用原始自由文本。
//
// 解析复用 normalizeSummaryHeading / summaryBullet 的逻辑，因此支持 # 标题、
// "- " / "* " / "• " 项目符号、(none) 占位符与 CRLF 换行。条目按出现顺序收集并去重；
// 无法识别的章节（包括开头的 "## Compaction" 元数据块）会被忽略。
func ParseStructuredSummaryV2(text string) (StructuredSummaryV2, bool) {
	var out StructuredSummaryV2
	seen := make([]map[string]bool, bucketCountV2)
	for i := range seen {
		seen[i] = make(map[string]bool)
	}

	current := bucketNoneV2
	found := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if title, ok := normalizeSummaryHeading(trimmed); ok {
			current = summaryHeadingBucketsV2[title] // 未知标题 -> bucketNoneV2
			if current != bucketNoneV2 {
				found = true
			}
			continue
		}
		if current == bucketNoneV2 {
			continue
		}
		item, ok := summaryBullet(trimmed)
		if !ok {
			continue
		}
		dest := out.bucket(current)
		if dest == nil {
			continue
		}
		if seen[current][item] {
			continue
		}
		seen[current][item] = true
		*dest = append(*dest, item)
	}
	if !found {
		return StructuredSummaryV2{}, false
	}
	return out, true
}

// RenderStructuredSummaryV2 以 ParseStructuredSummaryV2 能理解的规范 markdown 形式
// 渲染 StructuredSummaryV2。字段顺序固定为 Objective / Requirements / Decisions /
// Work State / Relevant Files / Important Context / Progress / Uncertainties / User actions。
// 空字段输出为 "- (none)"，使人类可读文本保持熟悉的 Summarize 布局，同时
// ParseStructuredSummaryV2 仍能将它们往返为空切片。
func RenderStructuredSummaryV2(s StructuredSummaryV2) string {
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
	write("Objective", s.Objective)
	write("Requirements", s.Requirements)
	write("Decisions", s.Decisions)
	write("Work State", s.WorkState)
	write("Relevant Files", s.RelevantFiles)
	write("Important Context", s.ImportantContext)
	write("Progress", s.Progress)
	write("Uncertainties", s.Uncertainties)
	write("User actions", s.UserActions)
	return b.String()
}

// StructuredV2 是 c.Summary 的只读、可选扩展投影。它不修改记录，也不被任何压缩写入路径调用。
func (c Compaction) StructuredV2() (StructuredSummaryV2, bool) {
	return ParseStructuredSummaryV2(c.Summary)
}
