package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// suffixMsg 构造一条普通文本记录。
func suffixMsg(id, parent, kind, text string) session.Record {
	data, _ := json.Marshal(text)
	return session.Record{ID: id, Parent: parent, Kind: kind, Data: data}
}

// suffixAssistant 构造一条声明若干 tool_calls 的 assistant 记录。
func suffixAssistant(id, parent string, callIDs ...string) session.Record {
	calls := make([]map[string]string, 0, len(callIDs))
	for _, callID := range callIDs {
		calls = append(calls, map[string]string{"id": callID, "name": "shell"})
	}
	data, _ := json.Marshal(map[string]any{"content": "调用工具", "tool_calls": calls})
	return session.Record{ID: id, Parent: parent, Kind: session.KindAssistant, Data: data}
}

// suffixToolResult 构造一条携带 ToolCallID 的工具结果记录。
func suffixToolResult(id, parent, callID string) session.Record {
	data, _ := json.Marshal(map[string]any{"toolCallId": callID, "content": "结果"})
	return session.Record{ID: id, Parent: parent, Kind: session.KindTool, Data: data}
}

// cloneReplayRecords 深拷贝记录（含 Data 底层字节），用于断言入参未被修改。
func cloneReplayRecords(records []session.Record) []session.Record {
	if records == nil {
		return nil
	}
	out := make([]session.Record, len(records))
	for i, r := range records {
		out[i] = r
		if r.Data != nil {
			out[i].Data = make([]byte, len(r.Data))
			copy(out[i].Data, r.Data)
		}
	}
	return out
}

// TestReplayNoticeSuffixEmptyAndComplete 覆盖所有应返回空串的情形：
// 完整流、空流（nil 与空切片）、结果出现在调用之前的配对流。返回空串保证
// 历史公告字节与 NeedsConfirmation 不受影响。
func TestReplayNoticeSuffixEmptyAndComplete(t *testing.T) {
	cases := []struct {
		name    string
		records []session.Record
	}{
		{name: "nil 空流", records: nil},
		{name: "空切片流", records: []session.Record{}},
		{
			name: "线性流工具已配对",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixMsg("u", "h", session.KindUser, "你好"),
				suffixAssistant("a", "u", "c1"),
				suffixToolResult("t", "a", "c1"),
				suffixMsg("f", "t", session.KindAssistant, "完成"),
			},
		},
		{
			name: "结果出现在调用之前仍算配对",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixToolResult("t", "h", "early"),
				suffixAssistant("a", "t", "early"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayNoticeSuffix(tc.records); got != "" {
				t.Fatalf("replayNoticeSuffix() = %q, want 空串", got)
			}
		})
	}
}

// TestReplayNoticeSuffixUnpaired 验证未配对工具调用会产生确定性中文后缀，
// 含未配对数量、完整=false 与活跃叶子 id。
func TestReplayNoticeSuffixUnpaired(t *testing.T) {
	cases := []struct {
		name      string
		records   []session.Record
		wantCount string
		wantLeaf  string
	}{
		{
			name: "单个未配对",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixAssistant("a", "h", "c1"),
			},
			wantCount: "1 个未完成的工具调用",
			wantLeaf:  "叶子=a",
		},
		{
			name: "多个未配对按首次出现计数",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixAssistant("a", "h", "c1", "c2", "c2"),
				suffixToolResult("t", "a", "c1"),
				suffixAssistant("b", "t", "c3"),
				suffixMsg("end", "b", session.KindUser, "结束"),
			},
			wantCount: "2 个未完成的工具调用",
			wantLeaf:  "叶子=end",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replayNoticeSuffix(tc.records)
			for _, want := range []string{" (重放审计：", tc.wantCount, "完整=false", tc.wantLeaf, ")"} {
				if !strings.Contains(got, want) {
					t.Errorf("后缀 %q 缺少 %q", got, want)
				}
			}
			if !strings.HasPrefix(got, " (重放审计：") || !strings.HasSuffix(got, ")") {
				t.Errorf("后缀应有前导空格与括号包裹：%q", got)
			}
		})
	}
}

// TestReplayNoticeSuffixCycle 验证 parent 环导致的不完整流也会产生后缀，
// 此时未配对数为 0 但完整位为 false，叶子 id 仍然可见。
func TestReplayNoticeSuffixCycle(t *testing.T) {
	records := []session.Record{
		{ID: "b", Parent: "c", Kind: session.KindAssistant, Data: json.RawMessage(`"b"`)},
		{ID: "c", Parent: "b", Kind: session.KindAssistant, Data: json.RawMessage(`"c"`)},
	}
	got := replayNoticeSuffix(records)
	for _, want := range []string{"0 个未完成的工具调用", "完整=false", "叶子=c"} {
		if !strings.Contains(got, want) {
			t.Errorf("环后缀 %q 缺少 %q", got, want)
		}
	}
}

