package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// focusSampleOf 把 "role:content" 形式的条目解析成样本并赋予给定 Turn。
// 无已知角色前缀时默认为 assistant，因此内容里出现的 ":" 不会被误判为角色。
func focusSampleOf(turn int, spec string) FocusSample {
	role, content := "assistant", spec
	if i := strings.Index(spec, ":"); i >= 0 {
		switch spec[:i] {
		case "user", "assistant", "tool", "function", "system":
			role, content = spec[:i], spec[i+1:]
		}
	}
	return FocusSample{Role: role, Content: content, Turn: turn}
}

// focusSamples 把一组 "role:content" 条目包装成 Turn 从 1 递增的样本序列。
func focusSamples(specs ...string) []FocusSample {
	out := make([]FocusSample, 0, len(specs))
	for i, s := range specs {
		out = append(out, focusSampleOf(i+1, s))
	}
	return out
}

// repeatSpec 返回 n 个相同的条目，便于构造重复内容样本。
func repeatSpec(spec string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, spec)
	}
	return out
}

// TestMeasureFocusFocusedConversation 检查围绕任务的正常对话被判为聚焦，且各项计数正确。
func TestMeasureFocusFocusedConversation(t *testing.T) {
	samples := focusSamples(
		"user:请修复登录 bug",
		"assistant:我来查看登录代码",
		"tool:login.go 的内容",
		"assistant:找到问题了",
		"user:谢谢",
	)
	want := FocusReport{
		Turns: 5, UserTurns: 2, ToolResults: 1, RepeatedContents: 0,
		LastUserTurn: 5, Drift: 0.06, Verdict: focusVerdictFocused,
	}
	if got := MeasureFocus(samples); !reflect.DeepEqual(got, want) {
		t.Fatalf("focused conversation: got %+v, want %+v", got, want)
	}
	wantSummary := "对话聚焦度[聚焦]：轮次 5，用户轮 2，工具结果 1，重复内容 0，末次用户轮 5，偏移 0.06"
	if got := want.Summary(); got != wantSummary {
		t.Fatalf("Summary() = %q, want %q", got, wantSummary)
	}
}

// TestMeasureFocusRepeatedSpinning 检查反复输出相同内容被判为 stuck，
// 同时验证 LastUserTurn 记录的是最后一条 user 样本的 Turn。
func TestMeasureFocusRepeatedSpinning(t *testing.T) {
	specs := append([]string{"user:任务"}, repeatSpec("assistant:同样的回复", 7)...)
	got := MeasureFocus(focusSamples(specs...))
	if got.Verdict != focusVerdictStuck {
		t.Fatalf("repeated spinning: verdict = %q, want stuck (drift=%v)", got.Verdict, got.Drift)
	}
	want := FocusReport{
		Turns: 8, UserTurns: 1, ToolResults: 0, RepeatedContents: 6,
		LastUserTurn: 1, Drift: 0.525, Verdict: focusVerdictStuck,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated spinning: got %+v, want %+v", got, want)
	}
	if !strings.Contains(got.Summary(), "卡住") {
		t.Fatalf("stuck summary should contain 卡住: %q", got.Summary())
	}
}

// TestMeasureFocusToolFlooding 检查大量内容互异的工具结果被判为游移而非卡住：
// 工具密集会抬高漂移，但没有重复内容就不该触发 stuck。
func TestMeasureFocusToolFlooding(t *testing.T) {
	specs := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		specs = append(specs, fmt.Sprintf("tool:结果 %d", i))
	}
	got := MeasureFocus(focusSamples(specs...))
	want := FocusReport{
		Turns: 10, UserTurns: 0, ToolResults: 10, RepeatedContents: 0,
		LastUserTurn: 0, Drift: 0.3, Verdict: focusVerdictWandering,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool flooding: got %+v, want %+v", got, want)
	}
}

// TestMeasureFocusMixed 检查重复内容与工具结果同时存在时被判为游移，且各计数独立累加。
func TestMeasureFocusMixed(t *testing.T) {
	samples := focusSamples(
		"user:开始",
		"assistant:分析",
		"tool:r1",
		"tool:r2",
		"assistant:分析", // 与第 2 条内容重复
		"tool:r3",
		"user:继续",
		"tool:r4",
	)
	got := MeasureFocus(samples)
	want := FocusReport{
		Turns: 8, UserTurns: 2, ToolResults: 4, RepeatedContents: 1,
		LastUserTurn: 7, Drift: 0.2375, Verdict: focusVerdictWandering,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed: got %+v, want %+v", got, want)
	}
}

// TestMeasureFocusCJK 检查 CJK 与 emoji 多字节内容被正确比较、计数，不会 panic，
// 摘要中保留中文等级标签。
func TestMeasureFocusCJK(t *testing.T) {
	samples := focusSamples(
		"user:请修复中文路径问题",
		"assistant:正在检查项目/主.go 😀",
		"tool:项目/主.go 的内容：中文结果",
		"assistant:正在检查项目/主.go 😀", // 与上一条 assistant 内容重复
	)
	got := MeasureFocus(samples)
	want := FocusReport{
		Turns: 4, UserTurns: 1, ToolResults: 1, RepeatedContents: 1,
		LastUserTurn: 1, Drift: 0.25, Verdict: focusVerdictWandering,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK: got %+v, want %+v", got, want)
	}
	summary := got.Summary()
	for _, frag := range []string{"对话聚焦度", "游移", "重复内容 1"} {
		if !strings.Contains(summary, frag) {
			t.Fatalf("Summary() = %q, want it to contain %q", summary, frag)
		}
	}
}

