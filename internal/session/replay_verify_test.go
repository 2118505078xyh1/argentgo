package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// vfState 构造用于指纹/摘要测试的 ReplayState；无可变参数时未配对切片为 nil。
func vfState(leaf string, count int, complete bool, kind string, unpaired ...string) ReplayState {
	return ReplayState{
		ActiveLeaf:        leaf,
		RecordCount:       count,
		UnpairedToolCalls: unpaired,
		LastKind:          kind,
		Complete:          complete,
	}
}

func TestReplayFingerprintStable(t *testing.T) {
	tests := []struct {
		name string
		st   ReplayState
	}{
		{name: "空流", st: vfState("", 0, true, "")},
		{name: "完整线性流", st: vfState("f", 5, true, KindAssistant)},
		{name: "有未配对", st: vfState("end", 3, false, KindUser, "c1", "c2")},
		{name: "CJK 字段", st: vfState("叶子", 4, true, KindTool, "工具-调用")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneReplayState(tt.st)
			first := ReplayFingerprint(tt.st)
			second := ReplayFingerprint(tt.st)
			if first != second {
				t.Fatalf("同一状态重复计算指纹不稳定：%x != %x", first, second)
			}
			// 用深拷贝再算一次，确保指纹只取决于内容而非切片身份。
			if got := ReplayFingerprint(before); got != first {
				t.Fatalf("深拷贝的指纹不同：%x != %x", got, first)
			}
			if !reflect.DeepEqual(tt.st, before) {
				t.Fatalf("ReplayFingerprint 修改了入参：got %+v, before %+v", tt.st, before)
			}
		})
	}
}

func TestReplayFingerprintDistinguishesEachField(t *testing.T) {
	base := vfState("leaf", 3, true, KindAssistant, "c1")
	tests := []struct {
		name    string
		changed ReplayState
	}{
		{name: "ActiveLeaf", changed: vfState("other", 3, true, KindAssistant, "c1")},
		{name: "RecordCount", changed: vfState("leaf", 4, true, KindAssistant, "c1")},
		{name: "UnpairedToolCalls", changed: vfState("leaf", 3, true, KindAssistant, "c1", "c2")},
		{name: "LastKind", changed: vfState("leaf", 3, true, KindUser, "c1")},
		{name: "Complete", changed: vfState("leaf", 3, false, KindAssistant, "c1")},
	}

	want := ReplayFingerprint(base)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReplayFingerprint(tt.changed); got == want {
				t.Fatalf("改变 %s 后指纹未变化：%x", tt.name, got)
			}
		})
	}
}

// TestReplayFingerprintLengthPrefixDisambiguates 验证长度前缀消除字段拼接歧义：
// 没有前缀时 "a"+"bc" 与 "ab"+"c" 会拼成同一串，这里必须给出不同指纹。
func TestReplayFingerprintLengthPrefixDisambiguates(t *testing.T) {
	tests := []struct {
		name string
		a, b ReplayState
	}{
		{
			name: "ActiveLeaf 与 LastKind 的拼接歧义",
			a:    vfState("a", 2, true, "bc"),
			b:    vfState("ab", 2, true, "c"),
		},
		{
			name: "未配对元素之间的拼接歧义",
			a:    vfState("leaf", 2, false, KindAssistant, "a", "bc"),
			b:    vfState("leaf", 2, false, KindAssistant, "ab", "c"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ReplayFingerprint(tt.a) == ReplayFingerprint(tt.b) {
				t.Fatalf("歧义对撞成了同一指纹：%x", ReplayFingerprint(tt.a))
			}
		})
	}
}

// TestReplayFingerprintCanonicalUnpaired 验证规范化：未配对集合按字典序排序，
// 且 nil 与空切片视为同一状态。
func TestReplayFingerprintCanonicalUnpaired(t *testing.T) {
	tests := []struct {
		name string
		a, b ReplayState
	}{
		{
			name: "顺序无关",
			a:    vfState("leaf", 3, false, KindAssistant, "b", "a", "c"),
			b:    vfState("leaf", 3, false, KindAssistant, "c", "b", "a"),
		},
		{
			name: "nil 与空切片等价",
			a:    vfState("leaf", 1, true, KindUser),
			b:    vfState("leaf", 1, true, KindUser, []string{}...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ReplayFingerprint(tt.a) != ReplayFingerprint(tt.b) {
				t.Fatalf("规范化后指纹应相同：%x != %x", ReplayFingerprint(tt.a), ReplayFingerprint(tt.b))
			}
		})
	}
}

// TestReplayFingerprintDoesNotReorderInput 验证排序发生在副本上，不改入参切片。
func TestReplayFingerprintDoesNotReorderInput(t *testing.T) {
	st := vfState("leaf", 3, false, KindAssistant, "z", "a", "m")
	before := cloneReplayState(st)

	_ = ReplayFingerprint(st)
	if !reflect.DeepEqual(st, before) {
		t.Fatalf("ReplayFingerprint 改了入参顺序：got %+v, before %+v", st, before)
	}
	if st.UnpairedToolCalls[0] != "z" || st.UnpairedToolCalls[1] != "a" || st.UnpairedToolCalls[2] != "m" {
		t.Fatalf("入参切片被原地排序：%v", st.UnpairedToolCalls)
	}
}

