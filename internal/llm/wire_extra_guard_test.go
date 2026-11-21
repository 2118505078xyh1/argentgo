package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件守护 extra_headers.go 与 debug_trace.go 对外部消费者的集成契约：
//  1. 三协议默认请求头基线（无注入时逐字节不变）；
//  2. 安装额外头后，真实客户端构造请求并调用 ApplyExtraHeaders 的线上效果，
//     以及在构造后再次显式应用时的幂等性；
//  3. 通过 SetTraceSink 安装收集器后 RecordTrace 的收集、脱敏与并发行为。
//
// 所有流量都走 httptest loopback，不联网。

// resetWireGuardGlobals 在每个测试前后清空两个包级注入状态，避免测试间互相污染。
// 这些测试共享包级状态，不能 t.Parallel。
func resetWireGuardGlobals(t *testing.T) {
	t.Helper()
	llm.ClearExtraHeaders()
	llm.SetTraceSink(nil)
	t.Cleanup(func() {
		llm.ClearExtraHeaders()
		llm.SetTraceSink(nil)
	})
}

// normalizeWireHeader 去掉传输层自动添加的头（User-Agent / Accept-Encoding / Content-Length），
// 返回稳定的单值映射，便于对客户端可控的请求头做逐字节基线比对。
func normalizeWireHeader(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "User-Agent", "Accept-Encoding", "Content-Length":
			continue
		}
		if len(values) != 1 {
			// 注入头用 Header.Set 覆盖，不应出现多值；joined 让失败信息仍可见。
			out[canonical] = strings.Join(values, ",")
			continue
		}
		out[canonical] = values[0]
	}
	return out
}

// wireProtocolCase 把一种协议的最小 SSE 脚本、真实客户端构造器与期望头部组织在一起，
// 供"基线"与"应用注入"两个测试共用，确保两者针对同一份请求。
type wireProtocolCase struct {
	name        string
	script      string
	newClient   func(baseURL string) llm.Chatter
	baseHeaders map[string]string // 无注入时的请求头基线
	inject      map[string]string // 安装到 ApplyExtraHeaders 的头（含覆盖既有头的用例）
	wantHeaders map[string]string // 注入安装后应出现的请求头
}

var wireProtocolCases = []wireProtocolCase{
	{
		name:   "chat-completions",
		script: chatHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.New(u, "test-key")
		},
		baseHeaders: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
			"Authorization": "Bearer test-key",
		},
		inject: map[string]string{
			"Authorization": "Bearer injected", // 覆盖客户端默认头
			"X-Trace":       "trace-123",
			"api-key":       "sk-azure",
		},
		wantHeaders: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
			"Authorization": "Bearer injected",
			"X-Trace":       "trace-123",
			"Api-Key":       "sk-azure", // Header.Set 会规范化头名
		},
	},
	{
		name:   "anthropic-messages",
		script: anthropicHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.NewAnthropic(u, "test-key")
		},
		baseHeaders: map[string]string{
			"Content-Type":      "application/json",
			"Accept":            "text/event-stream",
			"Anthropic-Version": llm.AnthropicVersion,
			"X-Api-Key":         "test-key",
		},
		inject: map[string]string{
			"x-api-key": "injected-key", // 覆盖 anthropic 的 x-api-key
			"X-Trace":   "trace-123",
		},
		wantHeaders: map[string]string{
			"Content-Type":      "application/json",
			"Accept":            "text/event-stream",
			"Anthropic-Version": llm.AnthropicVersion,
			"X-Api-Key":         "injected-key",
			"X-Trace":           "trace-123",
		},
	},
	{
		name:   "responses",
		script: responsesHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.NewResponses(u, "test-key")
		},
		baseHeaders: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
			"Authorization": "Bearer test-key",
		},
		inject: map[string]string{
			"Authorization": "Bearer injected",
			"X-Trace":       "trace-123",
			"api-key":       "sk-azure",
		},
		wantHeaders: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
			"Authorization": "Bearer injected",
			"X-Trace":       "trace-123",
			"Api-Key":       "sk-azure",
		},
	},
}

// captureWireHeaders 用真实客户端发一次请求，在服务端处理器里捕获到达线上的头部。
// alsoApply 为 true 时在请求已经过客户端构造与注入之后，再显式调用一次 ApplyExtraHeaders，
// 用于验证接线后再应用一次的幂等性；false 是客户端的常规线上路径。
func captureWireHeaders(t *testing.T, tc wireProtocolCase, alsoApply bool) http.Header {
	t.Helper()
	captured := make(chan http.Header, 1) // 带缓冲，处理器不会因无人读取而阻塞
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if alsoApply {
			llm.ApplyExtraHeaders(r)
		}
		captured <- r.Header.Clone()
		writeSSE(w, tc.script)
	}))
	defer server.Close()

	if _, err := tc.newClient(server.URL).Chat(context.Background(), "test-model", nil, nil, nil); err != nil {
		t.Fatalf("%s: Chat 失败：%v", tc.name, err)
	}
	select {
	case h := <-captured:
		if h == nil {
			t.Fatalf("%s: 服务端未收到请求", tc.name)
		}
		return h
	default:
		t.Fatalf("%s: 服务端未捕获到请求头", tc.name)
		return nil
	}
}

// TestWireExtraHeadersBaselineNoInjection 固化三协议默认请求头：未安装注入时，
// 真实客户端的线上头部与基线逐字节一致。
func TestWireExtraHeadersBaselineNoInjection(t *testing.T) {
	resetWireGuardGlobals(t)
	for _, tc := range wireProtocolCases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeWireHeader(captureWireHeaders(t, tc, false))
			if !reflect.DeepEqual(got, tc.baseHeaders) {
				t.Fatalf("默认请求头基线漂移\n got: %#v\nwant: %#v", got, tc.baseHeaders)
			}
		})
	}
}

