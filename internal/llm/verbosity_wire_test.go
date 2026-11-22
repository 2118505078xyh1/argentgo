package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// captureChatOnceWire 启动一个 httptest 端点，返回最小非流式 JSON 响应，
// 并把收到的原始请求体写入返回的指针，供逐字节与字段断言。
func captureChatOnceWire(t *testing.T) (*string, *llm.Client) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
			return
		}
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return &body, llm.New(srv.URL, "k")
}

// TestChatVerbosityDefaultKeepsGoldenBytes 锁定默认线协议字节：Options.Verbosity
// 为空时，请求体必须与接线前逐字节一致（不含 verbosity）。
func TestChatVerbosityDefaultKeepsGoldenBytes(t *testing.T) {
	body, c := captureThinkingWire(t)
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	const golden = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	if *body != golden {
		t.Fatalf("默认请求字节漂移\n got: %s\nwant: %s", *body, golden)
	}
	if strings.Contains(*body, "verbosity") {
		t.Fatalf("默认请求不应含 verbosity: %s", *body)
	}
}

// TestChatVerbosityWire 验证 Options.Verbosity 被映射为 verbosity，
// 且请求体仍是合法 JSON、字段值正确。
func TestChatVerbosityWire(t *testing.T) {
	body, c := captureThinkingWire(t)
	c.Options.Verbosity = "high"
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*body, `"verbosity":"high"`) {
		t.Fatalf("请求体缺少 verbosity=high: %s", *body)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(*body), &decoded); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v; body=%s", err, *body)
	}
	if decoded["verbosity"] != "high" {
		t.Fatalf("verbosity = %#v, want high", decoded["verbosity"])
	}
}

// TestChatVerbosityLegalValues 覆盖 OpenAI 支持的三个合法值，确认逐一
// 出现在请求体中且字段顺序位于末尾。
func TestChatVerbosityLegalValues(t *testing.T) {
	for _, want := range []string{"low", "medium", "high"} {
		t.Run(want, func(t *testing.T) {
			body, c := captureThinkingWire(t)
			c.Options.Verbosity = want
			if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			suffix := `,"verbosity":"` + want + `"}`
			if !strings.HasSuffix(*body, suffix) {
				t.Fatalf("verbosity 应位于请求体末尾\n got: %s\nwant suffix: %s", *body, suffix)
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(*body), &decoded); err != nil {
				t.Fatalf("请求体不是合法 JSON: %v; body=%s", err, *body)
			}
			if decoded["verbosity"] != want {
				t.Fatalf("verbosity = %#v, want %s", decoded["verbosity"], want)
			}
		})
	}
}

// TestChatOnceVerbosityWire 验证非流式 ChatOnce 也复用 buildChatRequest，
// 携带 verbosity；默认（空值）时请求体同样不含该字段。
func TestChatOnceVerbosityWire(t *testing.T) {
	body, c := captureChatOnceWire(t)
	if reply, err := c.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatOnce: %v", err)
	} else if reply.Content != "ok" {
		t.Fatalf("reply.Content = %q, want ok", reply.Content)
	}
	const golden = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`
	if *body != golden {
		t.Fatalf("ChatOnce 默认请求字节漂移\n got: %s\nwant: %s", *body, golden)
	}

	body, c = captureChatOnceWire(t)
	c.Options.Verbosity = "high"
	if _, err := c.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatOnce: %v", err)
	}
	if !strings.Contains(*body, `"verbosity":"high"`) {
		t.Fatalf("ChatOnce 请求体缺少 verbosity=high: %s", *body)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(*body), &decoded); err != nil {
		t.Fatalf("ChatOnce 请求体不是合法 JSON: %v; body=%s", err, *body)
	}
	if decoded["verbosity"] != "high" {
		t.Fatalf("verbosity = %#v, want high", decoded["verbosity"])
	}
	if decoded["stream"] != false {
		t.Fatalf("ChatOnce stream = %#v, want false", decoded["stream"])
	}
}
