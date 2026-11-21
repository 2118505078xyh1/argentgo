package llm

import "strings"

// 本文件提供一组"权威工具调用摘要"纯函数：从最终 Message 的 ToolCalls
// 提取权威参数，并把流式采集结果与最终权威结果对齐比较。
//
// 它们不接任何 wire、没有调用方、不修改入参，只依赖标准库。用途是给
// 上层（例如 agent 的最终确认逻辑）提供诊断能力：判断流式 hook 采集到的
// 工具调用是否可信，从而决定是否需要用最终 ToolCalls 重新覆盖。

// AuthoritativeToolCallDeltas 从最终 Message.ToolCalls 提取权威摘要。
//
// 每一项产出一个 ToolCallDelta：Index 取该项在 ToolCalls 中的序号
// （从 0 开始，保持调用顺序稳定），ID/Name/Arguments 分别取自
// ToolCall.ID、Function.Name、Function.Arguments。Arguments 是最终
// 权威参数，可能由网关在流结束后补齐或改写。
//
// 消息按值传入，函数只读不写；返回切片为新建内存，调用方任意修改
// 都不会影响 msg。空 ToolCalls 返回长度为 0 的切片。
func AuthoritativeToolCallDeltas(msg Message) []ToolCallDelta {
	out := make([]ToolCallDelta, len(msg.ToolCalls))
	for i, call := range msg.ToolCalls {
		out[i] = ToolCallDelta{
			Index:     i,
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		}
	}
	return out
}

// DeltasMatchFinal 判断流式采集结果与最终权威摘要是否一致。
//
// 按 Index 对齐后逐项比较：ID、Name 必须完全相等，Arguments 经规范化
// （把所有 Unicode 空白折叠为单个 ASCII 空格并去掉首尾空白）后相等
// 才算匹配。长度不同、Index 对不齐、出现重复 Index 或任一字段不同都
// 返回 false。函数只读入参，不修改任何切片或元素。
//
// 它用于诊断"采集流是否可信"，不做 JSON 语义解析：参数顺序、键顺序
// 等差异仍会被视作不同，只有空白排版差异被容忍。
func DeltasMatchFinal(streamed, final []ToolCallDelta) bool {
	if len(streamed) != len(final) {
		return false
	}

	// 以 Index 建索引以支持乱序到达的流；重复 Index 无法可靠对齐，
	// 直接判为不可信。
	byIndex := make(map[int]ToolCallDelta, len(streamed))
	for _, d := range streamed {
		if _, dup := byIndex[d.Index]; dup {
			return false
		}
		byIndex[d.Index] = d
	}

	for _, f := range final {
		s, ok := byIndex[f.Index]
		if !ok {
			return false
		}
		if s.ID != f.ID || s.Name != f.Name {
			return false
		}
		if normalizeToolArguments(s.Arguments) != normalizeToolArguments(f.Arguments) {
			return false
		}
	}
	return true
}

// normalizeToolArguments 把 Unicode 空白折叠为单个 ASCII 空格并去首尾，
// 使流式分片与最终参数之间的纯排版差异不影响相等判断。strings.Fields
// 基于 unicode.IsSpace，覆盖全角空格等 CJK 场景。
func normalizeToolArguments(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
