package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestStderrTraceSinkWritesValidSingleJSONLine 验证一条记录被写为合法、
// 独立的单行 JSON，且字段可无损往返。
func TestStderrTraceSinkWritesValidSingleJSONLine(t *testing.T) {
	var buf bytes.Buffer
	sink := newStderrTraceSink(&buf)
	rec := llm.RequestTrace{
		Protocol:         llm.ProtocolChatCompletions,
		Model:            "model-x",
		PromptTokens:     1,
		CompletionTokens: 2,
		CachedTokens:     3,
		LatencyMS:        4,
		Status:           "ok",
		Redacted:         true,
	}
	sink.Record(rec)

	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("每条记录应恰好占一行并以换行结束，got %q", out)
	}
	var got llm.RequestTrace
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("输出应为合法 JSON：%v，内容 %q", err, out)
	}
	if got != rec {
		t.Fatalf("JSON 往返字段不符\n got: %#v\nwant: %#v", got, rec)
	}
}

// TestStderrTraceSinkCJK 验证模型名与状态中的中文被完整保留、不被截断成
// 半个 UTF-8 字符。
func TestStderrTraceSinkCJK(t *testing.T) {
	var buf bytes.Buffer
	sink := newStderrTraceSink(&buf)
	rec := llm.RequestTrace{Model: "模型-Alpha", Status: "请求失败：网络超时（中文）"}
	sink.Record(rec)

	out := buf.String()
	if !utf8.ValidString(out) {
		t.Fatalf("输出必须是合法 UTF-8，got %q", out)
	}
	if !strings.Contains(out, rec.Model) || !strings.Contains(out, rec.Status) {
		t.Fatalf("CJK 文本应完整保留，got %q", out)
	}
	var got llm.RequestTrace
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Model != rec.Model || got.Status != rec.Status {
		t.Fatalf("CJK 往返失败：err=%v got=%#v", err, got)
	}
}

// TestStderrTraceSinkNilWriter 验证 nil writer 是安全 no-op，不 panic。
func TestStderrTraceSinkNilWriter(t *testing.T) {
	sink := newStderrTraceSink(nil)
	sink.Record(llm.RequestTrace{Model: "m", Status: "ok"})
}

// countingWriter 统计 Write 调用次数，并缓存写入内容。
type countingWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	count int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	return w.buf.Write(p)
}

// TestStderrTraceSinkConcurrentLinesIntact 验证并发记录时每条记录都独占一次
// Write、独占一行，不会出现半行或半个字符。
func TestStderrTraceSinkConcurrentLinesIntact(t *testing.T) {
	w := &countingWriter{}
	sink := newStderrTraceSink(w)

	const goroutines = 32
	const perGoroutine = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for i := 0; i < perGoroutine; i++ {
				sink.Record(llm.RequestTrace{
					Protocol: llm.ProtocolResponses,
					Model:    "模型-" + string(rune('A'+id%26)),
					Status:   "ok",
					Redacted: true,
				})
			}
		}(g)
	}
	close(start)
	wg.Wait()

	out := w.buf.String()
	if !utf8.ValidString(out) {
		t.Fatal("并发输出必须是合法 UTF-8")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := goroutines * perGoroutine
	if len(lines) != want {
		t.Fatalf("应得到 %d 行，got %d", want, len(lines))
	}
	for _, line := range lines {
		var rec llm.RequestTrace
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("出现半行或非法 JSON：%v，行 %q", err, line)
		}
	}

	w.mu.Lock()
	writes := w.count
	w.mu.Unlock()
	if writes != want {
		t.Fatalf("每条记录应恰好一次 Write，got %d want %d", writes, want)
	}
}
