package memory

// 上下文回收规划原语。本文件提供一套与 Bank/Recall/Spill 完全解耦的纯函数：
// 根据候选条目的年龄与被取代/无用标记，规划一份淘汰（evict）/保留（keep）
// 名单，并渲染成稳定的中文摘要。它刻意不挂在 Bank 上，也没有任何调用方：
// 只提供独立的规划能力，不改变现有读取、召回或落盘行为，因此默认关闭。
//
// 全部为纯函数：不修改入参、不执行 I/O、不触碰任何全局状态。

import (
	"sort"
	"strconv"
	"strings"
)

// ReclaimCandidate 是一条待规划回收的候选条目。
//
// Key 是条目的稳定标识（可为 CJK 文本，按原样参与比较与渲染）；
// Age 是条目年龄（回合或时间代理值，与 maxAge 同单位）；
// Superseded 表示读取结果已被更新版本取代；
// Useless 表示空搜索或空结果这类无留存价值的条目；
// Bytes 是条目占用的字节数，仅供调用方参考，不参与淘汰判定。
type ReclaimCandidate struct {
	Key        string
	Age        int
	Superseded bool
	Useless    bool
	Bytes      int
}

// PlanReclaim 按以下规则把 cands 拆成淘汰与保留两个名单：
//
//   - 满足 Age > maxAge、Superseded、Useless 任意一项者进入 evict；
//   - 其余条目进入 keep（因此 Age == maxAge 属保留，边界不淘汰）；
//   - 两个名单分别按 Key 升序排列，排序稳定（相同 Key 保持输入中的相对
//     顺序），故输出确定；
//   - 不修改 cands，返回全新的切片。
//
// Key 以 UTF-8 字节序列参与比较：ASCII 与多字节（如 CJK）字符都不会被
// 按字节截断，比较结果确定且不会破坏字符完整性。Bytes 字段不影响判定。
// 入参为空时返回两个 nil 切片。
func PlanReclaim(cands []ReclaimCandidate, maxAge int) (evict []string, keep []string) {
	for _, c := range cands {
		if c.Age > maxAge || c.Superseded || c.Useless {
			evict = append(evict, c.Key)
		} else {
			keep = append(keep, c.Key)
		}
	}
	sort.SliceStable(evict, func(i, j int) bool { return evict[i] < evict[j] })
	sort.SliceStable(keep, func(i, j int) bool { return keep[i] < keep[j] })
	return evict, keep
}

// ReclaimSummary 把淘汰与保留名单渲染成稳定的中文摘要，形如：
//
//	"回收计划：淘汰 2 项 [alpha、密钥]；保留 1 项 [beta]"
//
// 纯函数：不修改 evict/keep。两个名单各自按 Key 升序排列后渲染，
// 因此摘要与传入顺序无关；相同内容恒得相同字符串。CJK 键按原样写入，
// 不会被截断或改写；空名单渲染为 []。
func ReclaimSummary(evict, keep []string) string {
	e := sortedCopy(evict)
	k := sortedCopy(keep)

	var b strings.Builder
	b.WriteString("回收计划：淘汰 ")
	b.WriteString(strconv.Itoa(len(e)))
	b.WriteString(" 项 [")
	b.WriteString(strings.Join(e, "、"))
	b.WriteString("]；保留 ")
	b.WriteString(strconv.Itoa(len(k)))
	b.WriteString(" 项 [")
	b.WriteString(strings.Join(k, "、"))
	b.WriteString("]")
	return b.String()
}

// sortedCopy 返回 in 的升序副本，不修改 in；排序稳定（相同元素保序）。
func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
