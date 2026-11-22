package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// responsesOptInWire 用给定的 Options 构建默认请求并序列化，
// 返回逐字节的请求体，便于锁定线协议格式。
// 使用本地端点，避免默认的 Responses 提示缓存键污染基线字节。
func responsesOptInWire(t *testing.T, opts Options) string {
	t.Helper()
	client := NewResponses("http://127.0.0.1:1", "k")
	client.Options = opts
	raw, err := json.Marshal(client.buildRequest("m", []Message{{Role: "user", Content: "hi"}}, nil))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(raw)
}

// TestResponsesOptInFieldsDefaultBytes 锁定默认线协议字节：三个 opt-in 字段
// 全部关闭时，请求体必须与接线前逐字节一致（store 仍为 false，
// 且不含 reasoning / verbosity / summary）。
func TestResponsesOptInFieldsDefaultBytes(t *testing.T) {
	const golden = `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":8192,"stream":true,"store":false}`
	got := responsesOptInWire(t, Options{})
	if got != golden {
		t.Fatalf("默认请求字节漂移\n got: %s\nwant: %s", got, golden)
	}
	for _, key := range []string{"reasoning", "summary", "verbosity"} {
		if strings.Contains(got, `"`+key+`"`) {
			t.Fatalf("默认请求不应含 %s: %s", key, got)
		}
	}
}

// TestResponsesReasoningSummaryOnly 验证仅开启推理摘要时，
// 请求携带 reasoning.summary，且不含 effort、verbosity，store 仍为 false。
func TestResponsesReasoningSummaryOnly(t *testing.T) {
	got := responsesOptInWire(t, Options{ReasoningSummary: "detailed"})
	if !strings.Contains(got, `"reasoning":{"summary":"detailed"}`) {
		t.Fatalf("请求缺少 reasoning.summary=detailed: %s", got)
	}
	if strings.Contains(got, `"effort"`) || strings.Contains(got, `"verbosity"`) {
		t.Fatalf("仅开启摘要时不应含 effort/verbosity: %s", got)
	}
	if !strings.Contains(got, `"store":false`) {
		t.Fatalf("store 应保持 false: %s", got)
	}

	var decoded struct {
		Reasoning *responsesReasoning `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("请求不是合法 JSON: %v; body=%s", err, got)
	}
	if decoded.Reasoning == nil || decoded.Reasoning.Summary != "detailed" || decoded.Reasoning.Effort != "" {
		t.Fatalf("reasoning = %+v, want summary=detailed/effort 为空", decoded.Reasoning)
	}
}

// TestResponsesStoreOnly 验证仅开启 store 时发送 store:true，
// 且不引入 reasoning 或 verbosity。
func TestResponsesStoreOnly(t *testing.T) {
	got := responsesOptInWire(t, Options{StoreResponses: true})
	if !strings.Contains(got, `"store":true`) {
		t.Fatalf("请求应含 store:true: %s", got)
	}
	if strings.Contains(got, `"reasoning"`) || strings.Contains(got, `"verbosity"`) || strings.Contains(got, `"summary"`) {
		t.Fatalf("仅开启 store 时不应含其它 opt-in 字段: %s", got)
	}
}

// TestResponsesVerbosityOnly 验证仅设置 verbosity 时映射为顶层 verbosity，
// 且 store 仍为 false、不引入 reasoning。
func TestResponsesVerbosityOnly(t *testing.T) {
	got := responsesOptInWire(t, Options{Verbosity: "high"})
	if !strings.Contains(got, `"verbosity":"high"`) {
		t.Fatalf("请求缺少 verbosity=high: %s", got)
	}
	if strings.Contains(got, `"reasoning"`) || strings.Contains(got, `"summary"`) {
		t.Fatalf("仅开启 verbosity 时不应含 reasoning/summary: %s", got)
	}
	if !strings.Contains(got, `"store":false`) {
		t.Fatalf("store 应保持 false: %s", got)
	}
}

// TestResponsesOptInFieldsCombination 验证三个字段同时开启（并带 effort）时，
// reasoning 同时携带 effort 与 summary，store 为 true，verbosity 正确映射。
func TestResponsesOptInFieldsCombination(t *testing.T) {
	opts := Options{
		Thinking:         &ThinkingConfig{Effort: "high"},
		ReasoningSummary: "detailed",
		StoreResponses:   true,
		Verbosity:        "low",
	}
	got := responsesOptInWire(t, opts)
	for _, want := range []string{
		`"reasoning":{"effort":"high","summary":"detailed"}`,
		`"store":true`,
		`"verbosity":"low"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("组合请求缺少 %s: %s", want, got)
		}
	}
}

