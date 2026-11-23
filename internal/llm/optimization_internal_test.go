package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- #9 输出上限拟合 ----------

// TestFitOutputTokens 锁定输出拟合的最优参数：下限 1024、余量 64、误差 1.1、
// 每条消息 16 framing；未配置窗口时逐字节不变，且只收紧不放宽。
func TestFitOutputTokens(t *testing.T) {
	countASCII := func(s string) int { return (len(s) + 3) / 4 }

	tests := []struct {
		name      string
		maxTokens int
		window    int
		msgs      []Message
		estimate  func(string) int
		want      int
	}{
		{
			name:      "unknown window keeps cap",
			maxTokens: 8192,
			window:    0,
			msgs:      []Message{{Role: "user", Content: "x"}},
			want:      8192,
		},
		{
			name:      "ample window keeps cap",
			maxTokens: 8192,
			window:    1_000_000,
			msgs:      []Message{{Role: "user", Content: "short"}},
			estimate:  countASCII,
			want:      8192,
		},
		{
			name:      "near-full window shrinks to floor",
			maxTokens: 8192,
			window:    1100,
			msgs:      []Message{{Role: "user", Content: strings.Repeat("a", 4000)}},
			estimate:  countASCII,
			want:      fitOutputFloor,
		},
		{
			name:      "anchor skips history before it",
			maxTokens: 8192,
			window:    5000,
			msgs: []Message{
				{Role: "user", Content: strings.Repeat("a", 100_000)}, // 旧历史，锚点前，不计
				{Role: "assistant", Content: "ok", Usage: &Usage{PromptTokens: 3000}},
				{Role: "user", Content: strings.Repeat("b", 400)}, // 100 token * 1.1 + 16
			},
			estimate: countASCII,
			want:     5000 - (3000 + 126) - fitOutputMargin, // 1810
		},
		{
			name:      "never above requested cap",
			maxTokens: 512,
			window:    1_000_000,
			msgs:      []Message{{Role: "user", Content: "hi"}},
			estimate:  countASCII,
			want:      512,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FitOutputTokens(tt.maxTokens, tt.window, tt.msgs, tt.estimate); got != tt.want {
				t.Fatalf("FitOutputTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestFitOutputTokensWireBytes 用假 provider 锁定：近满窗口时请求里的
// max_tokens 被收紧；未配置窗口时请求字节不出现 max_tokens。
func TestFitOutputTokensWireBytes(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	msgs := []Message{
		{Role: "assistant", Content: "prior", Usage: &Usage{PromptTokens: 3000}},
		{Role: "user", Content: strings.Repeat("a", 2000)}, // 500 * 1.1 + 16 = 566
	}

	// 配置了小窗口：可用 = 4096 - (3000+566) - 64 = 466 → 下限 1024。
	client := New(server.URL, "")
	client.Options.ContextWindow = 4096
	if _, err := client.Chat(context.Background(), "m", msgs, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var fitted map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &fitted); err != nil {
		t.Fatalf("decode fitted request: %v", err)
	}
	if got := fitted["max_tokens"]; got != float64(fitOutputFloor) {
		t.Fatalf("fitted max_tokens = %v, want %d", got, fitOutputFloor)
	}

	// 未配置窗口：不写 max_tokens，保持历史线协议字节。
	client.Options.ContextWindow = 0
	if _, err := client.Chat(context.Background(), "m", msgs, nil, nil); err != nil {
		t.Fatalf("Chat without window: %v", err)
	}
	var plain map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &plain); err != nil {
		t.Fatalf("decode plain request: %v", err)
	}
	if _, ok := plain["max_tokens"]; ok {
		t.Fatalf("unfitted request must not carry max_tokens: %s", bodies[1])
	}
}

// ---------- #8 原生压缩 ----------

// TestNativeCompactTrigger 锁定：Compact 以 stream:false + compact 标记发出，
// 响应中的替换窗口随 Message 返回，且只认可原模型回放。
func TestNativeCompactTrigger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request["stream"] != false {
			t.Errorf("stream = %v, want false", request["stream"])
		}
		if request["compact"] != true {
			t.Errorf("compact = %v, want true", request["compact"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"native_window":[{"role":"user","content":"kept"}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "k")
	reply, err := client.Compact(context.Background(), "model-a", []Message{{Role: "user", Content: "fill"}})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if reply.CompactModel != "model-a" || len(reply.NativeWindow) != 1 || reply.NativeWindow[0].Content != "kept" {
		t.Fatalf("unexpected compact reply: %+v", reply)
	}

	// 原模型回放展开窗口；别的模型静默回退原消息。
	msgs := []Message{{Role: "system", Content: "s"}, reply}
	if got := replayNativeWindow("model-a", msgs); len(got) != 1 || got[0].Content != "kept" {
		t.Fatalf("replay on same model = %+v", got)
	}
	if got := replayNativeWindow("model-b", msgs); len(got) != 2 {
		t.Fatalf("replay on other model should keep original messages: %+v", got)
	}
}

// TestNativeCompactFallsBackText 锁定：提供方不支持（无替换窗口）或 Chatter
// 未实现 Compacter 时静默回退文本压缩，不抛错给用户。
func TestNativeCompactFallsBackText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New(server.URL, "")
	if _, err := client.Compact(context.Background(), "m", nil); !errors.Is(err, ErrCompactUnsupported) {
		t.Fatalf("Compact error = %v, want ErrCompactUnsupported", err)
	}
	window, ok := compactWith(context.Background(), client, "m", nil)
	if ok || window != nil {
		t.Fatalf("compactWith on unsupported provider = (%v, %v), want (nil, false)", window, ok)
	}
	window, ok = compactWith(context.Background(), opaqueChatter{}, "m", nil)
	if ok || window != nil {
		t.Fatalf("compactWith on non-Compacter = (%v, %v), want (nil, false)", window, ok)
	}
}

type opaqueChatter struct{}

func (opaqueChatter) Chat(context.Context, string, []Message, []ToolSpec, func(string)) (Message, error) {
	return Message{}, fmt.Errorf("not implemented")
}

// ---------- #30 拒绝不回放 ----------

// TestRefusalReplay 锁定：三协议的拒绝类别解析为 omitempty 字段；回放时丢弃
// 拒绝的 assistant 轮，并把流错误占位改写为「未执行」。
func TestRefusalReplay(t *testing.T) {
	// 解析：仅拒绝类别结构化，普通停止原因不写字段。
	for _, reason := range []string{"refusal", "sensitive", "content_filter"} {
		if got := parseRefusalReason(reason); got == nil || got.Reason != reason {
			t.Fatalf("parseRefusalReason(%q) = %+v", reason, got)
		}
	}
	for _, reason := range []string{"stop", "end_turn", "tool_calls", "length", ""} {
		if got := parseRefusalReason(reason); got != nil {
			t.Fatalf("parseRefusalReason(%q) = %+v, want nil", reason, got)
		}
	}

	msgs := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "I cannot help", FinishReason: "refusal", Refusal: &ProviderRefusal{Reason: "refusal"}},
		{Role: "assistant", Content: `{"__synthetic":true,"source":"assistant_stop_error","error":"refused by policy"}`},
		{Role: "user", Content: "second"},
	}
	replayed := providerReplayMessages(msgs)
	if len(replayed) != 3 {
		t.Fatalf("replayed length = %d, want 3 (refusal turn dropped): %+v", len(replayed), replayed)
	}
	if replayed[0].Content != "first" || replayed[2].Content != "second" {
		t.Fatalf("replayed ends changed: %+v", replayed)
	}
	if replayed[1].Content != providerErrorNotExecutedText {
		t.Fatalf("synthetic placeholder = %q, want %q", replayed[1].Content, providerErrorNotExecutedText)
	}
	if strings.Contains(replayed[1].Content, "refused by policy") {
		t.Fatalf("provider refusal reason leaked into replay: %q", replayed[1].Content)
	}
	// 原切片未被修改。
	if msgs[1].Content != "I cannot help" || !strings.Contains(msgs[2].Content, "refused by policy") {
		t.Fatalf("providerReplayMessages mutated its input: %+v", msgs)
	}
}
