package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件是事件字节的守护层：SSE 的 data 与 RPC 的 event 通知都直接
// json.Marshal(agent.Event)，任何字段重命名/顺序调整/omitempty 增删都会
// 改变线上字节并破坏前端与持久化记录的兼容性。下面的 golden 常量由当前
// 生产序列化输出逐字节抄录，不依赖重新计算，从而能捕捉意外的漂移。

const (
	// eventByteGuardZeroJSON 是全零值事件：所有非 omitempty 字段都出现，
	// Usage/TotalUsage 是零值对象而非 null，Checkpoint 无 omitempty 故为 null。
	eventByteGuardZeroJSON = `{"Kind":"","Turn":0,"Depth":0,"Text":"","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null}`

	// eventByteGuardFullJSON 让每个 omitempty 字段都出现，锁定 failure/retry
	// 的标签名、内部字段顺序与大小写，以及时间戳/partial 的附加字段。
	eventByteGuardFullJSON = `{"Kind":"partial","Turn":7,"Depth":2,"Text":"hello","ToolCallID":"call-1","ToolName":"edit","Args":"{\"path\":\"a.go\"}","Intent":"apply patch","Output":"done","IsError":true,"DurationMS":42,"Usage":{"prompt_tokens":10,"completion_tokens":20,"cached_tokens":3,"cache_write_tokens":4},"TotalUsage":{"prompt_tokens":100,"completion_tokens":200},"Err":"boom","failure":{"Kind":"rate_limit","Retryable":true,"UserAction":"retry","ErrorID":"rate_limit.exceeded"},"Reason":"provider_error","PendingMessages":2,"Checkpoint":{"turn":7,"calls":[{"id":"c1","tool":"edit","args":"{\"path\":\"a.go\"}"}],"paths":["a.go","b.go"]},"retry":{"attempt":2,"max_attempts":3,"delay_ms":250,"reason":"rate_limit","model":"gpt-x"},"started_at_ms":1700000000000,"finished_at_ms":1700000000042,"partial":true,"partial_stop_reason":"provider_error","partial_model":"gpt-x","partial_truncated":true}`

	// 以下五个样本分别覆盖一个 omitempty 分支被填、其余被省略的情形。
	eventByteGuardFailureJSON    = `{"Kind":"error","Turn":3,"Depth":0,"Text":"t","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"boom","failure":{"Kind":"quota","Retryable":false,"UserAction":"settings","ErrorID":"quota.exhausted"},"Reason":"","PendingMessages":0,"Checkpoint":null}`
	eventByteGuardRetryJSON      = `{"Kind":"retry","Turn":5,"Depth":0,"Text":"","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null,"retry":{"attempt":1,"max_attempts":4,"delay_ms":500,"reason":"rate_limit","model":"m"}}`
	eventByteGuardTimingJSON     = `{"Kind":"tool_end","Turn":2,"Depth":0,"Text":"","ToolCallID":"c","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":9,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null,"started_at_ms":100,"finished_at_ms":109}`
	eventByteGuardPartialJSON    = `{"Kind":"partial","Turn":4,"Depth":0,"Text":"prefix","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null,"partial":true,"partial_stop_reason":"cancelled","partial_model":"m2","partial_truncated":true}`
	eventByteGuardCheckpointJSON = `{"Kind":"checkpoint_ready","Turn":6,"Depth":0,"Text":"","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":{"turn":6,"calls":[{"id":"x","tool":"write","args":"a"}],"paths":["f.txt"]}}`

	// SSE/RPC 共用的 notice 样本字节（仅 Text 不同）。
	eventByteGuardNoticeHelloJSON = `{"Kind":"notice","Turn":0,"Depth":0,"Text":"hello","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null}`
	eventByteGuardNoticeWakeJSON  = `{"Kind":"notice","Turn":0,"Depth":0,"Text":"wake","ToolCallID":"","ToolName":"","Args":"","Intent":"","Output":"","IsError":false,"DurationMS":0,"Usage":{"prompt_tokens":0,"completion_tokens":0},"TotalUsage":{"prompt_tokens":0,"completion_tokens":0},"Err":"","Reason":"","PendingMessages":0,"Checkpoint":null}`
)

