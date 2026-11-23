package session

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 本文件实现 TODO #134 的纯文本版 foveated（多保真）归档视图（第一步）。
// 思路学自 oh-my-pi 的 planArchive：两端高保真、中段稀疏化、超预算丢最旧 dense，
// 并插入显式 gap 标记。零 wire 变化——不新增记录种类，只在 Messages() 的读路径
// 投影阶段对 []Entry 做文本变换；设为 "0" 关闭时完全不进入本文件的逻辑。

// foveatedArchiveEnv 是 foveated 归档视图的总开关。默认开启（多保真视图能
// 在有限预算内保留两端逐字内容）；仅显式设为 "0"（允许首尾空白）时关闭。
const foveatedArchiveEnv = "ARGENT_FOVEATED_ARCHIVE"

// foveatedArchiveEnabled 报告 foveated 归档视图是否启用。默认开启，=0 关闭。
func foveatedArchiveEnabled() bool {
	return strings.TrimSpace(os.Getenv(foveatedArchiveEnv)) != "0"
}

// foveatedGapMarkerFmt 是 gap 标记的规范格式。固定句式不含任何用户可控内容，
// 既便于模型理解"这里有省略"，也便于测试用正则解析。
const foveatedGapMarkerFmt = "[… %d messages omitted …]"

// foveatedGapMarkerRe 解析 gap 标记中的省略条数（语义为"消息条数"，非 rune）。
var foveatedGapMarkerRe = regexp.MustCompile(`^\[… ([0-9]+) messages omitted …\]$`)

// foveatedGapDataKey 是 gap 标记 Entry 的 Data 里携带的机器可读键。
// 测试同时断言 Role/Data/Content 三者，避免用户正文里出现相同字符串被误判。
const foveatedGapDataKey = "foveated_gap"

// foveatedParseGap 从一段文本中解析出 gap 标记声明的省略条数；不是 gap 标记时返回 ok=false。
func foveatedParseGap(text string) (omitted int, ok bool) {
	m := foveatedGapMarkerRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(m[1], "%d", &omitted); err != nil {
		return 0, false
	}
	return omitted, true
}

// isFoveatedGap 判断一个 Entry 是否是 foveated gap 标记（三要素齐备才算）。
func isFoveatedGap(e Entry) bool {
	if e.Role != "system" {
		return false
	}
	if _, ok := foveatedParseGap(e.Content); !ok {
		return false
	}
	var data map[string]json.RawMessage
	return json.Unmarshal(e.Data, &data) == nil && len(data[foveatedGapDataKey]) > 0
}

// foveatedSparseRuneLimit 是中段每条稀疏视图的内容上限（rune）。
const foveatedSparseRuneLimit = 80

// foveatedArchive 对条目做多保真投影。核心约束：操作单位是 buildProjectionPlan
// 产出的"配对单元"（工具调用与其结果同留同弃），因此 gap 与稀疏切点绝不落在
// assistant 调用与 tool 结果之间，避免给提供方造成非法消息序列。
//
//   - 预算足够（Content rune 不超过 budget）时原样返回同一切片（零拷贝、零差异）；
//   - 超预算时：两端各保留若干**逐字不变**的高保真单元，中段从较新往较旧保留
//     若干稀疏单元（角色 + 首行，保留 ID/ToolCallID/Data），最旧的中段单元被丢弃，
//     并在头部与稀疏段之间插入一个可解析的 system gap 标记。
//
// budget 是 rune 预算，与预算投影共用来源（ARGENT_BUDGET_PROJECTION_BUDGET / 默认 32000）。
func foveatedArchive(entries []Entry, budget int) []Entry {
	if budget <= 0 || len(entries) == 0 {
		return entries
	}
	if foveatedEntriesRunes(entries) <= budget {
		return entries
	}

	plan := buildProjectionPlan(entries)
	headBudget := budget * 2 / 5
	midBudget := budget / 5
	if headBudget < 1 {
		headBudget = 1
	}
	if midBudget < 1 {
		midBudget = 1
	}

	// 两端按"单元"累加 rune，保证切点在配对边界。
	headUnits := foveatedTakeUnits(entries, plan, 0, len(plan.order), headBudget, false)
	tailUnits := foveatedTakeUnits(entries, plan, 0, len(plan.order)-headUnits, headBudget, true)

	firstMid := headUnits
	lastMid := len(plan.order) - tailUnits // 不含
	if firstMid >= lastMid {
		// 极端预算：两端已吃满，退化为"头 + gap(0) + 尾"。
		return foveatedAssemble(entries, plan, 0, 0, headUnits, lastMid, 0)
	}

	// 中段从较新往较旧挑稀疏单元，直到用掉 midBudget。
	sparseFrom := lastMid
	used := 0
	for u := lastMid - 1; u >= firstMid; u-- {
		cost := foveatedUnitSparseCost(entries, plan.units[plan.order[u]])
		if used+cost > midBudget && used > 0 {
			break
		}
		used += cost
		sparseFrom = u
	}
	omitted := 0
	for u := firstMid; u < sparseFrom; u++ {
		omitted += len(plan.units[plan.order[u]])
	}
	// 稀疏段是 [sparseFrom, lastMid)（较新的少数单元）；[firstMid, sparseFrom) 是
	// 被完全丢弃的旧 dense 单元，只计入 gap 的 omitted 数。
	return foveatedAssemble(entries, plan, sparseFrom, lastMid, headUnits, lastMid, omitted)
}

