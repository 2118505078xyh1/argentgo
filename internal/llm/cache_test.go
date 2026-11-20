package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// opaqueChatter 有意不实现 PromptCacheCapable。
type opaqueChatter struct{}

func (opaqueChatter) Chat(context.Context, string, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
	return llm.Message{}, nil
}

func TestCacheSupportDefaultsByProtocolAndEndpoint(t *testing.T) {
	forcedLocalChat := llm.New("http://localhost:11434/v1", "")
	forcedLocalChat.Options.ForcePromptCache = true
	disabledAnthropic := llm.NewAnthropic("https://api.anthropic.com/v1", "")
	disabledAnthropic.Options.NoPromptCache = true

	cases := []struct {
		name      string
		chatter   llm.Chatter
		supported bool
		local     bool
	}{
		{"local chat completions", llm.New("http://127.0.0.1:11434/v1", ""), false, true},
		{"remote chat completions", llm.New("https://api.openai.com/v1", ""), false, false},
		{"forced open-compatible chat", forcedLocalChat, true, true},
		{"local anthropic", llm.NewAnthropic("http://127.0.0.1:1234/v1", ""), false, true},
		{"remote anthropic", llm.NewAnthropic("https://api.anthropic.com/v1", ""), true, false},
		{"opted-out anthropic", disabledAnthropic, false, false},
		{"remote responses", llm.NewResponses("https://api.openai.com/v1", ""), true, false},
		{"unknown chatter", opaqueChatter{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.CacheSupport(tc.chatter)
			if got.Supported != tc.supported {
				t.Fatalf("Supported = %v, want %v", got.Supported, tc.supported)
			}
			if got.Local != tc.local {
				t.Fatalf("Local = %v, want %v", got.Local, tc.local)
			}
			if tc.supported && got.Protocol == "" {
				t.Fatal("a capable chatter must report its protocol")
			}
		})
	}
}

func stableMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read it"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "1", Content: "file body"},
	}
}

func twoToolSpecs() []llm.ToolSpec {
	tools := []llm.ToolSpec{{Type: "function"}, {Type: "function"}}
	tools[0].Function.Name = "read"
	tools[1].Function.Name = "write"
	return tools
}

func TestAnthropicEmitsCacheMarkersOnlyOnTheStablePrefix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noCache    bool
		wantMarker bool
	}{
		{"enabled", false, true},
		{"opted out", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			var header http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Clone()
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("decode body: %v", err)
				}
				writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()

			client := llm.NewAnthropic(server.URL, "secret")
			client.Options.ForcePromptCache = true
			client.Options.NoPromptCache = tc.noCache
			if _, err := client.Chat(context.Background(), "m", stableMessages(), twoToolSpecs(), nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if got := header.Get("x-api-key"); got != "secret" {
				t.Fatalf("x-api-key = %q", got)
			}
			if got := header.Get("anthropic-version"); got != llm.AnthropicVersion {
				t.Fatalf("anthropic-version = %q", got)
			}

			tools, _ := body["tools"].([]any)
			if len(tools) != 2 {
				t.Fatalf("tools = %#v", body["tools"])
			}
			lastTool, _ := tools[1].(map[string]any)
			_, lastMarked := lastTool["cache_control"]
			if lastMarked != tc.wantMarker {
				t.Fatalf("last tool marked = %v, want %v: %#v", lastMarked, tc.wantMarker, lastTool)
			}
			firstTool, _ := tools[0].(map[string]any)
			if _, ok := firstTool["cache_control"]; ok {
				t.Fatalf("only the last tool schema may be marked: %#v", firstTool)
			}

			switch system := body["system"].(type) {
			case []any:
				if !tc.wantMarker {
					t.Fatalf("system should stay a string when opted out: %#v", system)
				}
				block, _ := system[0].(map[string]any)
				control, _ := block["cache_control"].(map[string]any)
				if block["text"] != "be terse" || control["type"] != "ephemeral" {
					t.Fatalf("system block = %#v", block)
				}
			case string:
				if tc.wantMarker {
					t.Fatalf("system should be a cacheable block: %q", system)
				}
				if system != "be terse" {
					t.Fatalf("system = %q", system)
				}
			default:
				t.Fatalf("system = %#v", body["system"])
			}

			wire, _ := body["messages"].([]any)
			for i, message := range wire {
				entry, _ := message.(map[string]any)
				if _, ok := entry["cache_control"]; ok {
					t.Fatalf("conversation message %d carries a cache marker", i)
				}
			}
		})
	}
}