// TestWireExtraHeadersAppliedAfterRequestConstruction 验证接线语义：安装额外头后，
// 真实客户端在构造请求、设置默认头之后应用的注入头到达线上——新增头出现、同名头被覆盖、
// 客户端其余默认头保持不变。同时在服务端再次显式应用一次，验证 ApplyExtraHeaders 幂等。
func TestWireExtraHeadersAppliedAfterRequestConstruction(t *testing.T) {
	resetWireGuardGlobals(t)
	for _, tc := range wireProtocolCases {
		t.Run(tc.name, func(t *testing.T) {
			llm.SetExtraHeaders(tc.inject)
			got := normalizeWireHeader(captureWireHeaders(t, tc, false))
			if !reflect.DeepEqual(got, tc.wantHeaders) {
				t.Fatalf("构造后应用的注入头未正确上线\n got: %#v\nwant: %#v", got, tc.wantHeaders)
			}

			again := normalizeWireHeader(captureWireHeaders(t, tc, true))
			if !reflect.DeepEqual(again, tc.wantHeaders) {
				t.Fatalf("再次显式应用注入头应幂等\n got: %#v\nwant: %#v", again, tc.wantHeaders)
			}
			llm.ClearExtraHeaders()
		})
	}
}

// wireTraceSink 是外部消费者视角的并发安全收集器：TraceSink 契约要求 Record 可并发调用。
type wireTraceSink struct {
	mu      sync.Mutex
	records []llm.RequestTrace
}

func (s *wireTraceSink) Record(rec llm.RequestTrace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

func (s *wireTraceSink) snapshot() []llm.RequestTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.RequestTrace(nil), s.records...)
}

// TestWireRecordTraceWithoutSinkIsNoop 从外部消费者视角验证未安装收集器时调用安全。
func TestWireRecordTraceWithoutSinkIsNoop(t *testing.T) {
	resetWireGuardGlobals(t)
	// 不应 panic，也不应产生任何可观察副作用。
	llm.RecordTrace(llm.ProtocolChatCompletions, "model", 1, 2, 3, 4, "api_key=sk-should-not-leak")
}

// TestWireRecordTraceCollectsRedactsAndScales 验证外部消费者安装收集器后的完整行为：
// 字段逐项收集、Status 脱敏、三协议协议名正确，以及 SetTraceSink/RecordTrace 并发安全。
func TestWireRecordTraceCollectsRedactsAndScales(t *testing.T) {
	resetWireGuardGlobals(t)
	sink := &wireTraceSink{}
	llm.SetTraceSink(sink)

	const secret = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	llm.RecordTrace(llm.ProtocolChatCompletions, "model-a", 10, 20, 5, 123,
		"request failed: api_key="+secret+" contact me@example.com")
	llm.RecordTrace(llm.ProtocolAnthropicMessages, "claude-3", 1, 2, 3, 4, "ok")
	llm.RecordTrace(llm.ProtocolResponses, "gpt-x", 7, 8, 9, 10, "Authorization: Bearer "+secret)

	records := sink.snapshot()
	if len(records) != 3 {
		t.Fatalf("应收集 3 条记录，got %d", len(records))
	}
	wantProtocols := []string{llm.ProtocolChatCompletions, llm.ProtocolAnthropicMessages, llm.ProtocolResponses}
	wantModels := []string{"model-a", "claude-3", "gpt-x"}
	for i := range wantProtocols {
		if records[i].Protocol != wantProtocols[i] {
			t.Errorf("第 %d 条协议名不符：got %q, want %q", i, records[i].Protocol, wantProtocols[i])
		}
		if records[i].Model != wantModels[i] {
			t.Errorf("第 %d 条模型名不符：got %q, want %q", i, records[i].Model, wantModels[i])
		}
	}
	first := records[0]
	if first.PromptTokens != 10 || first.CompletionTokens != 20 || first.CachedTokens != 5 || first.LatencyMS != 123 {
		t.Errorf("第 1 条 token/延迟字段不符：%#v", first)
	}
	if !first.Redacted {
		t.Error("记录应标记为已脱敏")
	}
	if strings.Contains(first.Status, secret) || strings.Contains(first.Status, "me@example.com") {
		t.Errorf("凭据与邮箱不得落入追踪：Status=%q", first.Status)
	}
	if !strings.Contains(first.Status, "request failed") {
		t.Errorf("普通错误措辞应保留：Status=%q", first.Status)
	}
	if strings.Contains(records[2].Status, secret) {
		t.Errorf("Authorization 中的凭据不得落入追踪：Status=%q", records[2].Status)
	}

	// 并发：安装收集器与记录追踪同时发生，记录不丢且不泄露凭据。
	const goroutines = 32
	before := len(records)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			llm.SetTraceSink(sink) // 写入同一个收集器，结果不依赖执行顺序
		}()
		go func() {
			defer wg.Done()
			<-start
			llm.RecordTrace(llm.ProtocolResponses, "model", 1, 2, 3, 4, "api_key="+secret)
		}()
	}
	close(start)
	wg.Wait()

	all := sink.snapshot()
	if len(all) != before+goroutines {
		t.Fatalf("并发后记录数 = %d，want %d", len(all), before+goroutines)
	}
	for _, rec := range all {
		if strings.Contains(rec.Status, secret) {
			t.Fatalf("并发记录同样不得泄露凭据：Status=%q", rec.Status)
		}
	}
}
