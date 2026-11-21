package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRebuildSummaryWithWiresNilCallbacks 验证三个回调全为 nil 时逐字节返回 rebuilt。
func TestRebuildSummaryWithWiresNilCallbacks(t *testing.T) {
	in := RebuildInput{
		Compaction:      Compaction{Summary: "压缩记录摘要", Dropped: 3},
		OriginalSummary: "原始摘要",
		Dropped:         []Entry{{Role: "user", Content: "你好"}},
	}
	for _, rebuilt := range []string{"", "重建摘要", "## Goal\n- 目标\n多行\n"} {
		if got := RebuildSummaryWithWires(rebuilt, in, nil, nil, nil); got != rebuilt {
			t.Errorf("全 nil 回调应逐字节返回 rebuilt: got %q, want %q", got, rebuilt)
		}
	}
}

// TestRebuildSummaryWithWiresCallbackCombos 穷举三回调 nil/非 nil 的 8 种组合，
// 验证每种组合只调用非 nil 回调，且顺序与结果都符合固定编排规则。
func TestRebuildSummaryWithWiresCallbackCombos(t *testing.T) {
	const rebuilt = "R"
	orig := "O"
	dropped := []Entry{{Role: "user", Content: "D"}}
	for mask := 0; mask < 8; mask++ {
		useP := mask&1 != 0
		useE := mask&2 != 0
		useF := mask&4 != 0

		var calls []string
		var preserve func(string, string) string
		var enrich func(string, []Entry) string
		var fidelity func(string) string
		if useP {
			preserve = func(s, o string) string {
				calls = append(calls, "p")
				return s + "P" + o
			}
		}
		if useE {
			enrich = func(s string, d []Entry) string {
				calls = append(calls, "e")
				return s + "E" + d[0].Content
			}
		}
		if useF {
			fidelity = func(string) string {
				calls = append(calls, "f")
				return "WARN"
			}
		}

		got := RebuildSummaryWithWires(rebuilt, RebuildInput{
			OriginalSummary: orig,
			Dropped:         dropped,
		}, preserve, enrich, fidelity)

		var wantCalls []string
		want := rebuilt
		if useP {
			wantCalls = append(wantCalls, "p")
			want += "P" + orig
		}
		if useE {
			wantCalls = append(wantCalls, "e")
			want += "E" + dropped[0].Content
		}
		if useF {
			wantCalls = append(wantCalls, "f")
			want += "\nWARN"
		}
		if !reflect.DeepEqual(calls, wantCalls) {
			t.Errorf("mask=%d: 调用顺序 = %v, want %v", mask, calls, wantCalls)
		}
		if got != want {
			t.Errorf("mask=%d: 结果 = %q, want %q", mask, got, want)
		}
	}
}

// TestRebuildSummaryWithWiresFixedOrderAndArguments 验证每个回调收到的都是上一步的
// 输出，并且 orig / dropped 被正确透传；同时验证告警被追加在最后。
func TestRebuildSummaryWithWiresFixedOrderAndArguments(t *testing.T) {
	in := RebuildInput{
		OriginalSummary: "ORIG",
		Dropped:         []Entry{{Role: "assistant", Content: "DROP"}},
	}
	got := RebuildSummaryWithWires("BASE", in,
		func(s, orig string) string {
			if s != "BASE" || orig != "ORIG" {
				t.Errorf("preserve 入参错误: s=%q orig=%q", s, orig)
			}
			return s + "-P"
		},
		func(s string, d []Entry) string {
			if s != "BASE-P" || len(d) != 1 || d[0].Content != "DROP" {
				t.Errorf("enrich 入参错误: s=%q d=%v", s, d)
			}
			return s + "-E"
		},
		func(s string) string {
			if s != "BASE-P-E" {
				t.Errorf("fidelity 入参错误: s=%q", s)
			}
			return "告警"
		},
	)
	if want := "BASE-P-E\n告警"; got != want {
		t.Errorf("结果 = %q, want %q", got, want)
	}
}

// TestRebuildSummaryWithWiresFidelityEmptyWarning 验证 fidelity 返回空串时不追加任何内容。
func TestRebuildSummaryWithWiresFidelityEmptyWarning(t *testing.T) {
	got := RebuildSummaryWithWires("摘要", RebuildInput{}, nil, nil, func(string) string { return "" })
	if got != "摘要" {
		t.Errorf("空告警不应追加: got %q", got)
	}
}