func TestChatCompletionsCacheMarkerAndUsage(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeSSE(w, "data: {\"usage\":{\"prompt_tokens\":60,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":25}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := llm.New(server.URL, "key")
	client.Options.ForcePromptCache = true
	client.Options.CacheRetention = "1h"
	reply, err := client.Chat(context.Background(), "m", stableMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage == nil || reply.Usage.CachedTokens != 25 || reply.Usage.PromptTokens != 60 {
		t.Fatalf("usage = %+v", reply.Usage)
	}
	if reply.Usage.FreshPromptTokens() != 35 || reply.Usage.TotalTokens() != 64 {
		t.Fatalf("fresh=%d total=%d", reply.Usage.FreshPromptTokens(), reply.Usage.TotalTokens())
	}

	wire, _ := body["messages"].([]any)
	first, _ := wire[0].(map[string]any)
	control, _ := first["cache_control"].(map[string]any)
	if control["type"] != "ephemeral" || control["ttl"] != "1h" {
		t.Fatalf("first message cache_control = %#v", first["cache_control"])
	}
	for i := 1; i < len(wire); i++ {
		message, _ := wire[i].(map[string]any)
		if _, ok := message["cache_control"]; ok {
			t.Fatalf("message %d unexpectedly marked", i)
		}
	}
	stream, _ := body["stream_options"].(map[string]any)
	if stream["include_usage"] != true {
		t.Fatalf("stream_options = %#v", body["stream_options"])
	}
	if body["prompt_cache_retention"] != "1h" {
		t.Fatalf("prompt_cache_retention = %#v", body["prompt_cache_retention"])
	}
}

func TestChatCompletionsStayCacheFreeByDefault(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		writeSSE(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := llm.New(server.URL, "")
	reply, err := client.Chat(context.Background(), "m", stableMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage == nil || reply.Usage.CachedTokens != 0 {
		t.Fatalf("zero-cache usage = %+v", reply.Usage)
	}
	wire, _ := body["messages"].([]any)
	first, _ := wire[0].(map[string]any)
	if _, ok := first["cache_control"]; ok {
		t.Fatalf("local endpoint was marked: %#v", first)
	}
	if _, ok := body["stream_options"]; ok {
		t.Fatalf("local endpoint should not ask for usage: %#v", body["stream_options"])
	}
}

func TestResponsesUsesStableCacheKeyAndCountsCachedTokens(t *testing.T) {
	var body map[string]any
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":90,\"output_tokens\":6,\"input_tokens_details\":{\"cached_tokens\":70}}}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "secret")
	client.Options.ForcePromptCache = true
	reply, err := client.Chat(context.Background(), "m", stableMessages(), twoToolSpecs(), nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage == nil || reply.Usage.CachedTokens != 70 || reply.Usage.PromptTokens != 90 {
		t.Fatalf("usage = %+v", reply.Usage)
	}
	key, _ := body["prompt_cache_key"].(string)
	if !strings.HasPrefix(key, "sha256:") {
		t.Fatalf("prompt_cache_key = %#v", body["prompt_cache_key"])
	}
	if got := header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if _, ok := body["prompt_cache_retention"]; ok {
		t.Fatal("an unset retention hint must not be serialized")
	}

	// 不同的稳定前缀必须产生不同的键，且相同的
	// 前缀在各请求之间必须是确定性的。
	firstKey := key
	if _, err := client.Chat(context.Background(), "m", stableMessages(), twoToolSpecs(), nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := body["prompt_cache_key"]; got != firstKey {
		t.Fatalf("cache key drifted: %v != %v", got, firstKey)
	}
	changed := stableMessages()
	changed[0].Content = "a different system prompt"
	if _, err := client.Chat(context.Background(), "m", changed, twoToolSpecs(), nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := body["prompt_cache_key"]; got == firstKey {
		t.Fatal("a changed stable prefix reused the cache key")
	}
}

func TestResponsesStayCacheFreeByDefault(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	reply, err := llm.NewResponses(server.URL, "k").Chat(context.Background(), "m", stableMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage == nil || reply.Usage.CachedTokens != 0 {
		t.Fatalf("zero-cache usage = %+v", reply.Usage)
	}
	for _, field := range []string{"prompt_cache_key", "prompt_cache_retention"} {
		if _, ok := body[field]; ok {
			t.Fatalf("local endpoint sent %s", field)
		}
	}
}

func TestAnthropicCacheReadAndWriteTokensAreSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":1}}}

data: {"type":"message_delta","usage":{"output_tokens":4}}

data: {"type":"message_stop"}

`)
	}))
	defer server.Close()

	reply, err := llm.NewAnthropic(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage == nil {
		t.Fatal("no usage reported")
	}
	if reply.Usage.PromptTokens != 60 || reply.Usage.CachedTokens != 30 || reply.Usage.CacheWriteTokens != 20 {
		t.Fatalf("usage = %+v", reply.Usage)
	}
	if reply.Usage.FreshPromptTokens() != 30 {
		t.Fatalf("fresh prompt tokens = %d, want 30", reply.Usage.FreshPromptTokens())
	}
}

func TestCancellationMidStreamLeavesNoPartialAccounting(t *testing.T) {
	cases := []struct {
		name  string
		build func(string) llm.Chatter
		first string
		usage string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "k") },
			first: `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n",
			usage: `data: {"usage":{"prompt_tokens":50,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":20}}}` + "\n\n",
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "k") },
			first: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n",
			usage: `data: {"type":"message_delta","usage":{"output_tokens":9}}` + "\n\n",
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "k") },
			first: `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n",
			usage: `data: {"type":"response.completed","response":{"usage":{"input_tokens":50,"output_tokens":9,"input_tokens_details":{"cached_tokens":20}}}}` + "\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.first)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(started)
				<-release
				io.WriteString(w, tc.usage)
			}))
			defer server.Close()
			defer close(release)

			ctx, cancel := context.WithCancel(context.Background())
			type outcome struct {
				reply llm.Message
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				reply, err := tc.build(server.URL).Chat(ctx, "m", stableMessages(), nil, nil)
				done <- outcome{reply, err}
			}()
			<-started
			cancel()
			select {
			case got := <-done:
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("err = %v, want context.Canceled", got.err)
				}
				if got.reply.Usage != nil || got.reply.Content != "" {
					t.Fatalf("partial stream was accounted: %+v", got.reply)
				}
			case <-release:
				t.Fatal("unreachable")
			}
		})
	}
}

// anthropicRequestBody 对本地测试服务器运行一次 Chat，并返回
// 解码后的请求体。客户端由调用方配置，使每个
// 用例可以独立演练缓存启用与保留设置。
func anthropicRequestBody(t *testing.T, configure func(*llm.AnthropicClient), messages []llm.Message, tools []llm.ToolSpec) map[string]any {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "k")
	if configure != nil {
		configure(client)
	}
	if _, err := client.Chat(context.Background(), "m", messages, tools, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return body
}

// toolResultBlocks 按线协议顺序返回所有消息中的每个 tool_result 块，
// 使测试能区分最后一个与更早的结果。
func toolResultBlocks(body map[string]any) []map[string]any {
	wire, _ := body["messages"].([]any)
	var blocks []map[string]any
	for _, rawMessage := range wire {
		message, _ := rawMessage.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_result" {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks
}

func messagesWithTwoToolResults() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read both"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "1", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`}},
			{ID: "2", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`}},
		}},
		{Role: "tool", ToolCallID: "1", Content: "first result"},
		{Role: "tool", ToolCallID: "2", Content: "second result"},
	}
}

func messagesWithoutToolResults() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
}

func TestAnthropicConversationTailBreakpoint(t *testing.T) {
	forced := func(retention string) func(*llm.AnthropicClient) {
		return func(c *llm.AnthropicClient) {
			c.Options.ForcePromptCache = true
			c.Options.CacheRetention = retention
		}
	}
	cases := []struct {
		name           string
		configure      func(*llm.AnthropicClient)
		messages       []llm.Message
		wantTailMark   bool
		wantTailTTL    string
		wantStableMark bool
	}{
		{
			name:           "enabled marks the final tool result with a validated tier",
			configure:      forced("24h"),
			messages:       messagesWithTwoToolResults(),
			wantTailMark:   true,
			wantTailTTL:    "24h",
			wantStableMark: true,
		},
		{
			name:           "invalid retention tier drops the tail hint",
			configure:      forced("10s"),
			messages:       messagesWithTwoToolResults(),
			wantTailMark:   true,
			wantTailTTL:    "",
			wantStableMark: true,
		},
		{
			name: "no prompt cache emits no tail marker",
			configure: func(c *llm.AnthropicClient) {
				forced("24h")(c)
				c.Options.NoPromptCache = true
			},
			messages:       messagesWithTwoToolResults(),
			wantTailMark:   false,
			wantStableMark: false,
		},
		{
			name: "local endpoint keeps cache auto-detection",
			configure: func(c *llm.AnthropicClient) {
				c.Options.CacheRetention = "24h"
			},
			messages:       messagesWithTwoToolResults(),
			wantTailMark:   false,
			wantStableMark: false,
		},
		{
			name:           "request without tool results is unchanged",
			configure:      forced("24h"),
			messages:       messagesWithoutToolResults(),
			wantTailMark:   false,
			wantStableMark: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := anthropicRequestBody(t, tc.configure, tc.messages, twoToolSpecs())
			blocks := toolResultBlocks(body)
			if tc.wantTailMark && len(blocks) < 2 {
				t.Fatalf("need at least two tool results to identify the last one, got %d", len(blocks))
			}
			for i, block := range blocks {
				control, marked := block["cache_control"].(map[string]any)
				wantMarked := tc.wantTailMark && i == len(blocks)-1
				if marked != wantMarked {
					t.Fatalf("tool_result[%d] marked = %v, want %v", i, marked, wantMarked)
				}
				if !wantMarked {
					continue
				}
				if control["type"] != "ephemeral" {
					t.Fatalf("tool_result[%d] cache_control = %#v", i, control)
				}
				ttl, hasTTL := control["ttl"]
				if tc.wantTailTTL == "" {
					if hasTTL {
						t.Fatalf("tool_result[%d] has unexpected ttl: %#v", i, ttl)
					}
				} else if ttl != tc.wantTailTTL {
					t.Fatalf("tool_result[%d] ttl = %#v, want %q", i, ttl, tc.wantTailTTL)
				}
			}

			tools, _ := body["tools"].([]any)
			if len(tools) == 0 {
				t.Fatal("request has no tool schemas")
			}
			lastTool, _ := tools[len(tools)-1].(map[string]any)
			if _, ok := lastTool["cache_control"]; ok != tc.wantStableMark {
				t.Fatalf("last tool schema marked = %v, want %v", ok, tc.wantStableMark)
			}
			switch system := body["system"].(type) {
			case []any:
				if !tc.wantStableMark {
					t.Fatalf("system should stay a string when caching is off: %#v", system)
				}
			case string:
				if tc.wantStableMark {
					t.Fatalf("system should be a cacheable block: %q", system)
				}
			default:
				t.Fatalf("system = %#v", body["system"])
			}
		})
	}
}
