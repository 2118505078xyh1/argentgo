package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件验证三协议 readStream 中新增的工具增量 hook：
//  1. 逐字节不变：同一 SSE 流在带/不带 hook 时，Message 的 JSON 序列化完全相同；
//  2. 投递正确：hook 收到的原始分片序列逐项相等，且覆盖 CJK 跨片与多调用交错。
//
// 所有流都经 httptest 提供，chat / anthropic / responses 各一条固定脚本。
// 脚本刻意让两个调用交错出现，并把 "中文"/"你好" 拆到相邻分片里。

const (
	chatHookScript = `data: {"choices":[{"delta":{"content":"回答"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{\"路径\":\"中"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"write","arguments":"{\"内容\":\"你"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"文\",\"值\":\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"好\",\"数\":\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"你好\"}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"42\"}"}}]}}]}

data: [DONE]

`

	anthropicHookScript = `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"read","input":{}}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"write","input":{}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"路径\":\"中"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"内容\":\"你"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"文\",\"值\":\""}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"好\",\"数\":\""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"你好\"}"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"42\"}"}}

data: {"type":"message_stop"}

`

	responsesHookScript = `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"read","arguments":""}}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_b","name":"write","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"路径\":\"中"}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"内容\":\"你"}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"文\",\"值\":\""}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"好\",\"数\":\""}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"你好\"}"}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"42\"}"}

data: {"type":"response.completed","response":{"status":"completed"}}

`
)

// hookCases 把三协议的脚本、客户端构造与期望值组织在一起，两个测试共用，
// 保证"逐字节不变"和"投递正确"针对的是同一份流。
var hookCases = []struct {
	name          string
	script        string
	newClient     func(baseURL string) llm.Chatter
	wantDeltas    []llm.ToolCallDelta
	wantCollected []llm.ToolCallDelta
}{
	{
		name:   "chat-completions",
		script: chatHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.New(u, "test-key")
		},
		wantDeltas: []llm.ToolCallDelta{
			{Index: 0, ID: "call_a", Name: "read", Arguments: `{"路径":"中`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `{"内容":"你`},
			{Index: 0, Arguments: `文","值":"`},
			{Index: 1, Arguments: `好","数":"`},
			{Index: 0, Arguments: `你好"}`},
			{Index: 1, Arguments: `42"}`},
		},
		wantCollected: []llm.ToolCallDelta{
			{Index: 0, ID: "call_a", Name: "read", Arguments: `{"路径":"中文","值":"你好"}`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `{"内容":"你好","数":"42"}`},
		},
	},
	{
		name:   "anthropic-messages",
		script: anthropicHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.NewAnthropic(u, "test-key")
		},
		wantDeltas: []llm.ToolCallDelta{
			{Index: 0, ID: "toolu_a", Name: "read"},
			{Index: 1, ID: "toolu_b", Name: "write"},
			{Index: 0, ID: "toolu_a", Name: "read", Arguments: `{"路径":"中`},
			{Index: 1, ID: "toolu_b", Name: "write", Arguments: `{"内容":"你`},
			{Index: 0, ID: "toolu_a", Name: "read", Arguments: `文","值":"`},
			{Index: 1, ID: "toolu_b", Name: "write", Arguments: `好","数":"`},
			{Index: 0, ID: "toolu_a", Name: "read", Arguments: `你好"}`},
			{Index: 1, ID: "toolu_b", Name: "write", Arguments: `42"}`},
		},
		wantCollected: []llm.ToolCallDelta{
			{Index: 0, ID: "toolu_a", Name: "read", Arguments: `{"路径":"中文","值":"你好"}`},
			{Index: 1, ID: "toolu_b", Name: "write", Arguments: `{"内容":"你好","数":"42"}`},
		},
	},
	{
		name:   "responses",
		script: responsesHookScript,
		newClient: func(u string) llm.Chatter {
			return llm.NewResponses(u, "test-key")
		},
		wantDeltas: []llm.ToolCallDelta{
			{Index: 0, ID: "call_a", Name: "read", Arguments: ""},
			{Index: 1, ID: "call_b", Name: "write", Arguments: ""},
			{Index: 0, ID: "call_a", Name: "read", Arguments: `{"路径":"中`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `{"内容":"你`},
			{Index: 0, ID: "call_a", Name: "read", Arguments: `文","值":"`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `好","数":"`},
			{Index: 0, ID: "call_a", Name: "read", Arguments: `你好"}`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `42"}`},
		},
		wantCollected: []llm.ToolCallDelta{
			{Index: 0, ID: "call_a", Name: "read", Arguments: `{"路径":"中文","值":"你好"}`},
			{Index: 1, ID: "call_b", Name: "write", Arguments: `{"内容":"你好","数":"42"}`},
		},
	},
}