// TestResponsesVerbosityLegalValues 验证 Responses 接受的合法档位
// low/medium/high 均按原值透传，且只有 verbosity 字段被添加。
func TestResponsesVerbosityLegalValues(t *testing.T) {
	for _, value := range []string{"low", "medium", "high"} {
		t.Run(value, func(t *testing.T) {
			got := responsesOptInWire(t, Options{Verbosity: value})
			if !strings.Contains(got, `"verbosity":"`+value+`"`) {
				t.Fatalf("verbosity = %s 未正确映射: %s", value, got)
			}
			if strings.Contains(got, `"reasoning"`) || strings.Contains(got, `"store":true`) {
				t.Fatalf("verbosity 档位不应影响其它字段: %s", got)
			}
		})
	}
}

// TestResponsesOptInFieldsCJKUnaffected 验证三个 opt-in 字段与消息内容是否
// 含 CJK 无关：CJK 文本被完整保留（UTF-8 未被切断），且字段表现与
// ASCII 内容一致。摘要本身是枚举值，不做任何截断或本地化处理。
func TestResponsesOptInFieldsCJKUnaffected(t *testing.T) {
	client := NewResponses("http://127.0.0.1:1", "k")
	client.Options = Options{
		Thinking:         &ThinkingConfig{Effort: "medium"},
		ReasoningSummary: "auto",
		StoreResponses:   true,
		Verbosity:        "high",
	}
	messages := []Message{{Role: "user", Content: "请用中文回答：你好，世界！"}}
	raw, err := json.Marshal(client.buildRequest("m", messages, nil))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	got := string(raw)

	if !strings.Contains(got, "请用中文回答：你好，世界！") {
		t.Fatalf("CJK 文本未被完整保留: %s", got)
	}
	for _, want := range []string{
		`"reasoning":{"effort":"medium","summary":"auto"}`,
		`"store":true`,
		`"verbosity":"high"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("CJK 内容下缺少 %s: %s", want, got)
		}
	}
}

// TestResponsesChatOnceOptInFields 验证 ChatOnce 非流式路径复用 buildRequest：
// 默认不出现任何 opt-in 字段；开启后三个字段与流式 Chat 一致，且 stream 为 false。
func TestResponsesChatOnceOptInFields(t *testing.T) {
	var bodyCh chan string
	newServer := func() *httptest.Server {
		bodyCh = make(chan string, 1)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
				return
			}
			bodyCh <- string(raw)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
		}))
	}

	callOnce := func(client *ResponsesClient) string {
		t.Helper()
		if _, err := client.ChatOnce(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatalf("ChatOnce: %v", err)
		}
		return <-bodyCh
	}

	srv := newServer()
	defer srv.Close()
	client := NewResponses(srv.URL, "k")

	defaultBody := callOnce(client)
	if !strings.Contains(defaultBody, `"stream":false`) {
		t.Fatalf("ChatOnce 应为非流式: %s", defaultBody)
	}
	for _, key := range []string{"reasoning", "summary", "verbosity"} {
		if strings.Contains(defaultBody, `"`+key+`"`) {
			t.Fatalf("默认 ChatOnce 不应含 %s: %s", key, defaultBody)
		}
	}
	if !strings.Contains(defaultBody, `"store":false`) {
		t.Fatalf("默认 ChatOnce store 应为 false: %s", defaultBody)
	}

	client.Options = Options{
		ReasoningSummary: "concise",
		StoreResponses:   true,
		Verbosity:        "medium",
	}
	enabledBody := callOnce(client)
	for _, want := range []string{
		`"reasoning":{"summary":"concise"}`,
		`"store":true`,
		`"verbosity":"medium"`,
		`"stream":false`,
	} {
		if !strings.Contains(enabledBody, want) {
			t.Fatalf("ChatOnce 缺少 %s: %s", want, enabledBody)
		}
	}
}
