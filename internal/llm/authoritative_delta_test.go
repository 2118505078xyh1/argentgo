package llm_test

import (
	"reflect"
	"testing"

	"example.com/argentgo/internal/llm"
)

// cloneDeltaSlice 返回切片的独立副本，用于检测比较函数是否改动入参。
func cloneDeltaSlice(in []llm.ToolCallDelta) []llm.ToolCallDelta {
	if in == nil {
		return nil
	}
	out := make([]llm.ToolCallDelta, len(in))
	copy(out, in)
	return out
}

// TestAuthoritativeToolCallDeltas 验证从最终 Message 提取权威摘要的字段映射、
// 序号稳定性、CJK 参数保真、空输入，以及不修改入参与返回值的独立性。
func TestAuthoritativeToolCallDeltas(t *testing.T) {
	cases := []struct {
		name string
		msg  llm.Message
		want []llm.ToolCallDelta
	}{
		{
			name: "multiple tools keep call order and index",
			msg: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.txt"}`}},
				{ID: "call_2", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"b.txt","content":"x"}`}},
				{ID: "call_3", Function: llm.ToolCallFunction{Name: "ls", Arguments: `{}`}},
			}},
			want: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
				{Index: 1, ID: "call_2", Name: "write", Arguments: `{"path":"b.txt","content":"x"}`},
				{Index: 2, ID: "call_3", Name: "ls", Arguments: `{}`},
			},
		},
		{
			name: "cjk arguments preserved verbatim",
			msg: llm.Message{ToolCalls: []llm.ToolCall{
				{ID: "call_cjk", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"路径":"中文/目录","内容":"你好，世界"}`}},
			}},
			want: []llm.ToolCallDelta{
				{Index: 0, ID: "call_cjk", Name: "write", Arguments: `{"路径":"中文/目录","内容":"你好，世界"}`},
			},
		},
		{
			name: "message without tool calls yields empty slice",
			msg:  llm.Message{Role: "assistant", Content: "ok"},
			want: []llm.ToolCallDelta{},
		},
		{
			name: "nil tool calls yields empty slice",
			msg:  llm.Message{Role: "assistant"},
			want: []llm.ToolCallDelta{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]llm.ToolCall(nil), tc.msg.ToolCalls...)
			got := llm.AuthoritativeToolCallDeltas(tc.msg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("AuthoritativeToolCallDeltas() = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.msg.ToolCalls, before) {
				t.Fatalf("入参 msg 被修改: %+v -> %+v", before, tc.msg.ToolCalls)
			}
			// 返回值必须是独立内存：改动它不能影响 msg。
			if len(got) > 0 {
				got[0].Arguments = " mutated "
				got[0].Name = "mutated"
				if !reflect.DeepEqual(tc.msg.ToolCalls, before) {
					t.Fatalf("修改返回值影响了 msg: %+v", tc.msg.ToolCalls)
				}
			}
		})
	}
}

// TestDeltasMatchFinal 表驱动覆盖：完全一致、空白差异容忍（含 CJK 全角空格）、
// ID/Name/内容不同、长度不同、空切片、乱序按 Index 对齐、重复 Index，
// 并断言两个入参在比较后保持不变。
func TestDeltasMatchFinal(t *testing.T) {
	base := []llm.ToolCallDelta{
		{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
		{Index: 1, ID: "call_2", Name: "write", Arguments: `{"路径":"中文","内容":"你好"}`},
	}

	cases := []struct {
		name     string
		streamed []llm.ToolCallDelta
		final    []llm.ToolCallDelta
		want     bool
	}{
		{
			name:     "exact match with multiple tools",
			streamed: cloneDeltaSlice(base),
			final:    cloneDeltaSlice(base),
			want:     true,
		},
		{
			name: "whitespace differences tolerated including cjk full width space",
			streamed: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: "  {\"path\":\t\"a.txt\"}\n"},
				{Index: 1, ID: "call_2", Name: "write", Arguments: `{"路径":"中　文","内容":"你好"}`},
			},
			final: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path": "a.txt"}`},
				{Index: 1, ID: "call_2", Name: "write", Arguments: `{"路径":"中 文","内容":"你好"}`},
			},
			want: true,
		},
		{
			name: "id mismatch is false",
			streamed: []llm.ToolCallDelta{
				{Index: 0, ID: "call_other", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			final: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			want: false,
		},
		{
			name: "name mismatch is false",
			streamed: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "write", Arguments: `{"path":"a.txt"}`},
			},
			final: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			want: false,
		},
		{
			name: "content mismatch beyond whitespace is false",
			streamed: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"b.txt"}`},
			},
			final: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			want: false,
		},
		{
			name:     "streamed longer than final is false",
			streamed: cloneDeltaSlice(base),
			final:    cloneDeltaSlice(base)[:1],
			want:     false,
		},
		{
			name:     "final longer than streamed is false",
			streamed: cloneDeltaSlice(base)[:1],
			final:    cloneDeltaSlice(base),
			want:     false,
		},
		{
			name:     "both empty is true",
			streamed: []llm.ToolCallDelta{},
			final:    []llm.ToolCallDelta{},
			want:     true,
		},
		{
			name:     "nil versus empty is true",
			streamed: nil,
			final:    []llm.ToolCallDelta{},
			want:     true,
		},
		{
			name:     "empty versus non empty is false",
			streamed: []llm.ToolCallDelta{},
			final:    []llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "read", Arguments: `{}`}},
			want:     false,
		},
		{
			name: "out of order streamed aligns by index",
			streamed: []llm.ToolCallDelta{
				{Index: 1, ID: "call_2", Name: "write", Arguments: `{"路径":"中文","内容":"你好"}`},
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			final: cloneDeltaSlice(base),
			want:  true,
		},
		{
			name: "duplicate index is false",
			streamed: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
			},
			final: []llm.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: "read", Arguments: `{"path":"a.txt"}`},
				{Index: 1, ID: "call_2", Name: "write", Arguments: `{}`},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamedBefore, finalBefore := cloneDeltaSlice(tc.streamed), cloneDeltaSlice(tc.final)
			got := llm.DeltasMatchFinal(tc.streamed, tc.final)
			if got != tc.want {
				t.Errorf("DeltasMatchFinal() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.streamed, streamedBefore) || !reflect.DeepEqual(tc.final, finalBefore) {
				t.Errorf("入参被修改: streamed %+v -> %+v, final %+v -> %+v",
					streamedBefore, tc.streamed, finalBefore, tc.final)
			}
		})
	}
}