// eventByteGuardSamples 构造覆盖所有 omitempty 分支的样本：零值、全填、
// 以及 Failure/Retry/timing/partial/Checkpoint 各自单独出现的混合样本。
func eventByteGuardSamples() []struct {
	name string
	ev   agent.Event
	want string
} {
	return []struct {
		name string
		ev   agent.Event
		want string
	}{
		{name: "zero", ev: agent.Event{}, want: eventByteGuardZeroJSON},
		{name: "full", want: eventByteGuardFullJSON, ev: agent.Event{
			Kind:            agent.EventPartial,
			Turn:            7,
			Depth:           2,
			Text:            "hello",
			ToolCallID:      "call-1",
			ToolName:        "edit",
			Args:            `{"path":"a.go"}`,
			Intent:          "apply patch",
			Output:          "done",
			IsError:         true,
			DurationMS:      42,
			Usage:           llm.Usage{PromptTokens: 10, CompletionTokens: 20, CachedTokens: 3, CacheWriteTokens: 4},
			TotalUsage:      llm.Usage{PromptTokens: 100, CompletionTokens: 200},
			Err:             "boom",
			Failure:         &llm.FailureInfo{Kind: llm.FailureRateLimit, Retryable: true, UserAction: llm.UserActionRetry, ErrorID: "rate_limit.exceeded"},
			Reason:          agent.StopProviderError,
			PendingMessages: 2,
			Checkpoint: &agent.Checkpoint{
				Turn:  7,
				Calls: []agent.CheckpointCall{{ID: "c1", Tool: "edit", Args: `{"path":"a.go"}`}},
				Paths: []string{"a.go", "b.go"},
			},
			Retry:             &agent.RetryInfo{Attempt: 2, MaxAttempts: 3, DelayMS: 250, Reason: "rate_limit", Model: "gpt-x"},
			StartedAtMS:       1700000000000,
			FinishedAtMS:      1700000000042,
			Partial:           true,
			PartialStopReason: agent.StopProviderError,
			PartialModel:      "gpt-x",
			PartialTruncated:  true,
		}},
		{name: "failure", want: eventByteGuardFailureJSON, ev: agent.Event{
			Kind: agent.EventError, Turn: 3, Text: "t", Err: "boom",
			Failure: &llm.FailureInfo{Kind: llm.FailureQuota, UserAction: llm.UserActionSettings, ErrorID: "quota.exhausted"},
		}},
		{name: "retry", want: eventByteGuardRetryJSON, ev: agent.Event{
			Kind: agent.EventRetry, Turn: 5,
			Retry: &agent.RetryInfo{Attempt: 1, MaxAttempts: 4, DelayMS: 500, Reason: "rate_limit", Model: "m"},
		}},
		{name: "timing", want: eventByteGuardTimingJSON, ev: agent.Event{
			Kind: agent.EventToolEnd, Turn: 2, ToolCallID: "c", DurationMS: 9,
			StartedAtMS: 100, FinishedAtMS: 109,
		}},
		{name: "partial", want: eventByteGuardPartialJSON, ev: agent.Event{
			Kind: agent.EventPartial, Turn: 4, Text: "prefix", Partial: true,
			PartialStopReason: agent.StopCancelled, PartialModel: "m2", PartialTruncated: true,
		}},
		{name: "checkpoint", want: eventByteGuardCheckpointJSON, ev: agent.Event{
			Kind: agent.EventCheckpointReady, Turn: 6,
			Checkpoint: &agent.Checkpoint{
				Turn:  6,
				Calls: []agent.CheckpointCall{{ID: "x", Tool: "write", Args: "a"}},
				Paths: []string{"f.txt"},
			},
		}},
	}
}