func TestReplayFingerprintCJK(t *testing.T) {
	cjk := vfState("叶子", 4, false, KindAssistant, "工具-一", "工具-二")
	first := ReplayFingerprint(cjk)
	if first != ReplayFingerprint(cjk) {
		t.Fatal("CJK 字段指纹不稳定")
	}
	ascii := vfState("leaf", 4, false, KindAssistant, "c1", "c2")
	if first == ReplayFingerprint(ascii) {
		t.Fatal("CJK 与 ASCII 状态撞指纹")
	}
	// 内容相同但顺序不同仍应一致。
	swapped := vfState("叶子", 4, false, KindAssistant, "工具-二", "工具-一")
	if first != ReplayFingerprint(swapped) {
		t.Fatal("CJK 未配对顺序不应影响指纹")
	}
}

func TestVerifyReplayFingerprint(t *testing.T) {
	complete := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c1"),
		replayToolResult("t", "a", "c1"),
	}
	orphan := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayAssistant("a", "h", "c1", "c2"),
	}
	cjk := []Record{
		replayMsg("根", "", KindHeader, "根"),
		replayAssistant("助手", "根", "工具-调用"),
	}

	completeFP := ReplayFingerprint(Replay(complete))
	orphanFP := ReplayFingerprint(Replay(orphan))
	cjkFP := ReplayFingerprint(Replay(cjk))
	mismatchFP := completeFP
	mismatchFP[0] ^= 0xff

	tests := []struct {
		name    string
		records []Record
		want    [32]byte
		wantErr bool
	}{
		{name: "一致时通过", records: complete, want: completeFP},
		{name: "空流一致时通过", records: nil, want: ReplayFingerprint(Replay(nil))},
		{name: "CJK 流一致时通过", records: cjk, want: cjkFP},
		{name: "不一致时报错", records: complete, want: mismatchFP, wantErr: true},
		{name: "用另一流指纹校验报错", records: orphan, want: completeFP, wantErr: true},
		{name: "实际指纹与预期指纹对调仍报错", records: complete, want: orphanFP, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneRecords(tt.records)
			err := VerifyReplayFingerprint(tt.records, tt.want)
			if !reflect.DeepEqual(tt.records, before) {
				t.Fatalf("VerifyReplayFingerprint 修改了入参：got %+v, before %+v", tt.records, before)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("VerifyReplayFingerprint() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("VerifyReplayFingerprint() = nil, want error")
			}
			if !errors.Is(err, ErrReplayMismatch) {
				t.Fatalf("VerifyReplayFingerprint() = %v, want errors.Is ErrReplayMismatch", err)
			}
		})
	}
}

func TestReplaySummary(t *testing.T) {
	tests := []struct {
		name string
		st   ReplayState
		want string
	}{
		{
			name: "空流完整",
			st:   vfState("", 0, true, ""),
			want: "记录 0 条，未配对工具调用 0 个，会话完整",
		},
		{
			name: "完整线性流",
			st:   vfState("f", 5, true, KindAssistant),
			want: "记录 5 条，未配对工具调用 0 个，会话完整",
		},
		{
			name: "有未配对",
			st:   vfState("end", 3, false, KindUser, "c1", "c2"),
			want: "记录 3 条，未配对工具调用 2 个，会话不完整",
		},
		{
			name: "仅因环不完整",
			st:   vfState("c", 2, false, KindAssistant),
			want: "记录 2 条，未配对工具调用 0 个，会话不完整",
		},
		{
			name: "CJK 内容不影响摘要口径",
			st:   vfState("叶子", 4, false, KindAssistant, "工具-一"),
			want: "记录 4 条，未配对工具调用 1 个，会话不完整",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneReplayState(tt.st)
			first := ReplaySummary(tt.st)
			if first != tt.want {
				t.Fatalf("ReplaySummary() = %q, want %q", first, tt.want)
			}
			if second := ReplaySummary(tt.st); second != first {
				t.Fatalf("摘要不稳定：%q != %q", second, first)
			}
			if !reflect.DeepEqual(tt.st, before) {
				t.Fatalf("ReplaySummary 修改了入参：got %+v, before %+v", tt.st, before)
			}
		})
	}
}

// TestReplayVerifyFromReplayEndToEnd 验证整条纯函数链：Replay 的结果经
// ReplayFingerprint 投影后可被 VerifyReplayFingerprint 原样校验通过。
func TestReplayVerifyFromReplayEndToEnd(t *testing.T) {
	records := []Record{
		replayMsg("h", "", KindHeader, "根"),
		replayMsg("u", "h", KindUser, "你好"),
		replayAssistant("a", "u", "c1"),
		replayToolResult("t", "a", "c1"),
		replayMsg("f", "t", KindAssistant, "完成"),
	}
	before := cloneRecords(records)

	st := Replay(records)
	fp := ReplayFingerprint(st)
	if err := VerifyReplayFingerprint(records, fp); err != nil {
		t.Fatalf("端到端校验失败：%v", err)
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatalf("端到端链修改了入参：got %+v, before %+v", records, before)
	}

	// 原始数据里出现 Json RawMessage 反序列化差异时仍应稳定：用一份等价记录重放。
	equivalent := []Record{
		{ID: "h", Kind: KindHeader, Data: json.RawMessage(`"根"`)},
		{ID: "u", Parent: "h", Kind: KindUser, Data: json.RawMessage(`"你好"`)},
		{ID: "a", Parent: "u", Kind: KindAssistant, Data: json.RawMessage(`{"tool_calls":[{"id":"c1","name":"shell"}]}`)},
		{ID: "t", Parent: "a", Kind: KindTool, Data: json.RawMessage(`{"toolCallId":"c1","content":"ok"}`)},
		{ID: "f", Parent: "t", Kind: KindAssistant, Data: json.RawMessage(`"完成"`)},
	}
	if err := VerifyReplayFingerprint(equivalent, fp); err != nil {
		t.Fatalf("等价记录的指纹应一致：%v", err)
	}
}
