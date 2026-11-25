package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestStrictStreamTruncationTriggersExactSuffixRecovery 锁定端到端链路：
// 线上客户端开启 StrictStreamTerminator 后，文本中途截断（无终止符）产生
// ErrStreamError，agent 的回补门据此触发 ChatOnce，且只发布精确后缀，
// 绝不重复失败尝试已经推送给 UI 的前缀。
func TestStrictStreamTruncationTriggersExactSuffixRecovery(t *testing.T) {
	const prefix = "Hello "
	const full = "Hello world"

	var (
		mu    sync.Mutex
		calls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		mu.Unlock()
		if strings.Contains(string(body), `"stream":false`) {
			// 非流式回补：返回单个 JSON，内容为完整的权威答复。
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"`+full+`"}}]}`)
			return
		}
		// 流式尝试：只发一个文本增量后直接关闭，不带 [DONE]。
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"`+prefix+`"}}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	client := llm.New(server.URL, "key")
	client.Options.StrictStreamTerminator = true

	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:                "m",
		EnableStreamRecovery: true,
		Chatter:              client,
		Emit:                 sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != full || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, want text %q and complete", res, full)
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("http requests = %d, want 2 (one stream + one ChatOnce)", gotCalls)
	}
	if got, want := sinkTextDeltas(sink), []string{prefix, "world"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text deltas = %q, want %q (only the exact suffix must follow the prefix)", got, want)
	}
	if got, want := sinkRetryReasons(sink), []string{streamRecoveryReason}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry reasons = %q, want %q", got, want)
	}
	if sink.has(EventPartial) {
		t.Fatal("successful strict recovery must not emit EventPartial")
	}
}
