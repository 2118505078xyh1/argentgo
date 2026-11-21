package llm

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

// captureTraceSink 是并发安全的测试收集器，把收到的追踪记录暂存于内存。
// TraceSink 的实现者必须自行保证 Record 可并发调用，这里用互斥锁满足该契约。
type captureTraceSink struct {
	mu      sync.Mutex
	records []RequestTrace
}

func (s *captureTraceSink) Record(rec RequestTrace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

// snapshot 返回已收到记录的副本。
func (s *captureTraceSink) snapshot() []RequestTrace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestTrace(nil), s.records...)
}

// resetTraceSink 在每个测试前后清除全局收集器，避免测试间互相污染。
// 这些测试共享包级注入状态，不能 t.Parallel。
func resetTraceSink(t *testing.T) {
	t.Helper()
	SetTraceSink(nil)
	t.Cleanup(func() { SetTraceSink(nil) })
}

// TestSetAndClearTraceSink 验证收集器可被安装与清除，默认状态为 nil。
func TestSetAndClearTraceSink(t *testing.T) {
	resetTraceSink(t)

	if got := currentTraceSink(); got != nil {
		t.Fatalf("初始状态应无收集器，got %#v", got)
	}

	sink := &captureTraceSink{}
	SetTraceSink(sink)
	if got := currentTraceSink(); got != sink {
		t.Fatalf("安装后读取不符：got %#v, want %#v", got, sink)
	}

	SetTraceSink(nil)
	if got := currentTraceSink(); got != nil {
		t.Fatalf("nil 应清除收集器，got %#v", got)
	}

	// 再次安装会替换先前的收集器。
	second := &captureTraceSink{}
	SetTraceSink(sink)
	SetTraceSink(second)
	if got := currentTraceSink(); got != second {
		t.Fatalf("再次安装应整体替换，got %#v, want %#v", got, second)
	}
}

// TestRecordTraceNilSinkNoop 验证未安装收集器时 RecordTrace 是安全 no-op。
func TestRecordTraceNilSinkNoop(t *testing.T) {
	resetTraceSink(t)

	// 不应 panic，也不应改变全局状态。
	RecordTrace(ProtocolChatCompletions, "model", 1, 2, 3, 4, "api_key=sk-should-not-leak")
	if got := currentTraceSink(); got != nil {
		t.Fatalf("no-op 不应安装收集器，got %#v", got)
	}
}

// TestRecordTraceFields 验证所有字段被原样写入追踪记录。
func TestRecordTraceFields(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	RecordTrace(ProtocolAnthropicMessages, "claude-3", 11, 22, 33, 1<<40, "ok")

	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("应收到 1 条记录，got %d", len(records))
	}
	want := RequestTrace{
		Protocol:         ProtocolAnthropicMessages,
		Model:            "claude-3",
		PromptTokens:     11,
		CompletionTokens: 22,
		CachedTokens:     33,
		LatencyMS:        1 << 40, // 验证 int64 大数不被截断
		Status:           "ok",
		Redacted:         true,
	}
	if got := records[0]; got != want {
		t.Fatalf("追踪字段不符\n got: %#v\nwant: %#v", got, want)
	}
}

// TestRecordTraceRedactsStatus 验证状态文本在写入前被脱敏，凭据不落入追踪。
func TestRecordTraceRedactsStatus(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	const secret = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	status := "request failed: api_key=" + secret
	RecordTrace(ProtocolChatCompletions, "model", 0, 0, 0, 5, status)

	records := sink.snapshot()
	if len(records) != 1 {
		t.Fatalf("应收到 1 条记录，got %d", len(records))
	}
	got := records[0].Status
	if strings.Contains(got, secret) {
		t.Fatalf("凭据不得落入追踪：Status=%q", got)
	}
	if !strings.Contains(got, "request failed") {
		t.Fatalf("普通错误措辞应保留：Status=%q", got)
	}
	if !records[0].Redacted {
		t.Fatal("写入前应完成脱敏，Redacted 应为 true")
	}

	// Authorization 风格与邮箱同样被脱敏。
	sink2 := &captureTraceSink{}
	SetTraceSink(sink2)
	RecordTrace(ProtocolResponses, "model", 0, 0, 0, 1, "Authorization: Bearer "+secret+" contact me@example.com")
	status2 := sink2.snapshot()[0].Status
	if strings.Contains(status2, secret) || strings.Contains(status2, "me@example.com") {
		t.Fatalf("Authorization 与邮箱应被脱敏：Status=%q", status2)
	}
}

