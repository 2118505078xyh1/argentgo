package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/eval"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件兑现 R16-R18 的 ChatterFactory / ARGENT_EVAL_LIVE / Trace.Usage 投资：
// 用 httptest loopback 上的假 SSE 服务端，让真实的三协议 llm.Chatter 驱动
// eval.Run + evaler.runScenario，覆盖"eval 完全不覆盖压缩/真实 wire"的盲区。
// 全程不联网、不需要 API key、不写 golden；直接调用 runScenario 绕过 RunSuite 的 key 门禁。

const (
	liveLocalNote    = "hello from seed\n"
	liveLocalAnswer  = "the note says hello from seed"
	liveLocalKey     = "test-key"
	liveLocalBaseURL = "/v1" // 客户端再拼接 /chat/completions、/messages、/responses
)

// liveLocalProtocol 描述一种协议在假服务端上的可观测差异。
type liveLocalProtocol struct {
	name       string // 传给 newChatter 的协议名
	path       string // 期望的请求路径
	authHeader string // 期望携带凭证的 header
	authValue  string // 期望的 header 值
	// toolResultMarker 是第二轮请求中工具结果在 wire 上的形态标记，
	// 用于断言工具结果确实回到了发给模型的消息里（消息形态正确）。
	toolResultMarker string
	toolScript       string
	finalScript      string
	// usagePrompt 是最终脚本在 Trace.Usage.PromptTokens 上的期望值。
	// Anthropic 客户端把 cache_read 计进 PromptTokens（baseInput + cached），故为 25；其余协议为 20。
	usagePrompt int
}

