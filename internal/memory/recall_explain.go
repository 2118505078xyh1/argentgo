package memory

// 召回评分解释原语。本文件把一条 RecallCandidate 的评分拆解成若干
// 要素，并把解释渲染成稳定的中文文本。它与 recall_rank.go 共享同一套
// 计分口径（标签命中加 tagMatchBonus、新近度为排序决胜键），但不接入
// 任何召回管线，也不改动 RankRecall 的行为。全部为纯函数：不修改入参、
// 不执行 I/O、不触碰 Bank。

import (
	"math"
	"strconv"
	"strings"
)

// ScoreFactor 是召回评分的一个要素及其贡献值。
type ScoreFactor struct {
	// Name 是要素的稳定标识（英文，便于机器读取）。
	Name string
	// Contribution 是该要素的贡献：base/tag_match 为加分数值，
	// recency 为新近度代理值（排序决胜键，不参与分数相加）。
	Contribution float64
}

// 要素标识。顺序固定为 base、tag_match、recency，便于稳定比较与渲染。
const (
	factorBase     = "base"
	factorTagMatch = "tag_match"
	factorRecency  = "recency"
)

// ExplainScore 按召回计分口径把 c 的评分拆解为要素列表。
//
// 拆解口径（与 Recall/RecallRanked 一致）：
//   - tag_match：opts.TagFilter 中每个标签（忽略大小写）只要命中 c.Tags
//     中任一标签便加 tagMatchBonus，每个 filter 标签最多计一次；零值
//     TagFilter 不加任何 tag 分。
//   - base：总分中扣除 tag 加分后的部分，即文本/标记命中贡献。当
//     Score 为有限数且 opts.TagFilter 与计分时所用标签一致时，
//     base 与 tag_match 之和等于 Score。NaN Score 视为最低分，base 取 0。
//   - recency：日志行号代理值，越大越新，仅作排序决胜键，不参与相加。
//
// 纯函数：不修改 c/opts，返回新切片，顺序固定为 base、tag_match、recency。
func ExplainScore(c RecallCandidate, opts RankOptions) []ScoreFactor {
	// tag 加分：与 Recall 中 wantTags 的计分循环完全一致。
	var tagBonus float64
	if len(opts.TagFilter) > 0 {
		for _, want := range opts.TagFilter {
			for _, tag := range c.Tags {
				if strings.EqualFold(want, tag) {
					tagBonus += float64(tagMatchBonus)
					break
				}
			}
		}
	}

	// 基础分为总分扣除 tag 加分后的余数；NaN 视为最低分，取 0。
	base := c.Score - tagBonus
	if math.IsNaN(base) {
		base = 0
	}

	return []ScoreFactor{
		{Name: factorBase, Contribution: base},
		{Name: factorTagMatch, Contribution: tagBonus},
		{Name: factorRecency, Contribution: float64(c.Recency)},
	}
}

// FormatExplanation 把 id 与评分要素渲染成稳定的中文文本，形如：
// "召回[id=abc] 基础分=2；tag匹配加分=3；新近度=42"。
//
// 纯函数：不修改 factors，按给定顺序渲染，相同输入恒得相同输出。
// id 按原样以 UTF-8 写入，CJK 字符不会被截断或改写。
func FormatExplanation(id string, factors []ScoreFactor) string {
	var b strings.Builder
	b.WriteString("召回[id=")
	b.WriteString(id)
	b.WriteString("]")

	sep := " " // 首个要素前用空格，要素之间用全角分号。
	for _, f := range factors {
		b.WriteString(sep)
		b.WriteString(factorLabel(f.Name))
		b.WriteString("=")
		b.WriteString(strconv.FormatFloat(f.Contribution, 'g', -1, 64))
		sep = "；"
	}
	return b.String()
}

// factorLabel 把要素标识映射为稳定中文标签，未知标识原样返回。
func factorLabel(name string) string {
	switch name {
	case factorBase:
		return "基础分"
	case factorTagMatch:
		return "tag匹配加分"
	case factorRecency:
		return "新近度"
	default:
		return name
	}
}
