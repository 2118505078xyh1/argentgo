package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestCanRunParallelDeclaredSafe 固定核心契约：只有显式声明为 true 才允许并行。
func TestCanRunParallelDeclaredSafe(t *testing.T) {
	cases := []struct {
		name   string
		table  map[string]bool
		tool   string
		expect bool
	}{
		{"声明安全", map[string]bool{"read": true}, "read", true},
		{"显式声明不安全", map[string]bool{"read": false}, "read", false},
		{"未声明", map[string]bool{"grep": true}, "read", false},
		{"空表", map[string]bool{}, "read", false},
		{"nil 表", nil, "read", false},
		{"空工具名", map[string]bool{"": true}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canRunParallel(tc.tool, tc.table); got != tc.expect {
				t.Fatalf("canRunParallel(%q, %v) = %v, want %v", tc.tool, tc.table, got, tc.expect)
			}
		})
	}
}

// TestCanRunParallelUnknownNameIsSerial 锁定 fail-closed：未知工具名一律串行。
func TestCanRunParallelUnknownNameIsSerial(t *testing.T) {
	safe := map[string]bool{"read": true, "grep": true}
	for _, name := range []string{"write", "unknown_tool", "READ"} {
		if canRunParallel(name, safe) {
			t.Fatalf("未知工具名 %q 不应被判定为可并行", name)
		}
	}
}

// TestCanRunParallelCJKToolName 确认 CJK 工具名按精确字节键匹配，不会因字符编码被误判。
func TestCanRunParallelCJKToolName(t *testing.T) {
	safe := map[string]bool{"读取文件": true, "搜索内容": false}

	if !canRunParallel("读取文件", safe) {
		t.Fatal("已声明安全的 CJK 工具名应可并行")
	}
	if canRunParallel("搜索内容", safe) {
		t.Fatal("显式声明不安全的 CJK 工具名应串行")
	}
	if canRunParallel("中文工具", safe) {
		t.Fatal("未声明的 CJK 工具名应串行")
	}
	if canRunParallel("读取文件", nil) {
		t.Fatal("nil 白名单下 CJK 工具名应串行")
	}
}

// runParallelProbe 驱动一次两调用的派发，并收集事件用于判断批次是否真正并行。
func runParallelProbe(t *testing.T, opts Options, calls []llm.ToolCall) *eventSink {
	t.Helper()
	sink := &eventSink{}
	if opts.Tools == nil {
		opts.Tools = &fakeTools{specs: []llm.ToolSpec{
			toolSpec("read"), toolSpec("grep"), toolSpec("write"),
		}}
	}
	opts.MaxParallel = 2
	s := newRunState(opts, "probe", sink.emit)
	s.runTools(context.Background(), 0, calls)
	return sink
}

// batchRanInParallel 依据事件顺序判断批次是否并发：
// 并行路径会先宣告所有 tool_start，再记录任何 tool_end；串行路径则交替出现。
func batchRanInParallel(sink *eventSink) bool {
	ends := 0
	for _, event := range sink.events {
		switch event.Kind {
		case EventToolStart:
			if ends > 0 {
				return false
			}
		case EventToolEnd:
			ends++
		}
	}
	return ends >= 2
}

// TestParallelDeclaredOnlyOffKeepsMutatingBehavior 固定 opt-in 默认关闭的契约：
// 未声明任何工具为安全时，只读批次仍沿用既有并行行为。
func TestParallelDeclaredOnlyOffKeepsMutatingBehavior(t *testing.T) {
	SetParallelDeclaredOnly(false)
	defer SetParallelDeclaredOnly(false)
	SetParallelSafeTools(nil)
	defer SetParallelSafeTools(nil)

	sink := runParallelProbe(t, Options{}, []llm.ToolCall{
		toolCall("c1", "read", `{}`),
		toolCall("c2", "grep", `{}`),
	})
	if !batchRanInParallel(sink) {
		t.Fatal("默认关闭时两个只读调用应保持既有并行行为")
	}
}

// TestParallelDeclaredOnlyOnForcesSerialWhenUndeclared 验证 fail-closed 接线：
// 启用仅白名单模式但未声明任何工具安全时，只读批次也必须串行。
func TestParallelDeclaredOnlyOnForcesSerialWhenUndeclared(t *testing.T) {
	SetParallelDeclaredOnly(true)
	defer SetParallelDeclaredOnly(false)
	SetParallelSafeTools(nil)
	defer SetParallelSafeTools(nil)

	sink := runParallelProbe(t, Options{}, []llm.ToolCall{
		toolCall("c1", "read", `{}`),
		toolCall("c2", "grep", `{}`),
	})
	if batchRanInParallel(sink) {
		t.Fatal("启用仅白名单模式且未声明时，批次必须串行")
	}
}

// TestParallelDeclaredOnlyOnParallelizesAllDeclared 验证启用后所有调用都被声明安全时才恢复并行。
func TestParallelDeclaredOnlyOnParallelizesAllDeclared(t *testing.T) {
	SetParallelDeclaredOnly(true)
	defer SetParallelDeclaredOnly(false)
	SetParallelSafeTools([]string{"read", "grep"})
	defer SetParallelSafeTools(nil)

	sink := runParallelProbe(t, Options{}, []llm.ToolCall{
		toolCall("c1", "read", `{}`),
		toolCall("c2", "grep", `{}`),
	})
	if !batchRanInParallel(sink) {
		t.Fatal("两个调用都被声明安全时，批次应可并行")
	}
}

// TestParallelDeclaredOnlyOnPartialDeclarationIsSerial 验证任一调用未声明都会让整批串行。
func TestParallelDeclaredOnlyOnPartialDeclarationIsSerial(t *testing.T) {
	SetParallelDeclaredOnly(true)
	defer SetParallelDeclaredOnly(false)
	SetParallelSafeTools([]string{"read"})
	defer SetParallelSafeTools(nil)

	sink := runParallelProbe(t, Options{}, []llm.ToolCall{
		toolCall("c1", "read", `{}`),
		toolCall("c2", "grep", `{}`),
	})
	if batchRanInParallel(sink) {
		t.Fatal("只要有一个调用未被声明安全，批次就必须串行")
	}
}

// TestParallelDeclaredOnlyVetoesMutatingEvenIfDeclared 验证接线只会更保守：
// 变更型工具即便被误列入白名单，也不得进入并行路径。
func TestParallelDeclaredOnlyVetoesMutatingEvenIfDeclared(t *testing.T) {
	SetParallelDeclaredOnly(true)
	defer SetParallelDeclaredOnly(false)
	SetParallelSafeTools([]string{"write", "read"})
	defer SetParallelSafeTools(nil)

	sink := runParallelProbe(t, Options{}, []llm.ToolCall{
		toolCall("c1", "write", `{}`),
		toolCall("c2", "read", `{}`),
	})
	if batchRanInParallel(sink) {
		t.Fatal("变更型调用即使被声明安全，也仍须串行")
	}
}