// TestEventByteGuardGoldenJSON 用硬编码 golden 锁定 Event 的线上字节：
// 大写 "Kind"、字段声明顺序、零值 Usage 对象、Checkpoint:null 以及每个
// omitempty 分支的标签名都逐字节固定。
func TestEventByteGuardGoldenJSON(t *testing.T) {
	for _, tc := range eventByteGuardSamples() {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.ev)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("json.Marshal bytes changed:\n got %s\nwant %s", data, tc.want)
			}

			// 大写 Kind 必须是第一个键，前端与持久化记录都依赖它。
			if !strings.HasPrefix(string(data), `{"Kind":`) {
				t.Fatalf("event JSON no longer starts with uppercase Kind: %s", data)
			}
			if strings.Contains(string(data), `"kind":`) {
				t.Fatalf("lowercase kind leaked into event JSON: %s", data)
			}
		})
	}
}

// TestEventByteGuardZeroContract 单独强调零值事件的两个易漂移点：
// Usage/TotalUsage 始终序列化为零值对象，Checkpoint 无 omitempty 故为 null。
func TestEventByteGuardZeroContract(t *testing.T) {
	data, err := json.Marshal(agent.Event{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if got != eventByteGuardZeroJSON {
		t.Fatalf("zero event bytes changed:\n got %s\nwant %s", got, eventByteGuardZeroJSON)
	}
	for _, frag := range []string{
		`"Usage":{"prompt_tokens":0,"completion_tokens":0}`,
		`"TotalUsage":{"prompt_tokens":0,"completion_tokens":0}`,
		`"Checkpoint":null`,
	} {
		if !strings.Contains(got, frag) {
			t.Fatalf("zero event JSON missing %s: %s", frag, got)
		}
	}
}

// eventByteGuardLegacy 是新增 omitempty 字段之前的字段集合。用它做
// 逐字节对照，可证明新字段在零值时完全不改变事件字节（最强形式的兼容保证）。
type eventByteGuardLegacy struct {
	Kind            agent.EventKind
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
	Usage           llm.Usage
	TotalUsage      llm.Usage
	Err             string
	Reason          string
	PendingMessages int
	Checkpoint      *agent.Checkpoint
}

// TestEventByteGuardOmitemptyAdditive 断言新增字段只以 omitempty 形式出现：
// 零值事件与不含这些字段的旧结构逐字节相同；混合样本中未填的附加字段
// 不会出现，而填了的字段使用小写标签而非 Go 字段名。
func TestEventByteGuardOmitemptyAdditive(t *testing.T) {
	zero, err := json.Marshal(agent.Event{})
	if err != nil {
		t.Fatal(err)
	}
	legacyZero, err := json.Marshal(eventByteGuardLegacy{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(zero, legacyZero) {
		t.Fatalf("zero-valued new fields changed event bytes:\n got %s\nwant %s", zero, legacyZero)
	}

	// 非平凡的公共字段同样必须字节相同，排除「恰好都是零值」的假阳性。
	ev := agent.Event{Kind: agent.EventError, Turn: 9, Text: "t", Err: "boom", Reason: agent.StopProviderError}
	got, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	legacy := eventByteGuardLegacy{Kind: ev.Kind, Turn: ev.Turn, Text: ev.Text, Err: ev.Err, Reason: ev.Reason}
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("unset omitempty fields changed event bytes:\n got %s\nwant %s", got, want)
	}

	// 零值时每个附加键（含可能被误用的 Go 字段名）都不得泄漏。
	for _, key := range []string{
		"failure", "Failure", "retry", "Retry",
		"started_at_ms", "StartedAtMS", "finished_at_ms", "FinishedAtMS",
		"partial", "Partial", "partial_stop_reason", "PartialStopReason",
		"partial_model", "PartialModel", "partial_truncated", "PartialTruncated",
	} {
		if strings.Contains(string(zero), `"`+key+`"`) {
			t.Fatalf("zero value leaked key %q into event JSON: %s", key, zero)
		}
	}

	// 全填样本必须携带全部小写标签，且不得出现 Go 字段名。
	full := eventByteGuardSamples()[1].ev
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"failure", "retry", "started_at_ms", "finished_at_ms",
		"partial", "partial_stop_reason", "partial_model", "partial_truncated",
	} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("full event JSON missing key %q: %s", key, data)
		}
	}
	for _, key := range []string{"Failure", "Retry", "StartedAtMS", "FinishedAtMS", "Partial", "PartialStopReason", "PartialModel", "PartialTruncated"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("full event JSON used Go field name %q instead of json tag: %s", key, data)
		}
	}
}

