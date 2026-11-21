package app

import (
	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 本文件提供“压缩前先跑本地确定性裁剪”的编排原语（对应 R30 学 omp 的
// shake/methodOrder 思想）。它刻意只包含纯函数与零调用方：主会话在压缩点
// 先调用 LocalTrimEstimate，若 ShouldSkipModelSummary 判定已省够，就跳过
// 一次模型摘要调用，省下成本；否则才走 generateModelSummary。
//
// 文件不改动 compact.go / integration.go 的任何生产接线，接线由主会话完成。

const (
	// localTrimMarker 与 tools.clipTo 的转换标记逐字节一致，使这里的 token
	// 估算与真实 head+tail 裁剪产出的上下文成本对齐。
	localTrimMarker = "\n... [truncated] ...\n"

	// defaultLocalTrimMinSavingRatio 是 ShouldSkipModelSummary 的默认阈值：
	// 本地裁剪省下 30% 以上上下文就足以跳过模型摘要。
	defaultLocalTrimMinSavingRatio = 0.3
)

// LocalTrimResult 汇总本地裁剪的估算结果。
type LocalTrimResult struct {
	// Trimmed 报告是否有工具结果会被 head+tail 裁剪（内容确实变短）。
	Trimmed bool
	// BeforeTokens 是裁剪前整批 entry 的估算 token 总量。
	BeforeTokens int
	// AfterTokens 是裁剪后的估算 token 总量。
	AfterTokens int
	// Saved 等于 BeforeTokens-AfterTokens，恒为非负。
	Saved int
}

// LocalTrimEstimate 估算：对每条工具结果做 head+tail 裁剪（与 tools.clipTo
// 相同的窗口与字素簇边界规则）之后，这批会话 entry 能省下多少 token。
//
// 纯函数契约：
//   - 只读 entries，不修改、不重排入参；
//   - 结果只由入参决定，无随机、无外部状态，重复调用逐字段相同；
//   - 仅 Role=="tool" 的条目参与裁剪，其余条目原样计入前后总量；
//   - Before/After 只累计 Content（entriesFromMessages 产出的 entry 不携带
//     Reasoning），用 agent.EstimateTokens 估算，因此 CJK/emoji 与 ASCII
//     都使用与预算循环同一套 token 成本定义；
//   - maxOutput<=0 表示禁用裁剪，返回前后相等的结果。
//
// 防御：若某条结果插入标记后反而更贵（极端小的 maxOutput），该条保持原样，
// 因此 Saved 恒非负，裁剪绝不增加上下文。
func LocalTrimEstimate(entries []session.Entry, maxOutput int) LocalTrimResult {
	var res LocalTrimResult
	beforeTotal, afterTotal := 0, 0
	for _, e := range entries {
		before := agent.EstimateTokens(e.Content)
		after := before
		if e.Role == "tool" && maxOutput > 0 {
			if clipped := localHeadTailClip(e.Content, maxOutput); clipped != e.Content {
				if estimate := agent.EstimateTokens(clipped); estimate < before {
					after = estimate
					res.Trimmed = true
				}
			}
		}
		beforeTotal += before
		afterTotal += after
	}
	res.BeforeTokens = beforeTotal
	res.AfterTokens = afterTotal
	res.Saved = beforeTotal - afterTotal
	return res
}

// localHeadTailClip 复刻 tools.clipTo 的 head+tail 裁剪：保留前 2/3、后 1/3，
// 中间用 localTrimMarker 替代。切点吸附到字素簇边界（head 只回退、tail 只
// 前进），因此 CJK、组合标记、ZWJ emoji、旗帜都不会被切坏；非法 UTF-8 按
// textutil 的约定原样保留。长度未超限或 maxOutput 非正时原样返回。
func localHeadTailClip(text string, maxOutput int) string {
	if maxOutput <= 0 || len(text) <= maxOutput {
		return text
	}
	// 与 tools.clipTo 一致：head = limit*2/3，tail = 其余。
	head := textutil.ClusterBoundaryBefore(text, maxOutput*2/3)
	tailStart := textutil.ClusterBoundaryAfter(text, len(text)-(maxOutput-maxOutput*2/3))
	// 窗口重叠的防御：正常情况下 len>maxOutput 保证不重叠，这里仍保持
	// 结果严格短于原文，避免未来预算改动把“裁剪”变成增长。
	if head > tailStart {
		tailStart = head
	}
	return text[:head] + localTrimMarker + text[tailStart:]
}

// ShouldSkipModelSummary 报告本地裁剪是否已省够，足以跳过模型摘要调用。
//
// 判据是 Saved/Before >= minSavingRatio；比值用 float64 计算，避免整数
// 除法截断或溢出。minSavingRatio<=0 时使用默认值 defaultLocalTrimMinSavingRatio。
// Before<=0 或 Saved<=0（没有可比的基线或没有节省）一律返回 false，
// 让调用方维持原有的模型摘要路径。阈值取“大于等于”，恰好等于阈值视为达标。
func ShouldSkipModelSummary(r LocalTrimResult, minSavingRatio float64) bool {
	if r.BeforeTokens <= 0 || r.Saved <= 0 {
		return false
	}
	if minSavingRatio <= 0 {
		minSavingRatio = defaultLocalTrimMinSavingRatio
	}
	return float64(r.Saved)/float64(r.BeforeTokens) >= minSavingRatio
}
