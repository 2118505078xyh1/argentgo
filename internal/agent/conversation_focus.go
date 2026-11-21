package agent

import (
	"fmt"
	"math"
	"strings"
)

// 对话聚焦度诊断回答一个狭窄的问题：一段不断增长的对话是否仍然围绕最初的目标，
// 还是已经开始原地重复（卡住），或被工具结果淹没（游移）。
//
// 它是对基于事件的 stuck/dedupe/no_progress 检测的纯逻辑、无状态补充：
// 不依赖定时器、I/O 或循环，只读一组轻量样本即可得到可快照、可比较的诊断报告。
// 该原语默认不接入运行循环；只有显式调用 MeasureFocus 时才会被计算，
// 因此未配置时的运行行为完全不变。

// FocusSample 是一条用于聚焦度诊断的轻量对话样本。
// 它刻意不依赖 agent 或 llm 的具体消息类型，使诊断保持无调用方、零依赖。
// Role 为角色（"user"/"assistant"/"tool" 等，大小写与周边空白不敏感），
// Content 为文本内容，Turn 为调用方给出的轮次序号。
type FocusSample struct {
	Role    string
	Content string
	Turn    int
}

// FocusReport 是一次对话聚焦度的快照，由 MeasureFocus 产生。
// 所有字段都是只读诊断量，调用方应将其视为值。
type FocusReport struct {
	// Turns 是观察到的样本总数（每个样本对应一个观察到的交互条目）。
	Turns int
	// UserTurns 是角色归一化为 "user" 的样本数。
	UserTurns int
	// ToolResults 是角色为工具结果（tool/tool_result/toolresult/function）的样本数。
	ToolResults int
	// RepeatedContents 是内容与更早某条样本相同的样本数（首次出现不计，空内容不计）。
	RepeatedContents int
	// LastUserTurn 是最后一条 user 样本的 Turn；没有 user 样本时为 0。
	LastUserTurn int
	// Drift 是重复率与工具结果占比的加权综合指标，稳定落在 [0,1]。
	Drift float64
	// Verdict 是按固定阈值分出的聚焦等级："focused"/"wandering"/"stuck"。
	Verdict string
}

// 聚焦等级字符串。不导出以保持诊断原语的封闭性；值本身稳定，便于快照与日志比对。
const (
	focusVerdictFocused   = "focused"
	focusVerdictWandering = "wandering"
	focusVerdictStuck     = "stuck"
)

// 漂移计算与分级阈值。两个信号先各自归一化为 [0,1] 的占比，再按权重合成 Drift；
// 重复率权重更高，因为"反复输出相同内容"比"工具结果密集"更能说明对话偏离了目标。
// 阈值是包内常量、规则稳定，便于测试锁定边界。
const (
	focusWeightRepetition = 0.7
	focusWeightTool       = 0.3

	focusDriftWandering = 0.20
	focusDriftStuck     = 0.45

	// 把 Drift 收敛到小数后 6 位，消除 0.2/0.45 这类边界上的浮点误差。
	focusDriftPrecision = 1e6
)

// MeasureFocus 测量一组对话样本的聚焦度。它是纯函数：只读入参，
// 不修改 samples 或其中的字符串，也不持有任何共享可变状态，相同输入总产生相同报告。
//
// 计数规则：
//   - Turns = len(samples)；
//   - UserTurns：Role 归一化后等于 "user" 的样本数，并据此更新 LastUserTurn；
//   - ToolResults：Role 为 "tool"/"tool_result"/"toolresult"/"function" 的样本数；
//   - RepeatedContents：去空白后非空的内容，与此前某条样本完全相同的样本数（首次不计）。
//
// 角色归一化只用 strings.TrimSpace/ToLower；重复检测以完整内容字符串作映射键，
// 全程不按下标切分，故对 CJK、emoji 及任意多字节序列安全。
//
// Drift = 0.7*(RepeatedContents/Turns) + 0.3*(ToolResults/Turns)，空输入为 0。
// Verdict：Drift >= 0.45 为 stuck，>= 0.20 为 wandering，否则 focused。
func MeasureFocus(samples []FocusSample) FocusReport {
	r := FocusReport{Turns: len(samples)}
	seen := make(map[string]struct{}, len(samples))
	for _, s := range samples {
		role := strings.ToLower(strings.TrimSpace(s.Role))
		switch role {
		case "user":
			r.UserTurns++
			r.LastUserTurn = s.Turn
		case "tool", "tool_result", "toolresult", "function":
			r.ToolResults++
		}
		content := strings.TrimSpace(s.Content)
		if content == "" {
			continue
		}
		if _, dup := seen[content]; dup {
			r.RepeatedContents++
		} else {
			seen[content] = struct{}{}
		}
	}
	if r.Turns > 0 {
		repetitionRate := float64(r.RepeatedContents) / float64(r.Turns)
		toolRatio := float64(r.ToolResults) / float64(r.Turns)
		r.Drift = focusWeightRepetition*repetitionRate + focusWeightTool*toolRatio
	}
	r.Drift = math.Round(r.Drift*focusDriftPrecision) / focusDriftPrecision
	r.Verdict = r.verdict()
	return r
}

// verdict 根据 Drift 给出聚焦等级。先判 stuck 再判 wandering，阈值均含等号。
func (r FocusReport) verdict() string {
	switch {
	case r.Drift >= focusDriftStuck:
		return focusVerdictStuck
	case r.Drift >= focusDriftWandering:
		return focusVerdictWandering
	default:
		return focusVerdictFocused
	}
}

// Summary 返回稳定的中文摘要：等级标签经过翻译，未知等级原样保留，
// 浮点偏移固定两位小数。格式固定，可直接用于报告或快照比对。
func (r FocusReport) Summary() string {
	return fmt.Sprintf("对话聚焦度[%s]：轮次 %d，用户轮 %d，工具结果 %d，重复内容 %d，末次用户轮 %d，偏移 %.2f",
		focusVerdictLabel(r.Verdict), r.Turns, r.UserTurns, r.ToolResults,
		r.RepeatedContents, r.LastUserTurn, r.Drift)
}

// focusVerdictLabel 把等级值翻译成中文标签；未知值原样返回，保证摘要永不丢失信息。
func focusVerdictLabel(v string) string {
	switch v {
	case focusVerdictFocused:
		return "聚焦"
	case focusVerdictWandering:
		return "游移"
	case focusVerdictStuck:
		return "卡住"
	default:
		return v
	}
}