func liveLocalProtocols() []liveLocalProtocol {
	return []liveLocalProtocol{
		{
			name:             "chat",
			path:             "/v1/chat/completions",
			authHeader:       "Authorization",
			authValue:        "Bearer " + liveLocalKey,
			toolResultMarker: `"role":"tool"`,
			usagePrompt:      20,
			toolScript: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_read_1","function":{"name":"read","arguments":"{\"path\":\"note.txt\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}

data: [DONE]

`,
			finalScript: `data: {"choices":[{"index":0,"delta":{"content":"the note says hello from seed"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"usage":{"prompt_tokens":20,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":5}}}

data: [DONE]

`,
		},
		{
			name:             "anthropic",
			path:             "/v1/messages",
			authHeader:       "x-api-key",
			authValue:        liveLocalKey,
			toolResultMarker: `"type":"tool_result"`,
			usagePrompt:      25,
			toolScript: `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":4,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"note.txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`,
			finalScript: `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":20,"cache_read_input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the note says hello from seed"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}

event: message_stop
data: {"type":"message_stop"}

`,
		},
		{
			name:             "responses",
			path:             "/v1/responses",
			authHeader:       "Authorization",
			authValue:        "Bearer " + liveLocalKey,
			toolResultMarker: `"type":"function_call_output"`,
			usagePrompt:      20,
			toolScript: `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_read_1","name":"read","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"note.txt\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call"}}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15,"input_tokens_details":{"cached_tokens":4}}}}

`,
			finalScript: `event: response.created
data: {"type":"response.created","response":{"id":"resp_2"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"the note says hello from seed"}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":6,"total_tokens":26,"input_tokens_details":{"cached_tokens":5}}}}

`,
		},
	}
}

// liveLocalRequest 记录一次到达假服务端的请求，供 SSE 保真度断言。
type liveLocalRequest struct {
	path   string
	header http.Header
	body   string
}

type liveLocalRecorder struct {
	mu       sync.Mutex
	requests []liveLocalRequest
}

func (r *liveLocalRecorder) snapshot() []liveLocalRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]liveLocalRequest(nil), r.requests...)
}

// newLiveLocalServer 按请求顺序回放 scripts；脚本用尽时重复最后一条。
func newLiveLocalServer(t *testing.T, scripts ...string) (*httptest.Server, *liveLocalRecorder) {
	t.Helper()
	rec := &liveLocalRecorder{}
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		n := next
		next++
		mu.Unlock()
		body, _ := io.ReadAll(req.Body)
		rec.mu.Lock()
		rec.requests = append(rec.requests, liveLocalRequest{
			path:   req.URL.Path,
			header: req.Header.Clone(),
			body:   string(body),
		})
		rec.mu.Unlock()
		script := scripts[len(scripts)-1]
		if n < len(scripts) {
			script = scripts[n]
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// runLiveLocal 把一条真实 chatter 注入 eval.Run，并断言场景通过。
func runLiveLocal(t *testing.T, p liveLocalProtocol, sc eval.Scenario, scripts ...string) (eval.Result, *liveLocalRecorder) {
	t.Helper()
	srv, rec := newLiveLocalServer(t, scripts...)
	opts := eval.Options{
		Out: io.Discard,
		ChatterFactory: func(eval.Scenario) llm.Chatter {
			return newChatter(p.name, srv.URL+liveLocalBaseURL, liveLocalKey, llm.Options{})
		},
	}
	report, err := eval.Run(context.Background(), []eval.Scenario{sc}, (&evaler{live: true}).runScenario, opts)
	if err != nil || report.Passed != 1 || report.Failed != 0 || len(report.Results) != 1 {
		reason := "<no result>"
		if len(report.Results) > 0 {
			reason = report.Results[0].Reason
		}
		t.Fatalf("eval.Run(%s): err=%v report=%+v reason=%q", p.name, err, report, reason)
	}
	return report.Results[0], rec
}

func liveLocalToolScenario(p liveLocalProtocol) eval.Scenario {
	turns := 2
	return eval.Scenario{
		Name:   "tool then answer (" + p.name + ")",
		Model:  "test-model",
		Prompt: "read note.txt and tell me what it says",
		Files:  map[string]string{"note.txt": liveLocalNote},
		Expect: eval.Expect{
			ToolCalls:    []string{"read"},
			TextContains: []string{"hello from seed"},
			Turns:        &turns,
		},
		Check: eval.Check{
			FileContains: map[string]string{"note.txt": "hello from seed"},
		},
	}
}

func liveLocalTextScenario(p liveLocalProtocol) eval.Scenario {
	turns := 1
	return eval.Scenario{
		Name:   "plain answer (" + p.name + ")",
		Model:  "test-model",
		Prompt: "say hello from seed",
		Expect: eval.Expect{
			ToolCalls:    []string{},
			TextContains: []string{"hello from seed"},
			Turns:        &turns,
		},
	}
}

// TestLiveLocalThreeProtocols 三协议表驱动：工具调用轮 + 最终文本轮全部通过 eval 评分，
// 覆盖 turns / tool_calls / text / file_contains 四类断言。
func TestLiveLocalThreeProtocols(t *testing.T) {
	for _, p := range liveLocalProtocols() {
		t.Run(p.name, func(t *testing.T) {
			// 工具场景：第 1 轮 read，第 2 轮文本收尾。
			res, _ := runLiveLocal(t, p, liveLocalToolScenario(p), p.toolScript, p.finalScript)
			if !reflect.DeepEqual(res.Tools, []string{"read"}) {
				t.Fatalf("tools = %v, want [read]", res.Tools)
			}
			if res.Turns != 2 {
				t.Fatalf("turns = %d, want 2", res.Turns)
			}
			if !strings.Contains(res.Text, "hello from seed") {
				t.Fatalf("text = %q", res.Text)
			}
			if res.Stopped {
				t.Fatalf("unexpected stopped result")
			}

			// 纯文本场景：1 轮即完成，无工具。
			res, _ = runLiveLocal(t, p, liveLocalTextScenario(p), p.finalScript)
			if res.Turns != 1 {
				t.Fatalf("text turns = %d, want 1", res.Turns)
			}
			if len(res.Tools) != 0 {
				t.Fatalf("text tools = %v, want none", res.Tools)
			}
			if !strings.Contains(res.Text, "hello from seed") {
				t.Fatalf("text = %q", res.Text)
			}
		})
	}
}

// TestLiveLocalNoKeyStillRuns 证明绕过 RunSuite 的 key 门禁后，环境里没有任何 key
// 也能用假服务端跑通评测（live 行为由 ChatterFactory 注入，而非 ARGENT_EVAL_LIVE）。
func TestLiveLocalNoKeyStillRuns(t *testing.T) {
	t.Setenv("ARGENT_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ARGENT_EVAL_LIVE", "")
	for _, p := range liveLocalProtocols() {
		t.Run(p.name, func(t *testing.T) {
			res, _ := runLiveLocal(t, p, liveLocalToolScenario(p), p.toolScript, p.finalScript)
			if !reflect.DeepEqual(res.Tools, []string{"read"}) || res.Turns != 2 {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

// TestLiveLocalSSEFidelity 断言真实客户端发出的请求形态：路径、header、Accept，
// 以及第二轮请求确实携带了工具结果（各协议 wire 结构不同）。
func TestLiveLocalSSEFidelity(t *testing.T) {
	for _, p := range liveLocalProtocols() {
		t.Run(p.name, func(t *testing.T) {
			_, rec := runLiveLocal(t, p, liveLocalToolScenario(p), p.toolScript, p.finalScript)
			reqs := rec.snapshot()
			if len(reqs) != 2 {
				t.Fatalf("requests = %d, want 2", len(reqs))
			}
			for _, req := range reqs {
				if req.path != p.path {
					t.Errorf("path = %q, want %q", req.path, p.path)
				}
				if got := req.header.Get("Accept"); got != "text/event-stream" {
					t.Errorf("Accept = %q", got)
				}
				if got := req.header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				if got := req.header.Get(p.authHeader); got != p.authValue {
					t.Errorf("%s = %q, want %q", p.authHeader, got, p.authValue)
				}
			}
			if !strings.Contains(reqs[0].body, "read note.txt") {
				t.Errorf("first request body lacks prompt: %q", reqs[0].body)
			}
			if !strings.Contains(reqs[0].body, `"name":"read"`) {
				t.Errorf("first request body lacks tool spec: %q", reqs[0].body)
			}
			if !strings.Contains(reqs[1].body, p.toolResultMarker) {
				t.Errorf("second request body lacks tool result marker %q: %q", p.toolResultMarker, reqs[1].body)
			}
			if !strings.Contains(reqs[1].body, "hello from seed") {
				t.Errorf("second request body lacks tool output: %q", reqs[1].body)
			}
		})
	}
}

// TestLiveLocalUsageFromSSE 验证 R18 的采集路径：runScenario 在 live 模式下安装 Emit，
// 从 SSE usage 帧累计 Trace.Usage；三协议都必须采集到非零 token 数。
func TestLiveLocalUsageFromSSE(t *testing.T) {
	for _, p := range liveLocalProtocols() {
		t.Run(p.name, func(t *testing.T) {
			srv, _ := newLiveLocalServer(t, p.finalScript)
			ws := eval.Workspace{
				Dir:     t.TempDir(),
				Prompt:  "say hello from seed",
				Model:   "test-model",
				Chatter: newChatter(p.name, srv.URL+liveLocalBaseURL, liveLocalKey, llm.Options{}),
			}
			sc := eval.Scenario{Name: "usage (" + p.name + ")", Prompt: ws.Prompt, Model: ws.Model}
			trace, err := (&evaler{live: true}).runScenario(context.Background(), sc, ws)
			if err != nil {
				t.Fatalf("runScenario: %v", err)
			}
			if trace.Usage.PromptTokens != p.usagePrompt || trace.Usage.CompletionTokens != 6 || trace.Usage.CachedTokens != 5 {
				t.Fatalf("usage = %+v, want prompt=%d completion=6 cached=5", trace.Usage, p.usagePrompt)
			}
		})
	}
}
