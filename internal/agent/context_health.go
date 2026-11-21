package agent

import (
	"math"
	"strings"
)

// 本文件提供"上下文健康度"诊断原语：以一组消息文本为样本，量化上下文被
// 重复指引与错误占位 stub 污染的程度。它是纯函数、默认不接线——只有显式调用
// ScoreContextHealth 时才会被计算，未调用时运行行为完全不变。
//
// 设计动机（R39）：guidanceDedupe.Injected 只统计注入条数；MeasureFocus 与
// FailureSignalSummary 虽是纯函数却默认不接线。缺少一个把"重复指引密度"与
// "错误 stub 占比"合成单一分数的量化指标。本原语补上这一缺口，不改动任何
// 现有类型或循环，也不引入新的包间依赖。

// ContextHealth 是一次上下文健康度评估的快照。所有字段只读，调用方应视为值。
type ContextHealth struct {
	// GuidanceRepeatRatio 是重复指引占比：指引类消息中，与此前某条指引规范化后
	// 相同的条数 / 指引类消息总数。无指引时为 0。
	GuidanceRepeatRatio float64
	// ErrorStubRatio 是错误占位占比：含 [tool output trimmed / [truncated 等裁剪
	// 标记的消息数 / 消息总数。无消息时为 0。
	ErrorStubRatio float64
	// Score 是综合健康分 = 1 - (加权污染)，钳制在 [0,1]，越高越健康。
	Score float64
}

// 权重与精度。指引重复权重略高于 stub 占比——反复注入同一指引比少量占位更能
// 说明上下文已经退化。两者之和略大于 1，使"双重严重污染"能把 Score 压到 0
// 以下，从而让钳制分支真正可达（见 ScoreContextHealth 的钳制说明）。
const (
	contextHealthWeightGuidance = 0.6
	contextHealthWeightStub     = 0.5
	contextHealthPrecision      = 1e6
)

// contextErrorStubMarkers 识别"错误占位/裁剪 stub"消息：工具输出被回收或截断后
// 留下的标记。采用忽略大小写的子串匹配，与 failureSignalMarkers 同口径——标记
// 被埋在进度输出之后也能被捕获。首项与 agent/trim.go 的 trimmedPrefix 同值，
// 但此处以本地常量维护，避免为一个字符串常量引入新的包间依赖。
var contextErrorStubMarkers = []string{
	"[tool output trimmed",           // 回收的工具结果（trim.go 的 trimmedPrefix）
	"[truncated",                     // 通用截断：[truncated] / [truncated: ...] / [truncated at ...]
	"[superseded by a newer read",    // 被较新读取取代的旧读取占位
	"[uneventful result elided]",     // 空结果被消费后的占位
	"[... tool result middle pruned", // 超长工具结果中间被裁剪
}

// contextGuidanceMarkers 识别"指引类"文本：失败纠正 / stuck 恢复 / 编辑格式
// 指引里常见的措辞。与 stub 标记一样做忽略大小写的子串匹配；中英文各列一组，
// 覆盖代码库里实际的 nudge 文案（stuck.go / editformat.go）并补充通用词与中文
// 指引词，使仅含中文的指引也能被识别。
var contextGuidanceMarkers = []string{
	"edit-format-guidance",
	"change the tool",
	"stop retrying",
	"commit to one approach",
	"take a concrete action",
	"diagnose the underlying cause",
	"without progress",
	"retry",
	"guidance",
	"nudge",
	"建议", "尝试", "重试", "指引", "改用", "请先",
}

// ScoreContextHealth 对一组消息样本评估上下文健康度。
//
// 它是纯函数：只读 messages，不修改切片或其中字符串，不持有任何共享可变状态，
// 相同输入总产生相同报告。全程使用按 rune 工作的字符串操作（strings.ToLower /
// Fields / Contains 都不按字节切分多字节序列），故对 CJK、emoji 及任意 UTF-8
// 安全；空输入或 nil 返回零占比与满分 1（没有污染证据即视为健康）。
//
// 计数规则：
//   - ErrorStubRatio = 含任一 contextErrorStubMarkers 的消息数 / len(messages)；
//   - GuidanceRepeatRatio：先取所有含 contextGuidanceMarkers 的消息，按规范化
//     文本（忽略大小写、折叠空白、去首尾空白）去重，重复条数 / 指引消息总数；
//   - Score = 1 - (0.6*GuidanceRepeatRatio + 0.5*ErrorStubRatio)。
//
// 权重之和略大于 1：当两类污染同时接近满分时会算出负值，此时钳制到 0；
// 另一侧钳制到 1，保证 Score 始终落在 [0,1]。三个浮点字段固定到小数后 6 位，
// 消除边界上的浮点误差，便于快照与比对。
func ScoreContextHealth(messages []string) ContextHealth {
	var h ContextHealth
	total := len(messages)
	if total == 0 {
		h.Score = 1
		return h
	}

	stubs := 0
	guidance := 0
	repeats := 0
	seen := make(map[string]struct{})
	for _, msg := range messages {
		lower := strings.ToLower(msg)
		if containsAny(lower, contextErrorStubMarkers...) {
			stubs++
		}
		if !containsAny(lower, contextGuidanceMarkers...) {
			continue
		}
		guidance++
		key := normalizeGuidance(msg)
		if _, dup := seen[key]; dup {
			repeats++
		} else {
			seen[key] = struct{}{}
		}
	}

	if guidance > 0 {
		h.GuidanceRepeatRatio = float64(repeats) / float64(guidance)
	}
	h.ErrorStubRatio = float64(stubs) / float64(total)

	weighted := contextHealthWeightGuidance*h.GuidanceRepeatRatio +
		contextHealthWeightStub*h.ErrorStubRatio
	h.Score = 1 - weighted
	if h.Score < 0 {
		h.Score = 0
	} else if h.Score > 1 {
		h.Score = 1
	}

	h.GuidanceRepeatRatio = roundHealth(h.GuidanceRepeatRatio)
	h.ErrorStubRatio = roundHealth(h.ErrorStubRatio)
	h.Score = roundHealth(h.Score)
	return h
}

// normalizeGuidance 把指引文本折叠为用于去重比较的键：忽略大小写、按 unicode
// 空白切分再以单个空格连接、去首尾空白。strings.Fields 在 rune 边界工作，
// 因此不会切开 CJK / emoji 的多字节序列；空文本归一化后为空串。
func normalizeGuidance(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// roundHealth 把浮点值收敛到小数后 contextHealthPrecision 位，消除加权带来的
// 尾数误差，使快照与边界测试稳定。
func roundHealth(v float64) float64 {
	return math.Round(v*contextHealthPrecision) / contextHealthPrecision
}