// TestRebuildSummaryWithWiresCJK 验证 CJK / emoji 内容全程逐字保留且结果 UTF-8 合法。
func TestRebuildSummaryWithWiresCJK(t *testing.T) {
	in := RebuildInput{
		Compaction:      Compaction{Summary: "压缩后中文摘要"},
		OriginalSummary: "## 目标\n- 实现中文功能\n",
		Dropped:         []Entry{{Role: "tool", Content: "失败信息：无法解析 🚀"}},
	}
	got := RebuildSummaryWithWires("重建🚀摘要", in,
		func(s, orig string) string { return s + "\n" + orig },
		func(s string, d []Entry) string { return s + "\n" + d[0].Content },
		func(string) string { return "保真告警：缺少下一步 🐉" },
	)
	if !utf8.ValidString(got) {
		t.Error("结果不是合法的 UTF-8，可能被按字节截断")
	}
	for _, want := range []string{
		"重建🚀摘要", "实现中文功能", "失败信息：无法解析 🚀", "保真告警：缺少下一步 🐉",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("结果缺少 %q: %q", want, got)
		}
	}
}

// TestRebuildSummaryWithWiresDoesNotMutateInputs 验证编排函数不改动入参（含切片与字符串）。
func TestRebuildSummaryWithWiresDoesNotMutateInputs(t *testing.T) {
	in := RebuildInput{
		Compaction: Compaction{
			Summary: "S", Dropped: 2, Keep: 1,
			Files:     []string{"a.go"},
			Failures:  []string{"f"},
			NextSteps: []string{"n"},
		},
		OriginalSummary: "O",
		Dropped:         []Entry{{Role: "tool", Content: "D", Data: []byte(`{"is_error":true}`)}},
	}
	inCopy := RebuildInput{
		Compaction: Compaction{
			Summary: in.Compaction.Summary, Dropped: in.Compaction.Dropped, Keep: in.Compaction.Keep,
			Files:     append([]string(nil), in.Compaction.Files...),
			Failures:  append([]string(nil), in.Compaction.Failures...),
			NextSteps: append([]string(nil), in.Compaction.NextSteps...),
		},
		OriginalSummary: in.OriginalSummary,
		Dropped:         append([]Entry(nil), in.Dropped...),
	}
	rebuilt := "REBUILT"
	rebuiltCopy := rebuilt

	_ = RebuildSummaryWithWires(rebuilt, in,
		func(s, orig string) string { return s + orig },
		func(s string, d []Entry) string { return s + d[0].Content },
		func(string) string { return "W" },
	)

	if !reflect.DeepEqual(in, inCopy) {
		t.Errorf("入参被改动: got %#v, want %#v", in, inCopy)
	}
	if rebuilt != rebuiltCopy {
		t.Errorf("rebuilt 被改动: %q", rebuilt)
	}
}

// TestRebuildSummaryWithWiresIdempotent 验证当 wire 幂等时整体幂等；全 nil 回调天然幂等。
func TestRebuildSummaryWithWiresIdempotent(t *testing.T) {
	const warning = "保真告警：缺少失败"
	in := RebuildInput{
		OriginalSummary: "## 目标\n- 原始目标\n",
		Dropped:         []Entry{{Role: "assistant", Content: "隐式上下文"}},
	}
	// 模拟真实 wire：靠内容标记去重，重复施加不会产生新内容。
	preserve := func(s, orig string) string {
		if strings.Contains(s, orig) {
			return s
		}
		return s + "\n" + orig
	}
	enrich := func(s string, d []Entry) string {
		if strings.Contains(s, d[0].Content) {
			return s
		}
		return s + "\n" + d[0].Content
	}
	fidelity := func(s string) string {
		if strings.Contains(s, warning) {
			return ""
		}
		return warning
	}

	once := RebuildSummaryWithWires("重建摘要", in, preserve, enrich, fidelity)
	twice := RebuildSummaryWithWires(once, in, preserve, enrich, fidelity)
	if once != twice {
		t.Errorf("非幂等:\nonce =\n%q\ntwice =\n%q", once, twice)
	}

	nilOnce := RebuildSummaryWithWires("重建摘要", in, nil, nil, nil)
	nilTwice := RebuildSummaryWithWires(nilOnce, in, nil, nil, nil)
	if nilOnce != nilTwice {
		t.Errorf("全 nil 回调非幂等: once=%q twice=%q", nilOnce, nilTwice)
	}
}

// TestRebuildSummaryWithWiresEmptyInput 验证零值输入的边界行为。
func TestRebuildSummaryWithWiresEmptyInput(t *testing.T) {
	var in RebuildInput
	if got := RebuildSummaryWithWires("", in, nil, nil, nil); got != "" {
		t.Errorf("零值输入 + nil 回调应返回空串, got %q", got)
	}
	got := RebuildSummaryWithWires("", in,
		func(s, orig string) string { return s + "P:" + orig },
		func(s string, d []Entry) string { return s + "E:" + strings.Repeat("x", len(d)) },
		func(string) string { return "W" },
	)
	if want := "P:E:\nW"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
