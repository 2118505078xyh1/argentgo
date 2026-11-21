package llm

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// 三个协议的最小合法 SSE 脚本：一条文本增量 + 一条携带 usage 的终止事件 +
// [DONE]（responses 用 response.completed 作为终止事件）。它们只用于验证
// readStream 的时间打点，不触碰任何网络。
const (
	chatTimingSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	anthropicTimingSSE = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	responsesTimingSSE = "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n"
)

// streamTimingRead 统一三个协议 readStream 的差异签名（chat 多一个 model 参数）。
type streamTimingRead struct {
	name   string
	script string
	read   func(context.Context, io.Reader, func(string)) (Message, error)
}

func streamTimingCases() []streamTimingRead {
	return []streamTimingRead{
		{
			name:   "chat_completions",
			script: chatTimingSSE,
			read: func(ctx context.Context, body io.Reader, onText func(string)) (Message, error) {
				return (&Client{}).readStream(ctx, "m", body, onText)
			},
		},
		{
			name:   "anthropic_messages",
			script: anthropicTimingSSE,
			read: func(ctx context.Context, body io.Reader, onText func(string)) (Message, error) {
				return (&AnthropicClient{}).readStream(ctx, body, onText)
			},
		},
		{
			name:   "responses",
			script: responsesTimingSSE,
			read: func(ctx context.Context, body io.Reader, onText func(string)) (Message, error) {
				return (&ResponsesClient{}).readStream(ctx, body, onText)
			},
		},
	}
}

// TestStreamTimingPointsMonotonic 验证三协议都记录三个毫秒时间点，且
// 请求发出 <= 首 token <= 流完成，onText 收到的文本不受包装影响。
func TestStreamTimingPointsMonotonic(t *testing.T) {
	disable := EnableStreamTiming()
	defer disable()

	for _, tc := range streamTimingCases() {
		t.Run(tc.name, func(t *testing.T) {
			var gotText strings.Builder
			msg, err := tc.read(context.Background(), strings.NewReader(tc.script), func(s string) {
				gotText.WriteString(s)
				// 拉开首 token 与流完成的间隔，使毫秒级递增可稳定观测。
				time.Sleep(3 * time.Millisecond)
			})
			if err != nil {
				t.Fatalf("readStream: %v", err)
			}
			if gotText.String() != "hi" {
				t.Fatalf("onText text = %q, want %q", gotText.String(), "hi")
			}
			if msg.Usage == nil {
				t.Fatal("usage is nil when timing is enabled")
			}
			u := msg.Usage
			if u.StreamRequestMS <= 0 || u.StreamFirstMS <= 0 || u.StreamDoneMS <= 0 {
				t.Fatalf("timing points must be positive: request=%d first=%d done=%d",
					u.StreamRequestMS, u.StreamFirstMS, u.StreamDoneMS)
			}
			if !(u.StreamRequestMS <= u.StreamFirstMS && u.StreamFirstMS <= u.StreamDoneMS) {
				t.Fatalf("timing points must be monotonic: request=%d first=%d done=%d",
					u.StreamRequestMS, u.StreamFirstMS, u.StreamDoneMS)
			}
			if u.StreamDoneMS-u.StreamFirstMS < 2 {
				t.Fatalf("stream duration too short to be meaningful: first=%d done=%d",
					u.StreamFirstMS, u.StreamDoneMS)
			}
		})
	}
}

// TestStreamTimingDefaultOff 证明 opt-in 关闭时三个字段全零，Usage 与打点
// 接入前逐字段一致，且 onText 不被包装（既有行为不变）。
func TestStreamTimingDefaultOff(t *testing.T) {
	if streamTimingOn() {
		t.Fatal("stream timing must be off by default; another test leaked the global switch")
	}
	for _, tc := range streamTimingCases() {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := tc.read(context.Background(), strings.NewReader(tc.script), nil)
			if err != nil {
				t.Fatalf("readStream: %v", err)
			}
			if msg.Usage == nil {
				t.Fatal("usage is nil")
			}
			u := msg.Usage
			if u.StreamRequestMS != 0 || u.StreamFirstMS != 0 || u.StreamDoneMS != 0 {
				t.Fatalf("timing fields must stay zero when disabled: %+v", u)
			}
			if u.PromptTokens != 10 || u.CompletionTokens != 5 {
				t.Fatalf("token accounting changed: %+v", u)
			}
		})
	}
}

// TestStreamTimingJSONBytesUnchanged 守护字节承诺：打点字段用 json:"-"，
// 因此带时间点与不带时间点的 Usage 序列化结果逐字节相同（会话转录、SSE、
// RPC 帧都不受影响）。
func TestStreamTimingJSONBytesUnchanged(t *testing.T) {
	disable := EnableStreamTiming()
	defer disable()

	msg, err := (&Client{}).readStream(context.Background(), "m", strings.NewReader(chatTimingSSE), nil)
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if msg.Usage == nil || msg.Usage.StreamDoneMS == 0 {
		t.Fatal("expected timing to be recorded")
	}
	stamped, err := json.Marshal(*msg.Usage)
	if err != nil {
		t.Fatalf("marshal stamped: %v", err)
	}
	u := *msg.Usage
	u.StreamRequestMS, u.StreamFirstMS, u.StreamDoneMS = 0, 0, 0
	plain, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if string(stamped) != string(plain) {
		t.Fatalf("timing fields leaked into JSON:\n stamped=%s\n plain=%s", stamped, plain)
	}
}
