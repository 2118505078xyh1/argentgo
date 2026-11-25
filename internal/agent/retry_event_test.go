package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

// TestEventRetryCarriesFullInfo 检查 EventRetry 携带前端绘制重试进度所需的每个字段，且负载使用稳定的 snake_case 线上名称而非 Go 字段名。
// （接上行）
// （接上行）
func TestEventRetryCarriesFullInfo(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: EventRetry, Turn: 2, Retry: &RetryInfo{
		Attempt:     2,
		MaxAttempts: 4,
		DelayMS:     1500,
		Reason:      "status",
		Model:       "backup-model",
	}}
	if ev.Kind != EventRetry || ev.Retry == nil {
		t.Fatalf("event = %+v, want a retry event with a payload", ev)
	}
	if ev.IsTerminal() {
		t.Fatal("EventRetry must not be a run terminal")
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Kind  EventKind `json:"Kind"`
		Turn  int       `json:"Turn"`
		Retry struct {
			Attempt     int    `json:"attempt"`
			MaxAttempts int    `json:"max_attempts"`
			DelayMS     int64  `json:"delay_ms"`
			Reason      string `json:"reason"`
			Model       string `json:"model"`
		} `json:"retry"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Kind != EventRetry || wire.Turn != 2 {
		t.Fatalf("wire header = %+v, want retry/2", wire)
	}
	got := wire.Retry
	if got.Attempt != 2 || got.MaxAttempts != 4 || got.DelayMS != 1500 || got.Reason != "status" || got.Model != "backup-model" {
		t.Fatalf("wire retry payload = %+v, want attempt=2 max=4 delay=1500 reason=status model=backup-model", got)
	}
	// 往返必须保留负载，而不只是序列化它。
	var back Event
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Retry == nil || *back.Retry != *ev.Retry {
		t.Fatalf("round trip = %+v, want %+v", back.Retry, ev.Retry)
	}
}

// TestEventRetryNilKeepsLegacyJSON 证明该字段纯粹是附加的：nil 的 Retry 会被省略，因此现有事件保持逐字节不变的线上输出，而旧版没有 retry 键的 JSON 仍解码出 Retry == nil。
// （接上行）
// （接上行）
func TestEventRetryNilKeepsLegacyJSON(t *testing.T) {
	t.Parallel()
	ev := Event{Kind: EventError, Turn: 3, Text: "t", Err: "boom", Reason: StopProviderError}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "retry") || strings.Contains(string(data), "Retry") {
		t.Fatalf("nil Retry leaked into JSON: %s", data)
	}

	// Retry 出现之前的结构，所有字段顺序相同。字节相等是「nil 时不变」保证的最强形式。Failure 和 Retry 都是 nil+omitempty，因此都不出现在任一编码中。
	// （接上行）
	// （接上行）
	type retryLegacyEvent struct {
		Kind            EventKind
		Turn            int
		Depth           int
		Text            string
		ToolCallID      string
		ToolName        string
		Args            string
		Intent          string
		Output          string
		IsError         bool
		DurationMS      int64
		Usage           llm.Usage
		TotalUsage      llm.Usage
		Err             string
		Reason          string
		PendingMessages int
		Checkpoint      *Checkpoint
	}
	legacy := retryLegacyEvent{Kind: ev.Kind, Turn: ev.Turn, Text: ev.Text, Err: ev.Err, Reason: ev.Reason}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(legacyJSON) {
		t.Fatalf("nil-Retry JSON changed:\n got %s\nwant %s", data, legacyJSON)
	}

	var back Event
	if err := json.Unmarshal([]byte(`{"Kind":"notice","Text":"legacy"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Retry != nil {
		t.Fatalf("legacy decode set Retry: %+v", back.Retry)
	}
}

// TestFallbackSwitchEmitsEventRetry 检查 agent 可见的重试点：提供方切换会发布结构化的 EventRetry，带有可安全上线传输的原因且不含提供方文本。旧版通知保留，使旧消费者不变。
// （接上行）
// （接上行）
func TestFallbackSwitchEmitsEventRetry(t *testing.T) {
	t.Parallel()
	const secret = "sk-proj-SUPERSECRET123"
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return llm.Message{}, errors.New("HTTP 429: too many requests; key=" + secret)
		}
		return textReply("ok from backup"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:          "primary",
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Emit:           sink.emit,
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}

	var retry Event
	found := false
	for _, ev := range sink.events {
		if ev.Kind == EventRetry {
			retry = ev
			found = true
		}
	}
	if !found {
		t.Fatalf("no EventRetry emitted; kinds = %v", sink.kinds())
	}
	if retry.Retry == nil {
		t.Fatal("EventRetry has no Retry payload")
	}
	r := retry.Retry
	if r.Attempt != 2 || r.MaxAttempts != 2 || r.Model != "backup" || r.Reason != "status" {
		t.Fatalf("retry payload = %+v, want attempt=2 max=2 model=backup reason=status", r)
	}
	if r.DelayMS != 0 {
		t.Fatalf("DelayMS = %d, want 0 (the agent fallback switch has no backoff wait)", r.DelayMS)
	}
	if !sink.has(EventNotice) {
		t.Fatal("legacy switch notice was dropped")
	}
	data, _ := json.Marshal(retry)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "sk-") {
		t.Fatalf("retry event leaked provider text: %s", data)
	}
}

// TestEventRetryFieldsAreRedacted 在两个生产者处锁定安全承诺：retryReason 只发出封闭的标记，而 RetryInfoFromAttempt 只复制 Reason 类别——绝不复制可能携带密钥的 llm 自由格式 Cause。
// （接上行）
// （接上行）
func TestEventRetryFieldsAreRedacted(t *testing.T) {
	t.Parallel()
	const secret = "sk-proj-SUPERSECRET123"
	reasons := []struct {
		name string
		err  error
	}{
		{"status with key", errors.New("HTTP 429: too many requests; Authorization: Bearer " + secret)},
		{"server with key", errors.New("provider returned 503: backend said " + secret)},
		{"timeout with key", errors.New("dial tcp: i/o timeout using " + secret)},
		{"cancellation with key", errors.New("context canceled after sending " + secret)},
	}
	known := map[string]bool{
		string(llm.ReasonNone): true, string(llm.ReasonStatus): true,
		string(llm.ReasonNotSent): true, string(llm.ReasonUnreachable): true,
		string(llm.ReasonTimeout): true,
	}
	for _, tc := range reasons {
		t.Run(tc.name, func(t *testing.T) {
			got := retryReason(tc.err)
			if !known[got] {
				t.Fatalf("retryReason = %q, want a closed llm reason token", got)
			}
			if strings.Contains(got, secret) || strings.Contains(got, "sk-") {
				t.Fatalf("reason leaked a credential: %q", got)
			}
		})
	}

	// llm 桥接：Cause 持有已脱敏但仍是自由格式的文本。转换器必须完全丢弃它，只保留固定的原因类别。
	// （接上行）
	attempt := llm.RetryAttempt{Attempt: 2, StatusCode: 429, Cause: "provider said " + secret, Reason: llm.ReasonStatus}
	info := RetryInfoFromAttempt(attempt, "backup", 3, 750*time.Millisecond)
	if info.Attempt != 2 || info.MaxAttempts != 3 || info.DelayMS != 750 || info.Reason != "status" || info.Model != "backup" {
		t.Fatalf("converted payload = %+v, want attempt=2 max=3 delay=750 reason=status model=backup", info)
	}
	data, err := json.Marshal(Event{Kind: EventRetry, Retry: &info})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "Cause") {
		t.Fatalf("converted payload leaked Cause/credential: %s", data)
	}
}
