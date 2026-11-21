package eval

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// 本文件中的类型只在测试内定义，用来模拟 agent.Event 及其嵌套类型。
// eval 不 import agent，故这里用独立的类型复刻其字段名、类型形态、json tag 与顺序；
// 类型名刻意与 lossless_event.go 中的镜像类型不同，以证明 MirrorEvent 是按字段名而非按类型投影。

type mirrorKind string

type mirrorUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

type mirrorFailureKind string
type mirrorUserAction string

type mirrorFailure struct {
	Kind       mirrorFailureKind
	Retryable  bool
	UserAction mirrorUserAction
	ErrorID    string
}

type mirrorCheckpoint struct {
	Turn  int          `json:"turn"`
	Calls []mirrorCall `json:"calls"`
	Paths []string     `json:"paths"`
}

type mirrorCall struct {
	ID   string `json:"id,omitempty"`
	Tool string `json:"tool"`
	Args string `json:"args,omitempty"`
}

type mirrorRetry struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	DelayMS     int64  `json:"delay_ms,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
}

// mirrorEvent 逐字段模拟 internal/agent/events.go:84-139 的 agent.Event。
type mirrorEvent struct {
	Kind            mirrorKind
	Turn            int
	Depth           int
	Text            string
	ToolCallID      string
	ToolName        string
	Args            string
	Intent          string
	Output          string
	IsError         bool
	DurationMS      int64
	Usage           mirrorUsage
	TotalUsage      mirrorUsage
	Err             string
	Failure         *mirrorFailure `json:"failure,omitempty"`
	Reason          string
	PendingMessages int
	Checkpoint      *mirrorCheckpoint
	Retry           *mirrorRetry `json:"retry,omitempty"`
	StartedAtMS     int64        `json:"started_at_ms,omitempty"`
	FinishedAtMS    int64        `json:"finished_at_ms,omitempty"`

	Partial           bool   `json:"partial,omitempty"`
	PartialStopReason string `json:"partial_stop_reason,omitempty"`
	PartialModel      string `json:"partial_model,omitempty"`
	PartialTruncated  bool   `json:"partial_truncated,omitempty"`
}

// wantField 描述 agent.Event 的一个字段：名称与完整 json tag（空表示无 tag）。
type wantField struct {
	name string
	tag  string
}

// agentEventFields 是 agent.Event 的 25 个字段清单，按声明顺序硬编码。
// tag 取自 internal/agent/events.go:84-139；新增字段时若镜像未跟进，parity 测试先红。
var agentEventFields = []wantField{
	{"Kind", ""},
	{"Turn", ""},
	{"Depth", ""},
	{"Text", ""},
	{"ToolCallID", ""},
	{"ToolName", ""},
	{"Args", ""},
	{"Intent", ""},
	{"Output", ""},
	{"IsError", ""},
	{"DurationMS", ""},
	{"Usage", ""},
	{"TotalUsage", ""},
	{"Err", ""},
	{"Failure", "failure,omitempty"},
	{"Reason", ""},
	{"PendingMessages", ""},
	{"Checkpoint", ""},
	{"Retry", "retry,omitempty"},
	{"StartedAtMS", "started_at_ms,omitempty"},
	{"FinishedAtMS", "finished_at_ms,omitempty"},
	{"Partial", "partial,omitempty"},
	{"PartialStopReason", "partial_stop_reason,omitempty"},
	{"PartialModel", "partial_model,omitempty"},
	{"PartialTruncated", "partial_truncated,omitempty"},
}

// assertFieldParity 断言 typ 的字段名/json tag/声明顺序与 want 逐项一致。
func assertFieldParity(t *testing.T, typ reflect.Type, want []wantField) {
	t.Helper()
	if got := typ.NumField(); got != len(want) {
		t.Fatalf("%s 字段数 = %d, 期望 %d", typ.Name(), got, len(want))
	}
	for i, w := range want {
		f := typ.Field(i)
		if f.Name != w.name {
			t.Errorf("%s 字段[%d] = %q, 期望 %q", typ.Name(), i, f.Name, w.name)
		}
		if got := f.Tag.Get("json"); got != w.tag {
			t.Errorf("%s 字段 %q 的 tag = %q, 期望 %q", typ.Name(), w.name, got, w.tag)
		}
	}
}

// TestLosslessEventFieldParity 反射校验 LosslessEvent 与测试内的 mirrorEvent
// 都与 agent.Event 的字段清单逐项一致。后者保证字节测试的输入是忠实的 agent.Event 替身。
func TestLosslessEventFieldParity(t *testing.T) {
	t.Run("LosslessEvent", func(t *testing.T) {
		assertFieldParity(t, reflect.TypeOf(LosslessEvent{}), agentEventFields)
	})
	t.Run("mirror source", func(t *testing.T) {
		assertFieldParity(t, reflect.TypeOf(mirrorEvent{}), agentEventFields)
	})
}

// TestMirrorEventJSONByteEqual 是“字节稳定”黄金测试：覆盖每个 omitempty 分支的
// 零值与非零值，断言镜像后的 json.Marshal 字节与原事件逐字节相等。
// 这里刻意用 json.Marshal 而非 JSON()：后者关闭了 HTML 转义，字节比较需在同一编码规则下进行。
func TestMirrorEventJSONByteEqual(t *testing.T) {
	full := mirrorEvent{
		Kind:       mirrorKind("tool_end"),
		Turn:       3,
		Depth:      1,
		Text:       "hello",
		ToolCallID: "call-1",
		ToolName:   "fs.read",
		Args:       `{"path":"a.txt"}`,
		Intent:     "read a",
		Output:     "content",
		IsError:    true,
		DurationMS: 42,
		Usage: mirrorUsage{
			PromptTokens: 10, CompletionTokens: 5, CachedTokens: 2, CacheWriteTokens: 1,
		},
		TotalUsage: mirrorUsage{
			PromptTokens: 100, CompletionTokens: 50, CachedTokens: 20, CacheWriteTokens: 10,
		},
		Err: "boom",
		Failure: &mirrorFailure{
			Kind: "transient", Retryable: true, UserAction: "retry", ErrorID: "transient.network",
		},
		Reason:          "stop",
		PendingMessages: 2,
		Checkpoint: &mirrorCheckpoint{
			Turn: 3,
			Calls: []mirrorCall{
				{ID: "call-1", Tool: "fs.write", Args: `{"path":"b.txt"}`},
				{Tool: "fs.read"},
			},
			Paths: []string{"b.txt"},
		},
		Retry: &mirrorRetry{
			Attempt: 2, MaxAttempts: 5, DelayMS: 1500, Reason: "rate_limit", Model: "gpt-x",
		},
		StartedAtMS:       1_700_000_000_000,
		FinishedAtMS:      1_700_000_000_042,
		Partial:           true,
		PartialStopReason: "cancelled",
		PartialModel:      "gpt-x",
		PartialTruncated:  true,
	}

	samples := []struct {
		name string
		in   mirrorEvent
	}{
		// 零值：所有 omitempty 字段都应被省略（含 3 个指针嵌套字段）。
		{"zero", mirrorEvent{}},
		// 满值：所有 omitempty 分支都非零，嵌套结构全部填充。
		{"full", full},
		// 混合：指针嵌套字段为 nil，但部分标量 omitempty 字段非零。
		{"mixed", mirrorEvent{
			Kind: "partial", Text: "prefix", StartedAtMS: 123,
			PartialStopReason: "cancelled", PartialModel: "gpt-x",
		}},
	}

	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal 源事件: %v", err)
			}
			got, err := json.Marshal(MirrorEvent(tc.in))
			if err != nil {
				t.Fatalf("marshal 镜像事件: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("字节不相等\n want: %s\n got:  %s", want, got)
			}
		})
	}
}

// TestMirrorEventMissingKeysAndPurity 验证“字段不存在时取零值”与“不修改入参”。
func TestMirrorEventMissingKeysAndPurity(t *testing.T) {
	type partialSource struct {
		Kind  mirrorKind
		Text  string
		Usage mirrorUsage
	}
	in := partialSource{Kind: "turn_start", Text: "hi", Usage: mirrorUsage{PromptTokens: 7}}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	got := MirrorEvent(in)

	after, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("MirrorEvent 修改了入参\nbefore: %s\nafter:  %s", before, after)
	}
	if got.Kind != "turn_start" || got.Text != "hi" || got.Usage.PromptTokens != 7 {
		t.Errorf("已有字段投影错误: %+v", got)
	}
	if got.Turn != 0 || got.Depth != 0 || got.Failure != nil || got.Checkpoint != nil ||
		got.Retry != nil || got.Partial || got.StartedAtMS != 0 {
		t.Errorf("缺失字段未取零值: %+v", got)
	}
}

// TestLosslessEventJSONStableBytes 验证 JSON(): 无尾换行、不转义 HTML、重复调用字节稳定。
func TestLosslessEventJSONStableBytes(t *testing.T) {
	e := MirrorEvent(mirrorEvent{Kind: mirrorKind("notice"), Text: "a<b>&c", StartedAtMS: 9})
	got := e.JSON()
	if len(got) == 0 || got[len(got)-1] == '\n' {
		t.Fatalf("JSON() 不应以换行结尾: %q", got)
	}
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if bytes.Contains(got, []byte(esc)) {
			t.Fatalf("JSON() 不应转义 HTML(%s): %s", esc, got)
		}
	}
	if !bytes.Contains(got, []byte(`"Text":"a<b>&c"`)) {
		t.Fatalf("JSON() 缺少原始文本: %s", got)
	}
	if again := e.JSON(); !bytes.Equal(got, again) {
		t.Errorf("JSON() 非确定性\nfirst:  %s\nsecond: %s", got, again)
	}
}