// TestMeasureFocusBoundaries 检查空输入、单条样本、空白内容与分级阈值边界。
func TestMeasureFocusBoundaries(t *testing.T) {
	t.Run("nil and empty are focused", func(t *testing.T) {
		for _, samples := range [][]FocusSample{nil, {}} {
			got := MeasureFocus(samples)
			if got.Turns != 0 || got.Drift != 0 || got.Verdict != focusVerdictFocused {
				t.Fatalf("empty input: got %+v, want zero/focused", got)
			}
			if got.Summary() == "" {
				t.Fatal("empty report should still have a stable summary")
			}
		}
	})

	t.Run("single sample", func(t *testing.T) {
		got := MeasureFocus(focusSamples("user:唯一一条"))
		want := FocusReport{Turns: 1, UserTurns: 1, LastUserTurn: 1, Verdict: focusVerdictFocused}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("single sample: got %+v, want %+v", got, want)
		}
	})

	t.Run("whitespace only content is not a repeat", func(t *testing.T) {
		got := MeasureFocus(focusSamples("assistant:   ", "assistant:\t "))
		if got.RepeatedContents != 0 || got.Verdict != focusVerdictFocused {
			t.Fatalf("blank content should not count as repeat: %+v", got)
		}
	})

	cases := []struct {
		name        string
		specs       []string
		wantDrift   float64
		wantVerdict string
	}{
		{
			name:        "just below wandering",
			specs:       append(repeatSpec("assistant:a", 3), "assistant:b", "assistant:c", "assistant:d", "assistant:e", "assistant:f"),
			wantDrift:   0.175,
			wantVerdict: focusVerdictFocused,
		},
		{
			name:        "wandering lower bound inclusive",
			specs:       append(repeatSpec("assistant:a", 3), "assistant:b", "assistant:c", "assistant:d", "assistant:e"),
			wantDrift:   0.2,
			wantVerdict: focusVerdictWandering,
		},
		{
			name:        "just below stuck",
			specs:       append(repeatSpec("assistant:a", 7), "assistant:b", "assistant:c", "assistant:d"),
			wantDrift:   0.42,
			wantVerdict: focusVerdictWandering,
		},
		{
			name:        "stuck lower bound inclusive",
			specs:       append(repeatSpec("assistant:a", 7), "assistant:b", "assistant:c", "tool:t1"),
			wantDrift:   0.45,
			wantVerdict: focusVerdictStuck,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MeasureFocus(focusSamples(tc.specs...))
			if got.Drift != tc.wantDrift {
				t.Fatalf("drift = %v, want %v", got.Drift, tc.wantDrift)
			}
			if got.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
		})
	}
}

// TestMeasureFocusDoesNotModifyInput 检查 MeasureFocus 只读入参，调用前后样本切片逐字段一致。
func TestMeasureFocusDoesNotModifyInput(t *testing.T) {
	samples := focusSamples("user:原始内容", "tool:工具结果", "assistant:原始内容", "user:末条")
	snapshot := make([]FocusSample, len(samples))
	copy(snapshot, samples)
	_ = MeasureFocus(samples)
	if !reflect.DeepEqual(samples, snapshot) {
		t.Fatalf("input mutated: got %+v, want %+v", samples, snapshot)
	}
}

// TestMeasureFocusStable 检查重复调用产生逐字节一致的结果与摘要，
// 非法 UTF-8 内容不 panic 且结果稳定，未知等级标签在摘要中原样保留。
func TestMeasureFocusStable(t *testing.T) {
	samples := focusSamples(
		"user:目标",
		"assistant:重复",
		"tool:结果一",
		"assistant:重复",
		"tool:结果二",
	)
	want := MeasureFocus(samples)
	wantSummary := want.Summary()
	for i := 0; i < 32; i++ {
		got := MeasureFocus(samples)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: got %+v, want %+v", i, got, want)
		}
		if got.Summary() != wantSummary {
			t.Fatalf("call %d: summary = %q, want %q", i, got.Summary(), wantSummary)
		}
	}

	invalid := string([]byte{0xff, 0xfe, 'a', 0x80})
	withInvalid := []FocusSample{
		{Role: "assistant", Content: invalid, Turn: 1},
		{Role: "assistant", Content: invalid, Turn: 2},
	}
	gotInvalid := MeasureFocus(withInvalid)
	if gotInvalid.RepeatedContents != 1 {
		t.Fatalf("invalid UTF-8 content should still compare as equal bytes: %+v", gotInvalid)
	}
	for i := 0; i < 8; i++ {
		if !reflect.DeepEqual(MeasureFocus(withInvalid), gotInvalid) {
			t.Fatalf("invalid UTF-8 input gave unstable result at call %d", i)
		}
	}

	unknown := FocusReport{Verdict: "unknown", Turns: 1}
	if s := unknown.Summary(); !strings.Contains(s, "unknown") {
		t.Fatalf("unknown verdict should be preserved in summary: %q", s)
	}
}
