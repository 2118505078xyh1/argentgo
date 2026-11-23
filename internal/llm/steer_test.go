package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// steerSSEClient 用固定的 SSE 脚本启动本地服务，返回指向它的 Responses 客户端。
// 不联网、不用真实模型；服务随测试结束自动关闭。
func steerSSEClient(t *testing.T, script string) *ResponsesClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(srv.Close)
	return NewResponses(srv.URL, "k")
}

func TestLiveSteerOnGate(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", false},
		{"zero", "0", false},
		{"truthy word", "true", false},
		{"one", "1", true},
		{"padded one", " \t1\n ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_LIVE_STEER", tc.value)
			if got := liveSteerOn(); got != tc.want {
				t.Fatalf("liveSteerOn() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDrainSteerTexts(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"filters blank", []string{"  ", "\t\n", " ok ", "x"}, []string{" ok ", "x"}},
		{"keeps original text", []string{"  修正  "}, []string{"  修正  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := drainSteerTexts(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("drainSteerTexts(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestLiveSteerOffPathsUnchanged 守护默认路径：门控关闭、或门控开启但未提供 Steer 时，
// readStream 不得调用 Steer、不得上报决策，且答复文本与顺序保持不变。
func TestLiveSteerOffPathsUnchanged(t *testing.T) {
	const script = `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":2}}}

`
	cases := []struct {
		name     string
		env      string
		setSteer bool
	}{
		{"gate off", "0", true},
		{"gate unset", "", true},
		{"gate on but steer nil", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_LIVE_STEER", tc.env)
			calls, decisions := 0, 0
			client := steerSSEClient(t, script)
			if tc.setSteer {
				client.Options.Steer = func() []string {
					calls++
					return []string{"请改主意"}
				}
			}
			client.Options.SteerHook = func(SteerDecision) { decisions++ }

			reply, err := client.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, nil)
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			if reply.Content != "Hello world" {
				t.Fatalf("content = %q, want %q", reply.Content, "Hello world")
			}
			if calls != 0 {
				t.Fatalf("Steer called %d times on off path, want 0", calls)
			}
			if decisions != 0 {
				t.Fatalf("SteerHook called %d times on off path, want 0", decisions)
			}
		})
	}
}

// TestLiveSteerConsumesAndRecords 验证门控开启后：事件边界消费排队的纠正文本，
// 工具调用进行中标记 deferred、其余标记 accepted，纯空白被过滤，且回复的
// 文本与工具调用按原顺序组装（transcript 顺序正确）。
func TestLiveSteerConsumesAndRecords(t *testing.T) {
	t.Setenv("ARGENT_LIVE_STEER", "1")
	const script = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":"}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"a.rs\"}"}

data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"a.rs\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call"}}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}

`
	// 每次事件边界返回一批；共 9 个事件，对应 9 次调用。
	batches := [][]string{
		nil,
		{"先把文件列出来"}, // 工具调用前 -> accepted
		nil,
		{"现在读 a.rs"}, // 工具调用进行中 -> deferred
		nil,
		nil,
		{"   "},   // 纯空白 -> 过滤，不产生决策
		{"改用新方案"}, // 工具调用完成后 -> accepted
		nil,
	}
	call := 0
	var decisions []SteerDecision
	client := steerSSEClient(t, script)
	client.Options.Steer = func() []string {
		if call < len(batches) {
			b := batches[call]
			call++
			return b
		}
		call++
		return nil
	}
	client.Options.SteerHook = func(d SteerDecision) { decisions = append(decisions, d) }

	reply, err := client.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if call != len(batches) {
		t.Fatalf("Steer called %d times, want %d", call, len(batches))
	}
	want := []SteerDecision{
		{Text: "先把文件列出来", Status: "accepted", Reason: "event-boundary"},
		{Text: "现在读 a.rs", Status: "deferred", Reason: "tool-call-in-progress"},
		{Text: "改用新方案", Status: "accepted", Reason: "event-boundary"},
	}
	if !reflect.DeepEqual(decisions, want) {
		t.Fatalf("decisions = %#v, want %#v", decisions, want)
	}
	// 纠正文本绝不混入助手正文；工具调用按原顺序完整组装。
	if reply.Content != "Hello world" {
		t.Fatalf("content = %q, want %q", reply.Content, "Hello world")
	}
	wantCalls := []ToolCall{{
		ID:       "call_1",
		Type:     "function",
		Function: ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
	}}
	if !reflect.DeepEqual(reply.ToolCalls, wantCalls) {
		t.Fatalf("tool calls = %#v, want %#v", reply.ToolCalls, wantCalls)
	}
}

// TestLiveSteerDoesNotChangeWireBytes 锁定线协议字节：即便注入了 Steer 源，
// buildRequest 的输出仍与接线前逐字节一致（Responses 流中不能追加 input 消息）。
func TestLiveSteerDoesNotChangeWireBytes(t *testing.T) {
	t.Setenv("ARGENT_LIVE_STEER", "1")
	client := NewResponses("http://127.0.0.1:1", "k")
	client.Options.Steer = func() []string { return []string{"改成别的实现"} }
	client.Options.SteerHook = func(SteerDecision) {}
	raw, err := json.Marshal(client.buildRequest("m", []Message{{Role: "user", Content: "hi"}}, nil))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	const golden = `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":8192,"stream":true,"store":false}`
	if string(raw) != golden {
		t.Fatalf("请求字节漂移\n got: %s\nwant: %s", raw, golden)
	}
}

// TestLiveSteerCancellationNoLeak 验证取消时无常驻 goroutine、无残留决策：
// 服务端在首个增量后挂起，取消 context 后 Chat 必须及时返回 context.Canceled。
func TestLiveSteerCancellationNoLeak(t *testing.T) {
	t.Setenv("ARGENT_LIVE_STEER", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	before := runtime.NumGoroutine()
	steerCalled := make(chan struct{}, 1)
	decisions := 0
	client := NewResponses(srv.URL, "k")
	client.Options.Steer = func() []string {
		select {
		case steerCalled <- struct{}{}:
		default:
		}
		return nil // 无 steering 文本时不应上报任何决策
	}
	client.Options.SteerHook = func(SteerDecision) { decisions++ }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Chat(ctx, "m", []Message{{Role: "user", Content: "hi"}}, nil, nil)
		done <- err
	}()

	select {
	case <-steerCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("Steer 未在事件边界被调用")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Chat 未及时返回")
	}
	if decisions != 0 {
		t.Fatalf("无 steering 文本时不应上报决策, got %d", decisions)
	}

	// 容忍 httptest 自身收尾的少量 goroutine；live steering 本身不启动协程。
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("疑似 goroutine 泄漏: before=%d after=%d", before, got)
	}
}
