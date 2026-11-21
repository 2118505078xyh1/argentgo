package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// replayAssistant 构造一条声明若干 tool_calls 的 assistant 记录。
func replayAssistant(id, parent string, callIDs ...string) Record {
	calls := make([]map[string]string, 0, len(callIDs))
	for _, callID := range callIDs {
		calls = append(calls, map[string]string{"id": callID, "name": "shell"})
	}
	data, _ := json.Marshal(map[string]any{
		"content":    "调用工具",
		"name":       "shell",
		"tool_calls": calls,
	})
	return Record{ID: id, Parent: parent, Kind: KindAssistant, Data: data}
}

// replayToolResult 构造一条携带 ToolCallID 的工具结果记录。
func replayToolResult(id, parent, callID string) Record {
	data, _ := json.Marshal(map[string]any{"toolCallId": callID, "content": "结果"})
	return Record{ID: id, Parent: parent, Kind: KindTool, Data: data}
}

// replayMsg 构造一条普通文本记录。
func replayMsg(id, parent, kind, text string) Record {
	data, _ := json.Marshal(text)
	return Record{ID: id, Parent: parent, Kind: kind, Data: data}
}

func TestReplay(t *testing.T) {
	tests := []struct {
		name    string
		records []Record
		want    ReplayState
	}{
		{
			name: "线性流且工具已配对",
			records: []Record{
				replayMsg("h", "", KindHeader, "根"),
				replayMsg("u", "h", KindUser, "你好"),
				replayAssistant("a", "u", "c1"),
				replayToolResult("t", "a", "c1"),
				replayMsg("f", "t", KindAssistant, "完成"),
			},
			want: ReplayState{
				ActiveLeaf:        "f",
				RecordCount:       5,
				UnpairedToolCalls: []string{},
				LastKind:          KindAssistant,
				Complete:          true,
			},
		},
		{
			name: "分叉取最后一条为活跃叶子并按整段流配对",
			records: []Record{
				replayMsg("h", "", KindHeader, "根"),
				replayMsg("u", "h", KindUser, "分叉点"),
				replayAssistant("a1", "u", "c1"),
				replayToolResult("t1", "a1", "c1"),
				replayAssistant("b1", "u", "c2"),
				replayToolResult("t2", "b1", "c2"),
				replayMsg("b2", "t2", KindUser, "另一分支末尾"),
			},
			want: ReplayState{
				ActiveLeaf:        "b2",
				RecordCount:       7,
				UnpairedToolCalls: []string{},
				LastKind:          KindUser,
				Complete:          true,
			},
		},
		{
			name: "孤儿工具调用按首次出现顺序去重",
			records: []Record{
				replayMsg("h", "", KindHeader, "根"),
				replayAssistant("a", "h", "c1", "c2", "c2"),
				replayToolResult("t", "a", "c1"),
				replayAssistant("b", "t", "c3"),
				replayMsg("end", "b", KindUser, "结束"),
			},
			want: ReplayState{
				ActiveLeaf:        "end",
				RecordCount:       5,
				UnpairedToolCalls: []string{"c2", "c3"},
				LastKind:          KindUser,
				Complete:          false,
			},
		},
		{
			name: "结果出现在调用之前仍算已配对",
			records: []Record{
				replayMsg("h", "", KindHeader, "根"),
				replayToolResult("t", "h", "early"),
				replayMsg("mid", "t", KindUser, "中间"),
				replayAssistant("a", "mid", "early"),
			},
			want: ReplayState{
				ActiveLeaf:        "a",
				RecordCount:       4,
				UnpairedToolCalls: []string{},
				LastKind:          KindAssistant,
				Complete:          true,
			},
		},
		{
			name: "parent 链成环",
			records: []Record{
				Record{ID: "b", Parent: "c", Kind: KindAssistant, Data: json.RawMessage(`"b"`)},
				Record{ID: "c", Parent: "b", Kind: KindAssistant, Data: json.RawMessage(`"c"`)},
			},
			want: ReplayState{
				ActiveLeaf:        "c",
				RecordCount:       2,
				UnpairedToolCalls: []string{},
				LastKind:          KindAssistant,
				Complete:          false,
			},
		},
		{
			name: "CJK id 与 CJK 工具调用 id",
			records: []Record{
				replayMsg("根", "", KindHeader, "根"),
				replayMsg("用户一", "根", KindUser, "中文提问"),
				replayAssistant("助手一", "用户一", "工具-调用-一"),
				replayToolResult("结果一", "助手一", "工具-调用-一"),
			},
			want: ReplayState{
				ActiveLeaf:        "结果一",
				RecordCount:       4,
				UnpairedToolCalls: []string{},
				LastKind:          KindTool,
				Complete:          true,
			},
		},
		{
			name: "CJK 孤儿工具调用被保留",
			records: []Record{
				replayMsg("根", "", KindHeader, "根"),
				replayAssistant("助手", "根", "工具-未回-一"),
			},
			want: ReplayState{
				ActiveLeaf:        "助手",
				RecordCount:       2,
				UnpairedToolCalls: []string{"工具-未回-一"},
				LastKind:          KindAssistant,
				Complete:          false,
			},
		},
		{
			name:    "nil 空流完整",
			records: nil,
			want: ReplayState{
				ActiveLeaf:        "",
				RecordCount:       0,
				UnpairedToolCalls: []string{},
				LastKind:          "",
				Complete:          true,
			},
		},
		{
			name:    "空切片流完整",
			records: []Record{},
			want: ReplayState{
				ActiveLeaf:        "",
				RecordCount:       0,
				UnpairedToolCalls: []string{},
				LastKind:          "",
				Complete:          true,
			},
		},
		{
			name: "旧版单 tool_call_id 也会配对",
			records: []Record{
				replayMsg("h", "", KindHeader, "根"),
				Record{ID: "a", Parent: "h", Kind: KindAssistant, Data: json.RawMessage(`{"tool_call_id":"legacy"}`)},
				Record{ID: "t", Parent: "a", Kind: KindTool, Data: json.RawMessage(`{"tool_call_id":"legacy","content":"ok"}`)},
			},
			want: ReplayState{
				ActiveLeaf:        "t",
				RecordCount:       3,
				UnpairedToolCalls: []string{},
				LastKind:          KindTool,
				Complete:          true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneRecords(tt.records)
			got := Replay(tt.records)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Replay() = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.records, before) {
				t.Fatalf("Replay 修改了入参：got %+v, before %+v", tt.records, before)
			}
		})
	}
}

