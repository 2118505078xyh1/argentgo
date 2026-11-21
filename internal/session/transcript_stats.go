package session

import (
	"encoding/json"
	"math"
)

// TranscriptStats 是会话转录的只读摘要，供状态展示、诊断与 eval 使用。
// 它只描述调用方给出的 []Entry：不读取会话文件、不修改入参，也不携带时间字段
// （时长需要调用方自行从 Entry.TS 计算，这里刻意不猜测）。
type TranscriptStats struct {
	Messages         int
	UserTurns        int
	AssistantTurns   int
	ToolCalls        int
	Errors           int
	PromptTokens     int64
	CompletionTokens int64
}

// SummarizeTranscript 遍历 entries 生成 TranscriptStats。统计规则：
//
//   - Messages 计入每一条 Entry（包括 system 摘要与未知 Role）。
//   - UserTurns/AssistantTurns/ToolCalls 分别按 Role == "user"/"assistant"/"tool" 计数。
//     assistant 载荷里的 tool_calls 数组不重复计数：每条工具结果 Entry 已经对应一次调用。
//   - Errors 计入带失败信号的工具记录（is_error 为真，或输出文本命中失败提示），
//     以及其它 Role 上显式 is_error 为真的 Entry。
//   - PromptTokens/CompletionTokens 从 Entry.Data 累加：既接受载荷顶层的
//     prompt_tokens/completion_tokens（stats 记录），也接受 usage 子对象中的
//     prompt_tokens/completion_tokens 或 input_tokens/output_tokens（导入的 assistant 记录）。
//     负数与无法解析的数字按 0 处理。
//
// 该函数是纯函数：不修改 entries 及其 Data，不依赖 map 遍历顺序，结果确定；
// 所有累加在逼近 int/int64 上限时钳制，不会环绕为负。
func SummarizeTranscript(entries []Entry) TranscriptStats {
	var stats TranscriptStats
	for _, entry := range entries {
		stats.Messages = clampAddInt(stats.Messages, 1)
		switch entry.Role {
		case "user":
			stats.UserTurns = clampAddInt(stats.UserTurns, 1)
		case "assistant":
			stats.AssistantTurns = clampAddInt(stats.AssistantTurns, 1)
		case "tool":
			stats.ToolCalls = clampAddInt(stats.ToolCalls, 1)
		}
		if entryIsError(entry) {
			stats.Errors = clampAddInt(stats.Errors, 1)
		}
		prompt, completion := transcriptTokens(entry.Data)
		stats.PromptTokens = clampAddInt64(stats.PromptTokens, prompt)
		stats.CompletionTokens = clampAddInt64(stats.CompletionTokens, completion)
	}
	return stats
}

// entryIsError 判断一条 Entry 是否为错误记录。工具记录复用 attempt_isolation.go
// 中的 toolRecordFailed，从而与尝试失败判定保持同一种失败语义；其它 Role 只认
// 显式的 is_error 标记，避免把助手讨论"错误"这一话题误计为一次错误。
func entryIsError(entry Entry) bool {
	if entry.Role == "tool" {
		return toolRecordFailed(entry.Data)
	}
	return dataIsError(entry.Data)
}

// dataIsError 读取载荷中布尔或字符串形式的 is_error 标记。
func dataIsError(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	flag, ok := fields["is_error"]
	return ok && rawIsTrue(flag)
}

// transcriptTokens 从一条 Entry 的 Data 中读取 (prompt, completion) token 计数。
// usage 子对象代表逐请求事实，优先于载荷顶层的汇总字段；usage 缺失或没有可识别
// 的 token 键时才回退到顶层 stats 字段。
func transcriptTokens(raw json.RawMessage) (int64, int64) {
	if len(raw) == 0 {
		return 0, 0
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return 0, 0
	}
	prompt := tokenCount(fields, "prompt_tokens", "input_tokens")
	completion := tokenCount(fields, "completion_tokens", "output_tokens")
	if usageRaw, ok := fields["usage"]; ok {
		var usage map[string]json.RawMessage
		if json.Unmarshal(usageRaw, &usage) == nil {
			up := tokenCount(usage, "prompt_tokens", "input_tokens")
			uc := tokenCount(usage, "completion_tokens", "output_tokens")
			if up != 0 || uc != 0 {
				prompt, completion = up, uc
			}
		}
	}
	return prompt, completion
}

// tokenCount 返回 JSON 对象中第一个存在且可解析的非负整数字段。
func tokenCount(fields map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var num json.Number
		if json.Unmarshal(raw, &num) != nil {
			return 0
		}
		if n, err := num.Int64(); err == nil {
			return max(int64(0), n)
		}
		// 超出 int64 的字面量（如 1e30）钳到上限，避免被当成 0 而丢失。
		if f, err := num.Float64(); err == nil && f > 0 {
			if f >= float64(math.MaxInt64) {
				return math.MaxInt64
			}
			return int64(f)
		}
		return 0
	}
	return 0
}

// clampAddInt 返回 a+b，在逼近 int 上限时钳制到 math.MaxInt。
func clampAddInt(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
