package llm_test

import (
	"context"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestToolDeltaHookContext 验证 hook 在 context 中的往返、未安装时的 nil
// 语义，以及 nil hook / nil ctx 的边界。
func TestToolDeltaHookContext(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		wantNil bool
	}{
		{"background without hook", context.Background(), true},
		{"nil ctx", nil, true},
		{"nil hook leaves ctx unchanged", llm.WithToolDeltaHook(context.Background(), nil), true},
		{"installed hook", llm.WithToolDeltaHook(context.Background(), func(llm.ToolCallDelta) {}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.HookFromContext(tc.ctx)
			if (got == nil) != tc.wantNil {
				t.Fatalf("HookFromContext() nil = %v, want nil = %v", got == nil, tc.wantNil)
			}
		})
	}

	// 取出的 hook 必须可回调，且携带的增量原样送达。
	var calls []llm.ToolCallDelta
	hook := func(d llm.ToolCallDelta) { calls = append(calls, d) }
	installed := llm.WithToolDeltaHook(context.Background(), hook)
	want := llm.ToolCallDelta{Index: 2, ID: "call_7", Name: "read", Arguments: `{"p":1}`}
	llm.HookFromContext(installed)(want)
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("hook got %v, want a single %+v", calls, want)
	}
}

// TestMergeToolDelta 用表驱动覆盖参数累加、ID/Name 合并、Index 不一致、
// CJK 参数与不改入参等约定；每个用例同时断言两个入参在合并后保持不变。
func TestMergeToolDelta(t *testing.T) {
	cases := []struct {
		name  string
		acc   llm.ToolCallDelta
		delta llm.ToolCallDelta
		want  llm.ToolCallDelta
	}{
		{
			name:  "arguments accumulate in order",
			acc:   llm.ToolCallDelta{Index: 0, Arguments: `{"a":`},
			delta: llm.ToolCallDelta{Index: 0, Arguments: `1}`},
			want:  llm.ToolCallDelta{Index: 0, Arguments: `{"a":1}`},
		},
		{
			name:  "id and name taken from delta when acc empty",
			acc:   llm.ToolCallDelta{Index: 1, Arguments: "{"},
			delta: llm.ToolCallDelta{Index: 1, ID: "call_1", Name: "write", Arguments: `"x":2}`},
			want:  llm.ToolCallDelta{Index: 1, ID: "call_1", Name: "write", Arguments: `{"x":2}`},
		},
		{
			name:  "id and name keep first non-empty",
			acc:   llm.ToolCallDelta{Index: 1, ID: "call_1", Name: "read", Arguments: "{"},
			delta: llm.ToolCallDelta{Index: 1, ID: "call_2", Name: "write", Arguments: `}`},
			want:  llm.ToolCallDelta{Index: 1, ID: "call_1", Name: "read", Arguments: "{}"},
		},
		{
			name:  "index mismatch keeps acc index",
			acc:   llm.ToolCallDelta{Index: 3, ID: "call_3", Name: "read", Arguments: "{"},
			delta: llm.ToolCallDelta{Index: 9, Arguments: `}`},
			want:  llm.ToolCallDelta{Index: 3, ID: "call_3", Name: "read", Arguments: "{}"},
		},
		{
			name:  "cjk arguments accumulate across fragments",
			acc:   llm.ToolCallDelta{Index: 0, Name: "write", Arguments: `{"路径":"中`},
			delta: llm.ToolCallDelta{Index: 0, Arguments: `文","内容":"你好"}`},
			want:  llm.ToolCallDelta{Index: 0, Name: "write", Arguments: `{"路径":"中文","内容":"你好"}`},
		},
		{
			name:  "empty delta leaves metadata intact",
			acc:   llm.ToolCallDelta{Index: 2, ID: "call_9", Name: "ls", Arguments: "{}"},
			delta: llm.ToolCallDelta{Index: 2},
			want:  llm.ToolCallDelta{Index: 2, ID: "call_9", Name: "ls", Arguments: "{}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accBefore, deltaBefore := tc.acc, tc.delta
			got := llm.MergeToolDelta(tc.acc, tc.delta)
			if got != tc.want {
				t.Errorf("MergeToolDelta(%+v, %+v) = %+v, want %+v", tc.acc, tc.delta, got, tc.want)
			}
			if tc.acc != accBefore || tc.delta != deltaBefore {
				t.Errorf("入参被修改: acc %+v -> %+v, delta %+v -> %+v", accBefore, tc.acc, deltaBefore, tc.delta)
			}
		})
	}
}
