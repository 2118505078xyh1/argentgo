package app

import (
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// TestFromAgentEventProjections 逐字段核对投影结果，覆盖文本、工具、错误与零值事件。
func TestFromAgentEventProjections(t *testing.T) {
	tests := []struct {
		name string
		ev   agent.Event
		seq  uint64
		want SurfaceEvent
	}{
		{
			name: "text delta",
			ev: agent.Event{
				Kind:        agent.EventTextDelta,
				Turn:        3,
				Depth:       0,
				Text:        "你好，世界",
				StartedAtMS: 1000,
			},
			seq: 7,
			want: SurfaceEvent{
				Kind:  "text_delta",
				Seq:   7,
				AtMS:  1000,
				Text:  "你好，世界",
				Extra: map[string]string{},
			},
		},
		{
			name: "tool start",
			ev: agent.Event{
				Kind:       agent.EventToolStart,
				ToolCallID: "call_1",
				ToolName:   "read_file",
				Args:       `{"path":"a.txt"}`,
				Depth:      1,
			},
			seq: 8,
			want: SurfaceEvent{
				Kind:       "tool_start",
				Seq:        8,
				ToolCallID: "call_1",
				Depth:      1,
				Extra:      map[string]string{},
			},
		},
		{
			name: "tool end with error and depth",
			ev: agent.Event{
				Kind:         agent.EventToolEnd,
				ToolCallID:   "call_2",
				Text:         "boom",
				IsError:      true,
				Depth:        2,
				StartedAtMS:  2000,
				FinishedAtMS: 2500,
			},
			seq: 9,
			want: SurfaceEvent{
				Kind:       "tool_end",
				Seq:        9,
				AtMS:       2000,
				ToolCallID: "call_2",
				Text:       "boom",
				IsError:    true,
				Depth:      2,
				Extra:      map[string]string{},
			},
		},
		{
			name: "zero event",
			ev:   agent.Event{},
			seq:  0,
			want: SurfaceEvent{
				Extra: map[string]string{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromAgentEvent(tt.ev, tt.seq)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FromAgentEvent() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestFromAgentEventSeqAndAtMS 单独锁定序号与时间回退规则。
func TestFromAgentEventSeqAndAtMS(t *testing.T) {
	tests := []struct {
		name    string
		ev      agent.Event
		seq     uint64
		wantSeq uint64
		wantMS  int64
	}{
		{name: "started wins", ev: agent.Event{StartedAtMS: 111, FinishedAtMS: 222}, seq: 5, wantSeq: 5, wantMS: 111},
		{name: "fallback to finished", ev: agent.Event{FinishedAtMS: 333}, seq: 6, wantSeq: 6, wantMS: 333},
		{name: "no timing", ev: agent.Event{}, seq: 0, wantSeq: 0, wantMS: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromAgentEvent(tt.ev, tt.seq)
			if got.Seq != tt.wantSeq {
				t.Errorf("Seq = %d, want %d", got.Seq, tt.wantSeq)
			}
			if got.AtMS != tt.wantMS {
				t.Errorf("AtMS = %d, want %d", got.AtMS, tt.wantMS)
			}
		})
	}
}

// TestFromAgentEventExtraIsIndependent 确认 Extra 是独立可写的副本，且 JSON 不会改动它。
func TestFromAgentEventExtraIsIndependent(t *testing.T) {
	got := FromAgentEvent(agent.Event{Kind: agent.EventNotice}, 1)
	if got.Extra == nil {
		t.Fatal("FromAgentEvent().Extra = nil, want a writable map")
	}
	if len(got.Extra) != 0 {
		t.Fatalf("len(Extra) = %d, want 0", len(got.Extra))
	}

	// 写入 DTO 的 Extra 不影响其他 DTO 的 Extra。
	other := FromAgentEvent(agent.Event{Kind: agent.EventNotice}, 1)
	got.Extra["k"] = "v"
	if _, exists := other.Extra["k"]; exists {
		t.Fatal("两个 DTO 共享了同一个 Extra 映射")
	}

	// JSON() 不修改调用方持有的映射，且重复调用字节一致。
	dto := SurfaceEvent{Kind: "notice", Extra: map[string]string{"b": "2", "a": "1"}}
	before := map[string]string{"b": "2", "a": "1"}
	first := string(dto.JSON())
	second := string(dto.JSON())
	if first != second {
		t.Fatalf("JSON() 不稳定：%q != %q", first, second)
	}
	if !reflect.DeepEqual(dto.Extra, before) {
		t.Fatalf("JSON() 修改了 Extra：%+v, want %+v", dto.Extra, before)
	}
}

// TestSurfaceEventJSON 锁定序列化字节：字段顺序、键排序、CJK 与 HTML 原样输出、无尾随换行。
func TestSurfaceEventJSON(t *testing.T) {
	tests := []struct {
		name string
		in   SurfaceEvent
		want string
	}{
		{
			name: "zero value",
			in:   SurfaceEvent{},
			want: `{}`,
		},
		{
			name: "all fields with sorted extra",
			in: SurfaceEvent{
				Kind:       "tool_end",
				Seq:        9,
				AtMS:       1000,
				ToolCallID: "call_1",
				Text:       "done",
				IsError:    true,
				Depth:      2,
				Extra:      map[string]string{"z": "last", "a": "first", "m": "mid"},
			},
			want: `{"kind":"tool_end","seq":9,"at_ms":1000,"tool_call_id":"call_1","text":"done","is_error":true,"depth":2,"extra":{"a":"first","m":"mid","z":"last"}}`,
		},
		{
			name: "cjk and html raw",
			in:   SurfaceEvent{Text: "你好 <世界> & 朋友"},
			want: `{"text":"你好 <世界> & 朋友"}`,
		},
		{
			name: "extra insertion order irrelevant",
			in:   SurfaceEvent{Extra: map[string]string{"b": "2", "a": "1"}},
			want: `{"extra":{"a":"1","b":"2"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(tt.in.JSON())
			if got != tt.want {
				t.Errorf("JSON() = %s, want %s", got, tt.want)
			}
			if strings.HasSuffix(got, "\n") {
				t.Errorf("JSON() 含尾随换行：%q", got)
			}
			if strings.Contains(got, `\u`) {
				t.Errorf("JSON() 含 Unicode 转义：%s", got)
			}
		})
	}
}

// TestSurfaceEventJSONStableAcrossMapInsertion 验证同一组键值在不同插入顺序下输出一致。
func TestSurfaceEventJSONStableAcrossMapInsertion(t *testing.T) {
	a := SurfaceEvent{Extra: map[string]string{"a": "1", "b": "2", "c": "3"}}
	b := SurfaceEvent{Extra: map[string]string{"c": "3", "b": "2", "a": "1"}}
	if string(a.JSON()) != string(b.JSON()) {
		t.Fatalf("键顺序影响输出：%s != %s", a.JSON(), b.JSON())
	}
}

// TestSameKind 验证 SameKind 只比较 Kind，忽略其余字段。
func TestSameKind(t *testing.T) {
	tests := []struct {
		name string
		a    SurfaceEvent
		b    SurfaceEvent
		want bool
	}{
		{
			name: "same kind different payload",
			a:    SurfaceEvent{Kind: "text_delta", Seq: 1, Text: "a"},
			b:    SurfaceEvent{Kind: "text_delta", Seq: 99, Text: "b", IsError: true, Depth: 3},
			want: true,
		},
		{
			name: "different kind",
			a:    SurfaceEvent{Kind: "tool_start"},
			b:    SurfaceEvent{Kind: "tool_end"},
			want: false,
		},
		{
			name: "both empty",
			a:    SurfaceEvent{},
			b:    SurfaceEvent{},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameKind(tt.a, tt.b); got != tt.want {
				t.Errorf("SameKind() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFromAgentEventDoesNotMutateInput 确认含指针字段的事件在投影后逐字段不变。
func TestFromAgentEventDoesNotMutateInput(t *testing.T) {
	ev := agent.Event{
		Kind:         agent.EventError,
		Text:         "failed",
		Err:          "boom",
		Failure:      &llm.FailureInfo{ErrorID: "E1"},
		Retry:        &agent.RetryInfo{Attempt: 2},
		StartedAtMS:  10,
		FinishedAtMS: 20,
	}
	before := ev
	_ = FromAgentEvent(ev, 42)
	if !reflect.DeepEqual(ev, before) {
		t.Fatalf("FromAgentEvent 修改了入参：%+v, want %+v", ev, before)
	}
}
