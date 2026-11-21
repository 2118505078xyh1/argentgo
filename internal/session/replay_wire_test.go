package session

import (
	"reflect"
	"testing"
)

// wireState 构造一个用于测试的 ReplayState，unpaired 为 nil 时表示无未配对调用。
func wireState(complete bool, unpaired ...string) ReplayState {
	if unpaired == nil {
		return ReplayState{Complete: complete, UnpairedToolCalls: nil}
	}
	return ReplayState{Complete: complete, UnpairedToolCalls: unpaired}
}

// cloneReplayState 深拷贝 ReplayState，用于断言入参未被修改。
func cloneReplayState(st ReplayState) ReplayState {
	out := st
	if st.UnpairedToolCalls != nil {
		out.UnpairedToolCalls = make([]string, len(st.UnpairedToolCalls))
		copy(out.UnpairedToolCalls, st.UnpairedToolCalls)
	}
	return out
}

func TestHintFromReplay(t *testing.T) {
	tests := []struct {
		name string
		st   ReplayState
		want RecoveryHint
	}{
		{
			name: "完整流无未配对",
			st:   ReplayState{Complete: true, UnpairedToolCalls: []string{}},
			want: RecoveryHint{HasUnpaired: false, UnpairedCount: 0, Complete: true, Text: "会话完整"},
		},
		{
			name: "nil 未配对视为完整",
			st:   wireState(true),
			want: RecoveryHint{HasUnpaired: false, UnpairedCount: 0, Complete: true, Text: "会话完整"},
		},
		{
			name: "单个未配对",
			st:   wireState(false, "c1"),
			want: RecoveryHint{HasUnpaired: true, UnpairedCount: 1, Complete: false, Text: "有 1 个未完成的工具调用"},
		},
		{
			name: "多个未配对",
			st:   wireState(false, "c1", "c2", "c3"),
			want: RecoveryHint{HasUnpaired: true, UnpairedCount: 3, Complete: false, Text: "有 3 个未完成的工具调用"},
		},
		{
			name: "仅因环而不完整且无未配对时公告仍为会话完整",
			st:   ReplayState{ActiveLeaf: "c", Complete: false, UnpairedToolCalls: []string{}},
			want: RecoveryHint{HasUnpaired: false, UnpairedCount: 0, Complete: false, Text: "会话完整"},
		},
		{
			name: "CJK 工具调用 id 只影响计数不影响文本稳定",
			st:   wireState(false, "工具-未回-一", "工具-未回-二"),
			want: RecoveryHint{HasUnpaired: true, UnpairedCount: 2, Complete: false, Text: "有 2 个未完成的工具调用"},
		},
		{
			name: "空流",
			st:   ReplayState{Complete: true, UnpairedToolCalls: []string{}},
			want: RecoveryHint{HasUnpaired: false, UnpairedCount: 0, Complete: true, Text: "会话完整"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneReplayState(tt.st)
			got := HintFromReplay(tt.st)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("HintFromReplay() = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.st, before) {
				t.Fatalf("HintFromReplay 修改了入参：got %+v, before %+v", tt.st, before)
			}
		})
	}
}

// TestHintFromReplayTextIsStableCJK 验证公告文本是稳定的中文，且重复调用结果一致。
func TestHintFromReplayTextIsStableCJK(t *testing.T) {
	complete := wireState(true)
	incomplete := wireState(false, "c1", "c2")

	if got := HintFromReplay(complete).Text; got != "会话完整" {
		t.Fatalf("完整公告 = %q, want %q", got, "会话完整")
	}
	if got := HintFromReplay(incomplete).Text; got != "有 2 个未完成的工具调用" {
		t.Fatalf("不完整公告 = %q, want %q", got, "有 2 个未完成的工具调用")
	}
	// 同一入参重复调用必须得到完全相同的结构。
	if first, second := HintFromReplay(incomplete), HintFromReplay(incomplete); !reflect.DeepEqual(first, second) {
		t.Fatalf("公告不稳定：first %+v, second %+v", first, second)
	}
}