// TestReplayNoticeSuffixCJKAndBounded 验证 CJK 叶子 id 用 rune 有界截断：
// 结果始终是合法 UTF-8，不会切在汉字中间，且长度有上界（截断后用省略号）。
func TestReplayNoticeSuffixCJKAndBounded(t *testing.T) {
	longCJK := strings.Repeat("工", 40)
	records := []session.Record{
		suffixMsg("根", "", session.KindHeader, "根"),
		suffixAssistant("助手", "根", "工具-未回"),
		suffixMsg(longCJK, "助手", session.KindAssistant, "末尾"),
	}
	got := replayNoticeSuffix(records)
	if !utf8.ValidString(got) {
		t.Fatalf("含 CJK id 的后缀不是合法 UTF-8：%q", got)
	}
	if !strings.Contains(got, strings.Repeat("工", replaySuffixIDRunes)+"…") {
		t.Fatalf("CJK id 未按 rune 有界截断：%q", got)
	}
	if strings.Contains(got, strings.Repeat("工", replaySuffixIDRunes+1)) {
		t.Fatalf("CJK id 截断后仍超过上限：%q", got)
	}
	if n := utf8.RuneCountInString(got); n > 128 {
		t.Fatalf("后缀长度 %d rune 超过上界：%q", n, got)
	}
}

// TestBoundedReplayID 直接覆盖截断边界：恰好上限原样返回，超长按 rune 截断，
// 短 CJK 不截断且保持合法 UTF-8。
func TestBoundedReplayID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{name: "短 id 原样", id: "abc", want: "abc"},
		{name: "恰好上限原样", id: strings.Repeat("x", replaySuffixIDRunes), want: strings.Repeat("x", replaySuffixIDRunes)},
		{name: "超长 ASCII 截断", id: strings.Repeat("x", replaySuffixIDRunes+5), want: strings.Repeat("x", replaySuffixIDRunes) + "…"},
		{name: "短 CJK 不截断", id: "工具一", want: "工具一"},
		{name: "超长 CJK 按 rune 截断", id: strings.Repeat("工", replaySuffixIDRunes+3), want: strings.Repeat("工", replaySuffixIDRunes) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boundedReplayID(tc.id)
			if got != tc.want {
				t.Fatalf("boundedReplayID(%q) = %q, want %q", tc.id, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("boundedReplayID(%q) 非法 UTF-8：%q", tc.id, got)
			}
		})
	}
}

// TestReplayNoticeSuffixDeterministic 验证纯函数确定性：同一入参重复调用逐字节一致，
// 且未配对声明顺序不同的等价流（计数相同）得到相同后缀——后缀只依赖确定性聚合值。
func TestReplayNoticeSuffixDeterministic(t *testing.T) {
	records := []session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c1", "c2"),
		suffixMsg("end", "a", session.KindUser, "结束"),
	}
	first := replayNoticeSuffix(records)
	for i := 0; i < 3; i++ {
		if got := replayNoticeSuffix(records); got != first {
			t.Fatalf("后缀不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}

	// 未配对 id 的声明顺序被 Replay 去重后只以计数进入后缀，故结果一致。
	other := []session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c2", "c1"),
		suffixMsg("end", "a", session.KindUser, "结束"),
	}
	if got := replayNoticeSuffix(other); got != first {
		t.Fatalf("等价流后缀应一致：first %q, other %q", first, got)
	}
}

// TestReplayNoticeSuffixDoesNotMutateInput 验证纯函数不改入参（含 Data 底层字节）。
func TestReplayNoticeSuffixDoesNotMutateInput(t *testing.T) {
	records := []session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c1", "c2"),
		suffixToolResult("t", "a", "c1"),
	}
	before := cloneReplayRecords(records)
	_ = replayNoticeSuffix(records)
	if !reflect.DeepEqual(records, before) {
		t.Fatalf("replayNoticeSuffix 修改了入参：got %+v, before %+v", records, before)
	}
}

// TestReplayRecoveryEnabled 覆盖 env 门所有取值：默认开启，
// 仅 ARGENT_RECOVERY_REPLAY=0（允许首尾空白）时紧急关闭。
func TestReplayRecoveryEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{name: "nil 读取器默认开启", env: nil, want: true},
		{name: "缺失默认开启", env: envFrom(map[string]string{}), want: true},
		{name: "空值默认开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": ""}), want: true},
		{name: "0 关闭", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "0"}), want: false},
		{name: "带空白的 0 关闭", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "\t 0 \n"}), want: false},
		{name: "true 保持开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "true"}), want: true},
		{name: "01 保持开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "01"}), want: true},
		{name: "1 开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "1"}), want: true},
		{name: "首尾空格的 1 开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": " 1 "}), want: true},
		{name: "制表换行的 1 开启", env: envFrom(map[string]string{"ARGENT_RECOVERY_REPLAY": "\t1\n"}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayRecoveryEnabled(tc.env); got != tc.want {
				t.Fatalf("replayRecoveryEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}