// TestRecordTraceDoesNotModifyInputs 验证 RecordTrace 不改写调用方入参，
// 且收集器拿到的 Status 是与原串互不影响的新值。
func TestRecordTraceDoesNotModifyInputs(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	protocol, model := ProtocolChatCompletions, "model-x"
	status := "error api_key=sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	original := status
	RecordTrace(protocol, model, 1, 2, 3, 4, status)

	if protocol != ProtocolChatCompletions || model != "model-x" || status != original {
		t.Fatalf("入参不得被修改：protocol=%q model=%q status=%q", protocol, model, status)
	}
	rec := sink.snapshot()[0]
	if rec.Status == status {
		t.Fatal("收集器中的 Status 应为脱敏后的新值")
	}
	// 改动收集器里的副本，不影响调用方原串。
	rec.Status = "mutated"
	if status != original {
		t.Fatalf("改动记录副本不应影响入参：status=%q", status)
	}
}

// TestRecordTraceCJK 验证模型名与含 CJK 的状态文本被完整保留，不被截断。
func TestRecordTraceCJK(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	const model = "模型-Alpha"
	const status = "请求失败：网络超时（中文）"
	RecordTrace(ProtocolChatCompletions, model, 1, 0, 0, 9, status)

	rec := sink.snapshot()[0]
	if rec.Model != model || rec.Status != status {
		t.Fatalf("CJK 文本应完整保留：got %#v", rec)
	}
}

// TestRecordTraceDeterministic 验证相同入参产生完全相同的追踪记录。
func TestRecordTraceDeterministic(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	args := []any{ProtocolChatCompletions, "model", 10, 20, 5, int64(123), "status ok"}
	record := func() {
		RecordTrace(args[0].(string), args[1].(string), args[2].(int), args[3].(int),
			args[4].(int), args[5].(int64), args[6].(string))
	}
	record()
	record()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("应收到 2 条记录，got %d", len(records))
	}
	if !reflect.DeepEqual(records[0], records[1]) {
		t.Fatalf("相同入参应得到相同记录\n first: %#v\nsecond: %#v", records[0], records[1])
	}
}

// TestRecordTraceConcurrent 验证注入、读取与记录可并发调用而不损坏状态。
// 在 -race 下运行可捕获数据竞争。
func TestRecordTraceConcurrent(t *testing.T) {
	resetTraceSink(t)
	sink := &captureTraceSink{}
	SetTraceSink(sink)

	const goroutines = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			// 写入相同收集器，使预期结果不依赖 goroutine 执行顺序。
			SetTraceSink(sink)
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = currentTraceSink()
		}()
		go func() {
			defer wg.Done()
			<-start
			RecordTrace(ProtocolResponses, "model", 1, 2, 3, 4, "api_key=sk-ant-api03-concurrentsecret1234")
		}()
	}
	close(start)
	wg.Wait()

	records := sink.snapshot()
	if len(records) == 0 {
		t.Fatal("并发结束后应至少留下一条记录")
	}
	for _, rec := range records {
		if strings.Contains(rec.Status, "concurrentsecret1234") {
			t.Fatalf("并发记录同样不得泄露凭据：Status=%q", rec.Status)
		}
	}
	SetTraceSink(nil)
	if got := currentTraceSink(); got != nil {
		t.Fatalf("并发结束后应能清除收集器，got %#v", got)
	}
}

// TestRecordTraceSinkCallbackCanSetSink 验证收集器回调 SetTraceSink 时不死锁：
// RecordTrace 必须在锁外调用 Record。
func TestRecordTraceSinkCallbackCanSetSink(t *testing.T) {
	resetTraceSink(t)

	var inner captureTraceSink
	sink := &callbackTraceSink{callback: func(RequestTrace) { SetTraceSink(&inner) }}
	SetTraceSink(sink)

	done := make(chan struct{})
	go func() {
		defer close(done)
		RecordTrace(ProtocolChatCompletions, "model", 1, 1, 0, 1, "ok")
	}()
	<-done

	if got := currentTraceSink(); got != &inner {
		t.Fatalf("回调内安装的收集器应生效，got %#v", got)
	}
}

// callbackTraceSink 在每次 Record 时调用一次回调，用于验证锁外调用。
type callbackTraceSink struct {
	callback func(RequestTrace)
}

func (s *callbackTraceSink) Record(rec RequestTrace) { s.callback(rec) }
