package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 隐式上下文保留（R12 长对话学习方案 #3，opt-in）的纯原语。
//
// 压缩摘要依靠显式正则标记（下一步词、失败词）来挑选要保留的散文，
// 但长对话里大量计划与失败并不带这些标记，100+ 轮后就被逐出摘要。
// 本文件的辅助函数从被丢弃的条目中"隐式"抽取一小段有界上下文，
// 由调用方显式决定是否合并进摘要。它们刻意不接入 Compact / Summarize，
// 因此默认压缩行为逐字节不变。

const (
	// implicitTailRunes 是每条 assistant 正文尾保留的最大符文数。
	implicitTailRunes = 160
	// implicitFailureRunes 是每条失败工具输出预览保留的最大符文数。
	implicitFailureRunes = 200
)

// implicitContextMarker 是合并幂等标记：摘要中一旦出现它，
// MergeImplicitIntoSummary 就不再重复追加，避免同一份摘要被合并两次时内容翻倍。
const implicitContextMarker = "<!-- argent:implicit-context-v1 -->"

// ImplicitContext 是从压缩丢弃条目中抽取的隐式上下文。
// AssistantTails 是最近若干条 assistant 正文的尾部片段，
// UnmarkedFailures 是未带保留标记却含失败信号的工具输出预览。
// 两个切片都按原始顺序排列（最近的项在后）。
type ImplicitContext struct {
	AssistantTails   []string
	UnmarkedFailures []string
}

// Empty 报告 ic 是否没有任何可合并的内容。
func (ic ImplicitContext) Empty() bool {
	return len(ic.AssistantTails) == 0 && len(ic.UnmarkedFailures) == 0
}

// ExtractImplicitContext 从被丢弃的条目中抽取隐式上下文。
// 它是纯函数：只读取 dropped，不修改入参，不产生任何副作用。
//
// tailN <= 0 时不抽取 assistant 正文尾；failN <= 0 时不抽取失败预览。
// 所有截断都在字素簇边界上进行（经 textutil），因此对 CJK / emoji 安全；
// 结果按输入顺序稳定排列，只保留最近 N 项。
func ExtractImplicitContext(dropped []Entry, tailN, failN int) ImplicitContext {
	var ic ImplicitContext
	if tailN > 0 {
		tails := make([]string, 0, tailN)
		for _, entry := range dropped {
			if entry.Role != "assistant" {
				continue
			}
			if tail := graphemeTail(entry.Content, implicitTailRunes); tail != "" {
				tails = append(tails, tail)
			}
		}
		ic.AssistantTails = tailItems(tails, tailN)
	}
	if failN > 0 {
		failures := make([]string, 0, failN)
		for _, entry := range dropped {
			// 带有 _retention 标记的条目会被 retainedForCompaction 重新发出，
			// 不属于"隐式"丢失的信息，因此这里只处理未带标记的工具结果。
			if entry.Role != "tool" || hasRetentionHint(entry.Data) {
				continue
			}
			if !hasImplicitFailureSignal(entry.Content, entry.Data) {
				continue
			}
			preview := graphemeTail(entry.Content, implicitFailureRunes)
			if preview == "" {
				// 只有 is_error 标志而没有输出文本时，给出稳定的占位说明。
				preview = "is_error"
			}
			failures = append(failures, failureBullet(entry.Name, preview))
		}
		ic.UnmarkedFailures = tailItems(failures, failN)
	}
	return ic
}

// collapseLine 将文本折叠为单行，与 truncateRunes 的口径一致，
// 便于安全地作为摘要项目符号插入。
func collapseLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

// graphemeTail 返回 text 折叠为单行后的最后 limit 个符文。
// 起点会被推进到最近的字素簇边界，因此绝不会从组合符、emoji 肤色、
// ZWJ 序列或旗帜中间开始——这对 CJK 与 emoji 都是安全的。
// limit <= 0 或空文本返回空字符串。
func graphemeTail(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	cleaned := collapseLine(text)
	if cleaned == "" {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	start := 0
	for i := 0; i < len(runes)-limit; i++ {
		_, size := utf8.DecodeRuneInString(cleaned[start:])
		if size == 0 {
			break
		}
		start += size
	}
	start = textutil.ClusterBoundaryAfter(cleaned, start)
	return strings.TrimSpace(cleaned[start:])
}

// dataField 从 JSON 对象载荷中读取单个原始字段值。
// 载荷为空、非对象或格式错误时返回 (nil, false)。
func dataField(data json.RawMessage, key string) (json.RawMessage, bool) {
	if len(data) == 0 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, false
	}
	value, ok := fields[key]
	return value, ok
}

// hasImplicitFailureSignal 报告工具结果是否携带失败信号：
// 载荷中 is_error 为真，或输出文本命中非零 [exit N]、"timed out"
// 以及失败词（错误 / 失败 / error / failed 等，复用 failureReason）。
func hasImplicitFailureSignal(content string, data json.RawMessage) bool {
	if flag, ok := dataField(data, "is_error"); ok && rawIsTrue(flag) {
		return true
	}
	_, failed := failureReason(content)
	return failed
}

// MergeImplicitIntoSummary 把 ic 作为项目符号追加到压缩摘要的
// "Failures" 与 "Next steps" 段，并返回新的摘要字符串。
// 它是纯字符串操作：不修改 summary、不触碰 Compaction 结构，
// 且对 CJK / emoji 安全（按整行与完整字符串处理，从不按字节截断）。
//
// 幂等：合并后会写入 implicitContextMarker；摘要中一旦出现该标记，
// 再次调用将原样返回 summary，因此同一份摘要不会被重复追加。
func MergeImplicitIntoSummary(summary string, ic ImplicitContext) string {
	if ic.Empty() || strings.Contains(summary, implicitContextMarker) {
		return summary
	}
	out := appendBulletsToSection(summary, "Failures", ic.UnmarkedFailures)
	out = appendBulletsToSection(out, "Next steps", ic.AssistantTails)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + implicitContextMarker + "\n"
}

// appendBulletsToSection 把 bullets 作为 "- " 项目符号插入指定 markdown 段，
// 放在该段现有内容与下一段标题之间。段不存在时在末尾新建该段。
// 使用按行拆分 / 拼接，保证原有内容逐字节保留。
func appendBulletsToSection(summary, title string, bullets []string) string {
	if len(bullets) == 0 {
		return summary
	}
	heading := "## " + title
	lines := strings.Split(summary, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSuffix(line, "\r") == heading {
			start = i
			break
		}
	}
	if start < 0 {
		out := summary
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		var b strings.Builder
		b.WriteString(out)
		b.WriteByte('\n')
		b.WriteString(heading)
		b.WriteByte('\n')
		for _, bullet := range bullets {
			fmt.Fprintf(&b, "- %s\n", bullet)
		}
		return b.String()
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSuffix(lines[i], "\r"), "## ") {
			end = i
			break
		}
	}
	// 跳过段尾空行，把新项目符号紧跟在现有内容之后。
	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	out := make([]string, 0, len(lines)+len(bullets))
	out = append(out, lines[:insertAt]...)
	for _, bullet := range bullets {
		out = append(out, "- "+bullet)
	}
	out = append(out, lines[insertAt:]...)
	return strings.Join(out, "\n")
}