// TestEventByteGuardSSEFrameGolden 锁定 SSE 帧格式：
// `event: <kind>\ndata: <json>\n\n`，并逐字节比对首帧与事件帧。
func TestEventByteGuardSSEFrameGolden(t *testing.T) {
	hub := NewEventHub()
	handler := newEventHandlerInterval(hub, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := startEventStream(t, handler, ctx)
	defer func() {
		if !stopEventStream(cancel, done) {
			t.Error("event handler did not stop on cancel")
		}
	}()

	rec.waitContains(t, "event: server.connected")
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "hello"})
	secondFrame := "event: notice\ndata: " + eventByteGuardNoticeHelloJSON + "\n\n"
	rec.waitContains(t, secondFrame)

	if !stopEventStream(cancel, done) {
		t.Fatal("event handler did not stop on cancel")
	}
	want := "event: server.connected\ndata: {\"seq\":0}\n\n" + secondFrame
	if got := rec.snapshot(); got != want {
		t.Fatalf("SSE frame bytes changed:\n got %q\nwant %q", got, want)
	}
}

// TestEventByteGuardRPCNotifyKeyOrder 锁定 RPC 通知中 map 的键排序：
// json.Marshal 对 map 按键排序，因此 params 内 "event" 必须排在 "kind" 之前。
func TestEventByteGuardRPCNotifyKeyOrder(t *testing.T) {
	var buf bytes.Buffer
	p := newPeer(&buf)
	p.notify("event", map[string]any{"kind": agent.EventNotice, "event": agent.Event{Kind: agent.EventNotice, Text: "wake"}})
	want := `{"jsonrpc":"2.0","method":"event","params":{"event":` + eventByteGuardNoticeWakeJSON + `,"kind":"notice"}}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("RPC notification bytes changed:\n got %s\nwant %s", got, want)
	}

	got := buf.String()
	eventAt := strings.Index(got, `"event"`)
	kindAt := strings.Index(got, `"kind"`)
	if eventAt < 0 || kindAt < 0 || eventAt >= kindAt {
		t.Fatalf("RPC notification key order changed (event must precede kind): %s", got)
	}
}

// TestEventByteGuardRPCStreamFrameGolden 从真实 RunRPC 传输读取原始帧字节，
// 确认 event/subscribe 的通知与 peer.notify 的 golden 逐字节一致（含结尾换行前的对象体）。
func TestEventByteGuardRPCStreamFrameGolden(t *testing.T) {
	hub := NewEventHub()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(ctx, inR, outW, Config{Runner: &fakeRunner{}, Events: hub})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "event/subscribe",
		"params": map[string]any{"after": 0},
	}); err != nil {
		t.Fatalf("encode subscribe: %v", err)
	}
	var result json.RawMessage
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("decode subscribe result: %v", err)
	}

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "wake"})
	var notification json.RawMessage
	if err := dec.Decode(&notification); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	want := `{"jsonrpc":"2.0","method":"event","params":{"event":` + eventByteGuardNoticeWakeJSON + `,"kind":"notice"}}`
	if string(notification) != want {
		t.Fatalf("RPC stream notification bytes changed:\n got %s\nwant %s", notification, want)
	}

	cancel()
	_ = inW.Close()
	waitFor(t, done)
}
