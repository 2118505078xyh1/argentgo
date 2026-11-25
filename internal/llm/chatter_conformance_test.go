package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

// 本文件把"任何 llm.Chatter 实现都必须满足"的行为收敛为一张工厂表 + 一组
// 契约断言。新接入的流式协议客户端只需在 chatterConformanceCases 中增加一项，
// 就会自动运行全部契约，避免每个协议各写一份、遗漏取消或回调顺序等边界。
//
// 这里只依赖本地 httptest，不联网、不使用真实模型。

// chatterConformanceCase 描述一个 Chatter 实现如何构造，以及一份能产生终态
// 消息的成功 SSE 脚本。脚本必须输出 wantText 指定的文本并以协议自身的完成
// 事件结束，否则"终态"契约会失败。
type chatterConformanceCase struct {
	name     string
	build    func(url string) llm.Chatter
	okScript string
	wantText string
}

// chatterConformanceCases 返回所有需要遵守 Chatter 契约的实现。
// 新增协议实现时只改这里，下面是统一的行为断言。
func chatterConformanceCases() []chatterConformanceCase {
	return []chatterConformanceCase{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			okScript: `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}

data: [DONE]

`,
			wantText: "Hello world",
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			okScript: `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`,
			wantText: "Hello world",
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			okScript: `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}

`,
			wantText: "Hello world",
		},
	}
}

// TestChatterConformanceTerminalMessage 要求每个实现把完整的成功流收敛为一条
// assistant 消息，且 onText 收到的增量拼接结果与消息正文一致。流式回调与最终
// 消息不一致会让 agent 边写边展示的内容和落盘历史对不上，属于静默错误。
func TestChatterConformanceTerminalMessage(t *testing.T) {
	for _, tc := range chatterConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.okScript)
			}))
			defer server.Close()

			var deltas strings.Builder
			reply, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, func(delta string) {
				deltas.WriteString(delta)
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Role != "assistant" {
				t.Errorf("Role = %q, want assistant", reply.Role)
			}
			if reply.Content != tc.wantText {
				t.Errorf("Content = %q, want %q", reply.Content, tc.wantText)
			}
			if got := deltas.String(); got != tc.wantText {
				t.Errorf("streamed deltas = %q, want %q", got, tc.wantText)
			}
		})
	}
}

// TestChatterConformanceCancelPropagation 要求已取消的 context 在限定时间内
// 以可识别的取消错误返回。Chatter 是 agent 循环的取消入口：若某个实现吞掉取消
// 或一直挂起，"取消 context 会在一轮之内停止运行"这条不变量就会失效。
func TestChatterConformanceCancelPropagation(t *testing.T) {
	for _, tc := range chatterConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			type result struct {
				err error
			}
			done := make(chan result, 1)
			go func() {
				_, err := tc.build("http://127.0.0.1:1").Chat(ctx, "m", nil, nil, nil)
				done <- result{err: err}
			}()

			select {
			case got := <-done:
				if got.err == nil {
					t.Fatal("Chat with cancelled context returned no error")
				}
				if !errors.Is(got.err, context.Canceled) {
					t.Errorf("err = %v, want errors.Is(err, context.Canceled)", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Chat did not return after context cancellation")
			}
		})
	}
}

// TestChatterConformanceServerErrorReturnsError 要求传输层/服务端的失败以 error
// 返回，而不是 panic 或伪装成一条空的 assistant 消息。agent 依赖该错误触发
// 分类与重试；把失败伪装成成功会导致工具循环在错误上下文上继续运行。
func TestChatterConformanceServerErrorReturnsError(t *testing.T) {
	for _, tc := range chatterConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := tc.build(server.URL).Chat(ctx, "m", nil, nil, nil)
			if err == nil {
				t.Fatal("Chat on HTTP 500 returned no error")
			}
		})
	}
}

// TestChatterConformanceOnTextIsSerialized 要求 onText 回调不被并发调用。
// agent 在回调里追加流式文本，若两个 goroutine 同时写入，累加缓冲会出现
// 竞态与乱序；这是要在契约层挡住的行为。
func TestChatterConformanceOnTextIsSerialized(t *testing.T) {
	for _, tc := range chatterConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.okScript)
			}))
			defer server.Close()

			var active int32
			var overlapped int32
			_, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, func(string) {
				if n := atomic.AddInt32(&active, 1); n > 1 {
					atomic.StoreInt32(&overlapped, 1)
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&active, -1)
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if atomic.LoadInt32(&overlapped) != 0 {
				t.Error("onText was called concurrently")
			}
		})
	}
}

// TestChatterConformanceNilOnTextAllowed 要求 onText 为 nil 时仍可完成请求。
// 调用方在只关心最终消息的场合（如探测/工具调用）会传入 nil。
func TestChatterConformanceNilOnTextAllowed(t *testing.T) {
	for _, tc := range chatterConformanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.okScript)
			}))
			defer server.Close()

			if _, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, nil); err != nil {
				t.Fatalf("Chat with nil onText: %v", err)
			}
		})
	}
}