// runHookChat 把脚本挂到 httptest 上并调用 Chat；hook 为 nil 时 ctx 保持原样，
// 以此覆盖"未安装 hook"的真实路径。
func runHookChat(t *testing.T, script string, newClient func(string) llm.Chatter, hook llm.ToolDeltaHook) llm.Message {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	ctx := context.Background()
	if hook != nil {
		ctx = llm.WithToolDeltaHook(ctx, hook)
	}
	msg, err := newClient(server.URL).Chat(ctx, "test-model", nil, nil, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	return msg
}

// TestReadStreamHookByteIdentical 验证安装 hook 不会改变 Message 的任何字节：
// 同一脚本分别以"无 hook"和"有 hook"跑一遍，对 json.Marshal 结果做逐字节比较。
func TestReadStreamHookByteIdentical(t *testing.T) {
	for _, tc := range hookCases {
		t.Run(tc.name, func(t *testing.T) {
			without := runHookChat(t, tc.script, tc.newClient, nil)

			called := 0
			with := runHookChat(t, tc.script, tc.newClient, func(llm.ToolCallDelta) {
				called++
			})
			if called == 0 {
				t.Fatal("hook 未被调用，无法验证有 hook 路径")
			}

			a, err := json.Marshal(without)
			if err != nil {
				t.Fatalf("marshal without hook: %v", err)
			}
			b, err := json.Marshal(with)
			if err != nil {
				t.Fatalf("marshal with hook: %v", err)
			}
			if !bytes.Equal(a, b) {
				t.Fatalf("Message JSON 逐字节不一致\n无 hook: %s\n有 hook: %s", a, b)
			}
		})
	}
}

// TestReadStreamHookDeltaSequence 验证 hook 的投递序列逐项相等：
// 一个 recorder 记录原始分片，同时把每片转交给 StreamToolCollector，
// 既断言顺序与内容（含 CJK 跨片、多调用交错），也断言归并出的完整调用正确。
func TestReadStreamHookDeltaSequence(t *testing.T) {
	for _, tc := range hookCases {
		t.Run(tc.name, func(t *testing.T) {
			collector, collect := llm.NewStreamToolCollector()
			var raw []llm.ToolCallDelta
			msg := runHookChat(t, tc.script, tc.newClient, func(d llm.ToolCallDelta) {
				raw = append(raw, d)
				collect(d)
			})

			if !reflect.DeepEqual(raw, tc.wantDeltas) {
				t.Errorf("hook delta 序列不符\n got: %#v\nwant: %#v", raw, tc.wantDeltas)
			}
			if calls := collector.CompleteAll(); !reflect.DeepEqual(calls, tc.wantCollected) {
				t.Errorf("采集器归并结果不符\n got: %#v\nwant: %#v", calls, tc.wantCollected)
			}
			// 由采集结果派生 Message 侧期望，避免重复维护两份数据。
			wantToolCalls := make([]llm.ToolCall, 0, len(tc.wantCollected))
			for _, d := range tc.wantCollected {
				wantToolCalls = append(wantToolCalls, llm.ToolCall{
					ID: d.ID, Type: "function",
					Function: llm.ToolCallFunction{Name: d.Name, Arguments: d.Arguments},
				})
			}
			if !reflect.DeepEqual(msg.ToolCalls, wantToolCalls) {
				t.Errorf("Message.ToolCalls 不符\n got: %#v\nwant: %#v", msg.ToolCalls, wantToolCalls)
			}
		})
	}
}