// TestHintFromReplayDoesNotAliasInput 验证 hint 不持有原切片的底层数组：
// 改动原 ReplayState 的未配对切片不会改变已生成的公告字段。
func TestHintFromReplayDoesNotAliasInput(t *testing.T) {
	st := wireState(false, "c1", "c2")
	hint := HintFromReplay(st)

	st.UnpairedToolCalls[0] = "被篡改"
	if hint.UnpairedCount != 2 || hint.Text != "有 2 个未完成的工具调用" || !hint.HasUnpaired {
		t.Fatalf("改动原切片影响了已生成公告：%+v", hint)
	}
}

func TestCanAutoContinue(t *testing.T) {
	tests := []struct {
		name            string
		st              ReplayState
		allowIncomplete bool
		want            bool
	}{
		{name: "完整且不允许不完整", st: wireState(true), allowIncomplete: false, want: true},
		{name: "完整且允许不完整", st: wireState(true), allowIncomplete: true, want: true},
		{name: "环不完整且不允许", st: wireState(false), allowIncomplete: false, want: false},
		{name: "环不完整但允许", st: wireState(false), allowIncomplete: true, want: true},
		{name: "有未配对且不允许", st: wireState(false, "c1"), allowIncomplete: false, want: false},
		{name: "有未配对但允许", st: wireState(false, "c1"), allowIncomplete: true, want: true},
		{name: "未配对状态不允许不完整仍不自动继续", st: wireState(false, "c1", "c2"), allowIncomplete: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneReplayState(tt.st)
			if got := CanAutoContinue(tt.st, tt.allowIncomplete); got != tt.want {
				t.Fatalf("CanAutoContinue() = %t, want %t", got, tt.want)
			}
			if !reflect.DeepEqual(tt.st, before) {
				t.Fatalf("CanAutoContinue 修改了入参：got %+v, before %+v", tt.st, before)
			}
		})
	}
}

// TestAllowIncompleteDoesNotHideUnpaired 是核心不变量：allowIncomplete 只放宽
// "是否自动继续"，绝不把未配对调用当作真实应答。即使允许继续，投影出的
// 公告仍如实报告未配对、非完整，文本也不会变成"会话完整"。
func TestAllowIncompleteDoesNotHideUnpaired(t *testing.T) {
	st := ReplayState{
		ActiveLeaf:        "a",
		RecordCount:       2,
		UnpairedToolCalls: []string{"c1", "c2"},
		LastKind:          "assistant",
		Complete:          false,
	}

	if !CanAutoContinue(st, true) {
		t.Fatal("允许不完整时应自动继续")
	}
	hint := HintFromReplay(st)
	if !hint.HasUnpaired || hint.UnpairedCount != 2 {
		t.Fatalf("允许不完整后未配对调用被隐藏：%+v", hint)
	}
	if hint.Complete {
		t.Fatal("允许不完整后不应把未配对状态标记为完整")
	}
	if hint.Text != "有 2 个未完成的工具调用" {
		t.Fatalf("公告被改写为 %q，不应视为真实应答", hint.Text)
	}
}

// TestReplayToHintEndToEnd 用 Replay 的结果驱动公告，验证整条纯适配链：
// 完整流给出"会话完整"，孤儿工具调用给出计数公告，且不修改输入记录。
func TestReplayToHintEndToEnd(t *testing.T) {
	completeRecords := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c1"),
		replayToolResult("t", "a", "c1"),
	}
	incompleteRecords := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c1", "c2"),
	}

	for _, tt := range []struct {
		name       string
		records    []Record
		wantText   string
		wantUnpair int
		wantAuto   bool
	}{
		{name: "完整流", records: completeRecords, wantText: "会话完整", wantUnpair: 0, wantAuto: true},
		{name: "孤儿调用流", records: incompleteRecords, wantText: "有 2 个未完成的工具调用", wantUnpair: 2, wantAuto: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneRecords(tt.records)
			st := Replay(tt.records)
			hint := HintFromReplay(st)
			if hint.Text != tt.wantText || hint.UnpairedCount != tt.wantUnpair {
				t.Fatalf("HintFromReplay(Replay()) = %+v, want text %q count %d", hint, tt.wantText, tt.wantUnpair)
			}
			if got := CanAutoContinue(st, false); got != tt.wantAuto {
				t.Fatalf("CanAutoContinue() = %t, want %t", got, tt.wantAuto)
			}
			if !reflect.DeepEqual(tt.records, before) {
				t.Fatalf("重放到公告链修改了入参：got %+v, before %+v", tt.records, before)
			}
		})
	}
}