// TestReplayReturnedSliceIsFresh 验证返回的未配对切片不与任何内部状态共享，
// 改动它不会影响下一次 Replay 的结果，也不会污染入参。
func TestReplayReturnedSliceIsFresh(t *testing.T) {
	records := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c1"),
	}
	before := cloneRecords(records)
	first := Replay(records)
	if len(first.UnpairedToolCalls) != 1 || first.UnpairedToolCalls[0] != "c1" {
		t.Fatalf("Replay() unpaired = %v, want [c1]", first.UnpairedToolCalls)
	}
	first.UnpairedToolCalls[0] = "被篡改"
	second := Replay(records)
	if len(second.UnpairedToolCalls) != 1 || second.UnpairedToolCalls[0] != "c1" {
		t.Fatalf("篡改返回切片影响了后续 Replay：%v", second.UnpairedToolCalls)
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatalf("篡改返回切片修改了入参：got %+v, before %+v", records, before)
	}
}

func TestVerifyReplay(t *testing.T) {
	valid := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayMsg("u", "h", KindUser, "你好"),
		replayAssistant("a", "u", "c1"),
		replayToolResult("t", "a", "c1"),
		replayMsg("f", "t", KindAssistant, "完成"),
	}
	validState := Replay(valid)

	orphan := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c9"),
		replayMsg("end", "a", KindUser, "结束"),
	}
	orphanExpected := ReplayState{
		ActiveLeaf:        "end",
		RecordCount:       3,
		UnpairedToolCalls: []string{}, // 实际是 ["c9"]，制造未配对偏差
		LastKind:          KindUser,
		Complete:          true,
	}

	cycle := []Record{
		Record{ID: "b", Parent: "c", Kind: KindAssistant, Data: json.RawMessage(`"b"`)},
		Record{ID: "c", Parent: "b", Kind: KindAssistant, Data: json.RawMessage(`"c"`)},
	}
	// 预期与实际状态逐字段相同，仅靠环本身触发 ErrCycle。
	cycleExpected := Replay(cycle)

	driftExpected := validState
	driftExpected.ActiveLeaf = "漂移的叶子"

	countExpected := validState
	countExpected.RecordCount++

	tests := []struct {
		name         string
		records      []Record
		expected     ReplayState
		wantSentinel error
		wantPlainErr bool
	}{
		{name: "状态一致时通过", records: valid, expected: validState},
		{name: "空流一致时通过", records: nil, expected: Replay(nil)},
		{name: "未配对工具调用", records: orphan, expected: orphanExpected, wantSentinel: ErrUnpairedTool},
		{name: "环", records: cycle, expected: cycleExpected, wantSentinel: ErrCycle},
		{name: "活跃叶子漂移", records: valid, expected: driftExpected, wantSentinel: ErrLeafDrift},
		{name: "其他字段漂移报普通错误", records: valid, expected: countExpected, wantPlainErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneRecords(tt.records)
			err := VerifyReplay(tt.records, tt.expected)
			if !reflect.DeepEqual(tt.records, before) {
				t.Fatalf("VerifyReplay 修改了入参：got %+v, before %+v", tt.records, before)
			}
			if tt.wantSentinel == nil && !tt.wantPlainErr {
				if err != nil {
					t.Fatalf("VerifyReplay() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifyReplay() = nil, want error")
			}
			if tt.wantSentinel != nil && !errors.Is(err, tt.wantSentinel) {
				t.Fatalf("VerifyReplay() = %v, want errors.Is %v", err, tt.wantSentinel)
			}
			if tt.wantPlainErr {
				for _, sentinel := range []error{ErrUnpairedTool, ErrCycle, ErrLeafDrift} {
					if errors.Is(err, sentinel) {
						t.Fatalf("VerifyReplay() = %v, 不应命中哨兵 %v", err, sentinel)
					}
				}
			}
		})
	}
}
