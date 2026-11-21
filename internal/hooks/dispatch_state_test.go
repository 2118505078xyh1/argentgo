package hooks

import "testing"

// TestClassifyDispatchStateAllCombinations 穷举三个布尔信号的全部八种组合，
// 校验 closed > terminating > busy > idle 的优先级与互斥性。
func TestClassifyDispatchStateAllCombinations(t *testing.T) {
	cases := []struct {
		name       string
		inFlight   bool
		terminated bool
		closed     bool
		want       HookDispatchState
	}{
		{name: "all false is idle", want: HookDispatchIdle},
		{name: "in flight only is busy", inFlight: true, want: HookDispatchBusy},
		{name: "terminated only is terminating", terminated: true, want: HookDispatchTerminating},
		{name: "closed only is closed", closed: true, want: HookDispatchClosed},
		{name: "in flight and terminated is terminating", inFlight: true, terminated: true, want: HookDispatchTerminating},
		{name: "in flight and closed is closed", inFlight: true, closed: true, want: HookDispatchClosed},
		{name: "terminated and closed is closed", terminated: true, closed: true, want: HookDispatchClosed},
		{name: "all true is closed", inFlight: true, terminated: true, closed: true, want: HookDispatchClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDispatchState(tc.inFlight, tc.terminated, tc.closed); got != tc.want {
				t.Fatalf("classifyDispatchState(%v, %v, %v) = %q, want %q",
					tc.inFlight, tc.terminated, tc.closed, got, tc.want)
			}
		})
	}
}

// TestClassifyDispatchStateStableValues 固定四种状态的字符串取值，
// 防止日后改动破坏遥测/审计下游按取值聚合的兼容性。
func TestClassifyDispatchStateStableValues(t *testing.T) {
	cases := []struct {
		state HookDispatchState
		want  string
	}{
		{HookDispatchIdle, "idle"},
		{HookDispatchBusy, "busy"},
		{HookDispatchTerminating, "terminating"},
		{HookDispatchClosed, "closed"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Fatalf("HookDispatchState(%q).String() = %q, want %q", string(tc.state), got, tc.want)
		}
	}
}

// TestClassifyDispatchStateDeterministic 校验纯函数性质：
// 对同一组输入重复调用始终返回同一状态，不受调用次序影响。
func TestClassifyDispatchStateDeterministic(t *testing.T) {
	inputs := []struct{ inFlight, terminated, closed bool }{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{false, false, true},
		{true, true, false},
		{true, true, true},
	}
	for _, in := range inputs {
		first := classifyDispatchState(in.inFlight, in.terminated, in.closed)
		for i := 0; i < 10; i++ {
			if got := classifyDispatchState(in.inFlight, in.terminated, in.closed); got != first {
				t.Fatalf("classifyDispatchState(%v, %v, %v) not stable: %q then %q",
					in.inFlight, in.terminated, in.closed, first, got)
			}
		}
	}
}

// TestClassifyDispatchStateExportSeam 校验导出接缝与内部纯函数行为一致，
// 使本包以外的调用方也能 opt-in 使用同一套分类。
func TestClassifyDispatchStateExportSeam(t *testing.T) {
	inFlight, terminated, closed := true, true, false
	if got, want := ClassifyDispatchState(inFlight, terminated, closed), classifyDispatchState(inFlight, terminated, closed); got != want {
		t.Fatalf("ClassifyDispatchState = %q, classifyDispatchState = %q", got, want)
	}
}
