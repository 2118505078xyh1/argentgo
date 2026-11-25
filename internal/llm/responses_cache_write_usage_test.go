package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestResponsesUsageReportsCacheWriteTokens 覆盖 Responses 协议在用量中
// 报告缓存写入量的场景。Usage.CacheWriteTokens 会被下游的成本核算和
// 缓存层级推断读取；只解析 cached_tokens 会让缓存写入量悄悄丢失。
// 网关既可能把它放在 input_tokens_details 内，也可能放在 usage 顶层，
// 两种形态都必须识别（嵌套值优先）。
func TestResponsesUsageReportsCacheWriteTokens(t *testing.T) {
	cases := []struct {
		name       string
		usage      string
		wantCached int
		wantWrite  int
	}{
		{
			name:       "nested cache write tokens alongside cached tokens",
			usage:      `"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":50,"cache_write_tokens":20}`,
			wantCached: 50,
			wantWrite:  20,
		},
		{
			name:       "top level cache write tokens",
			usage:      `"input_tokens":100,"output_tokens":7,"cache_write_tokens":12`,
			wantCached: 0,
			wantWrite:  12,
		},
		{
			name:       "nested value wins over top level",
			usage:      `"input_tokens":100,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":9},"cache_write_tokens":4`,
			wantCached: 30,
			wantWrite:  9,
		},
		{
			name:       "cached tokens only keeps zero write count",
			usage:      `"input_tokens":100,"input_tokens_details":{"cached_tokens":30}`,
			wantCached: 30,
			wantWrite:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"usage":{` + tc.usage + `}}}` + "\n\n"

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, script)
			}))
			defer server.Close()

			reply, err := llm.NewResponses(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Usage == nil {
				t.Fatal("no usage reported")
			}
			if reply.Usage.CachedTokens != tc.wantCached {
				t.Errorf("CachedTokens = %d, want %d", reply.Usage.CachedTokens, tc.wantCached)
			}
			if reply.Usage.CacheWriteTokens != tc.wantWrite {
				t.Errorf("CacheWriteTokens = %d, want %d", reply.Usage.CacheWriteTokens, tc.wantWrite)
			}
		})
	}
}