// foveatedAssemble 按 [头高保真单元][gap][中段稀疏单元][尾高保真单元] 的顺序拼装结果。
// headUnits 取 order 前 headUnits 个；tailUnits 取 order[tailStart:]；
// sparse 区间为 order[sparseFrom:sparseTo]（内部被稀疏化）。
func foveatedAssemble(entries []Entry, plan projectionPlan, sparseFrom, sparseTo, headCount, tailStart, omitted int) []Entry {
	// 收集各个区间的原始下标，保持原先后顺序。
	var headIdx, sparseIdx, tailIdx []int
	for u := 0; u < headCount; u++ {
		headIdx = append(headIdx, plan.units[plan.order[u]]...)
	}
	for u := sparseFrom; u < sparseTo; u++ {
		sparseIdx = append(sparseIdx, plan.units[plan.order[u]]...)
	}
	for u := tailStart; u < len(plan.order); u++ {
		tailIdx = append(tailIdx, plan.units[plan.order[u]]...)
	}

	out := make([]Entry, 0, len(headIdx)+len(sparseIdx)+len(tailIdx)+1)
	for _, i := range headIdx {
		out = append(out, entries[i])
	}
	if omitted > 0 {
		out = append(out, foveatedGapEntry(omitted))
	}
	for _, i := range sparseIdx {
		e := entries[i]
		line := firstLine(e.Content, foveatedSparseRuneLimit)
		if e.Role != "" {
			line = e.Role + ": " + line
		}
		e.Content = line
		e.Reasoning = ""
		e.ReasoningSignature = ""
		out = append(out, e)
	}
	for _, i := range tailIdx {
		out = append(out, entries[i])
	}
	return out
}

// foveatedTakeUnits 从单元序列的一端累加 Content rune，返回纳入的单元数。
// fromNewest=true 时从末端往前取（用于尾部）。
func foveatedTakeUnits(entries []Entry, plan projectionPlan, start, end, budget int, fromNewest bool) int {
	used, taken := 0, 0
	if fromNewest {
		for u := end - 1; u >= start; u-- {
			cost := foveatedUnitRunes(entries, plan.units[plan.order[u]])
			if used+cost > budget {
				break
			}
			used += cost
			taken++
		}
		return taken
	}
	for u := start; u < end; u++ {
		cost := foveatedUnitRunes(entries, plan.units[plan.order[u]])
		if used+cost > budget {
			break
		}
		used += cost
		taken++
	}
	return taken
}

// foveatedUnitRunes 统计一个单元内所有消息的 Content rune（与 Project 的预算口径一致）。
func foveatedUnitRunes(entries []Entry, idxs []int) int {
	total := 0
	for _, i := range idxs {
		total += len([]rune(entries[i].Content))
	}
	return total
}

// foveatedUnitSparseCost 统计一个单元稀疏化后的 rune 成本。
func foveatedUnitSparseCost(entries []Entry, idxs []int) int {
	total := 0
	for _, i := range idxs {
		e := entries[i]
		line := firstLine(e.Content, foveatedSparseRuneLimit)
		if e.Role != "" {
			line = e.Role + ": " + line
		}
		total += len([]rune(line))
	}
	return total
}

// foveatedEntriesRunes 统计全部条目的 Content rune 总数。
func foveatedEntriesRunes(entries []Entry) int {
	total := 0
	for _, e := range entries {
		total += len([]rune(e.Content))
	}
	return total
}

// foveatedGapEntry 构造一个可解析、带机器可读标记的 gap 消息。
func foveatedGapEntry(omitted int) Entry {
	data, _ := json.Marshal(map[string]int{foveatedGapDataKey: omitted})
	return Entry{
		ID:      fmt.Sprintf("foveated-gap-%d", omitted),
		Role:    "system",
		Content: fmt.Sprintf(foveatedGapMarkerFmt, omitted),
		Data:    data,
	}
}
