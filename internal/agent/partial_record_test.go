package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// partialRow 是测试用 Recorder 收集到的一行 (kind, payload)。
type partialRow struct {
	kind    string
	payload any
}

// collectPartialRows 用互斥锁收集写入的行，避免流式进度回调在其它 goroutine 写入时触发竞态。
func collectPartialRows() (*RecorderFunc, *[]partialRow, *sync.Mutex) {
	mu := &sync.Mutex{}
	rows := &[]partialRow{}
	rec := RecorderFunc(func(kind string, payload any) error {
		mu.Lock()
		defer mu.Unlock()
		*rows = append(*rows, partialRow{kind: kind, payload: payload})
		return nil
	})
	return &rec, rows, mu
}

// countPartialKind 统计指定类型的记录行数。
func countPartialKind(rows []partialRow, kind string) int {
	n := 0
	for _, row := range rows {
		if row.kind == kind {
			n++
		}
	}
	return n
}

// TestPartialRecordPayloadContent 验证快照的每个来源字段都落在稳定键名下，且 payload 没有多余键。
func TestPartialRecordPayloadContent(t *testing.T) {
	snap := partialSnapshot{
		Text:       "半截答案",
		StopReason: StopCancelled,
		Model:      "model-x",
		Turn:       4,
		Truncated:  true,
	}
	got := partialRecordPayload(snap)
	want := map[string]string{
		"text":        "半截答案",
		"stop_reason": StopCancelled,
		"model":       "model-x",
		"turn":        "4",
		"truncated":   "true",
	}
	if len(got) != len(want) {
		t.Fatalf("payload keys = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("payload[%q] = %q, want %q (full: %v)", key, got[key], value, got)
		}
	}
}

// TestPartialRecordPayloadCJK 验证 CJK 与 emoji 文本在辅助函数和 JSON 往返后都完整保留，
// 键名稳定且没有任何按字节切分。
func TestPartialRecordPayloadCJK(t *testing.T) {
	snap := partialSnapshot{
		Text:       "你好，世界🌏——尚未完成",
		StopReason: StopProviderError,
		Model:      "模型-甲",
		Turn:       7,
	}
	got := partialRecordPayload(snap)
	if got["text"] != snap.Text || !utf8.ValidString(got["text"]) {
		t.Fatalf("text = %q, want intact %q", got["text"], snap.Text)
	}
	if got["model"] != snap.Model {
		t.Fatalf("model = %q, want %q", got["model"], snap.Model)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]string
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if back["text"] != snap.Text {
		t.Fatalf("JSON round-trip text = %q, want %q", back["text"], snap.Text)
	}
	for _, key := range []string{"text", "stop_reason", "model", "turn", "truncated"} {
		if _, ok := back[key]; !ok {
			t.Fatalf("JSON payload missing stable key %q: %s", key, encoded)
		}
	}
}

// TestPartialRecordPayloadEmptyText 验证无文本的快照仍可表达：键都存在，文本值为空字符串。
func TestPartialRecordPayloadEmptyText(t *testing.T) {
	got := partialRecordPayload(partialSnapshot{StopReason: StopProviderError, Model: "m"})
	if value, ok := got["text"]; !ok || value != "" {
		t.Fatalf("empty snapshot text key = %q (present=%v), want empty string", value, ok)
	}
	if got["stop_reason"] != StopProviderError || got["model"] != "m" {
		t.Fatalf("empty snapshot lost provenance: %v", got)
	}
}

// TestPartialRecordPayloadTruncation 验证截断标记编码为稳定的字符串 "true"/"false"，
// 不依赖解码方对布尔值的处理。
func TestPartialRecordPayloadTruncation(t *testing.T) {
	for _, tc := range []struct {
		truncated bool
		want      string
	}{
		{false, "false"},
		{true, "true"},
	} {
		got := partialRecordPayload(partialSnapshot{Truncated: tc.truncated})["truncated"]
		if got != tc.want {
			t.Fatalf("truncated=%v encoded as %q, want %q", tc.truncated, got, tc.want)
		}
	}
}

// TestPartialRecordPayloadStopReason 验证 payload 原样携带运行将要上报的停止原因，
// 涵盖取消、供应商错误与自定义值。
func TestPartialRecordPayloadStopReason(t *testing.T) {
	for _, reason := range []string{StopCancelled, StopProviderError, "custom_reason", ""} {
		got := partialRecordPayload(partialSnapshot{StopReason: reason})["stop_reason"]
		if got != reason {
			t.Fatalf("stop reason = %q, want %q", got, reason)
		}
	}
}

// TestPartialRecordPersistenceDefaultOff 验证开关默认关闭时，失败运行仍发 EventPartial，
// 但不写入任何 "partial" 记录。
func TestPartialRecordPersistenceDefaultOff(t *testing.T) {
	SetPartialRecordPersistence(false)
	defer SetPartialRecordPersistence(false)

	rec, rows, mu := collectPartialRows()
	sink := &eventSink{}
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("半截")
		return llm.Message{}, errors.New("boom")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Emit: sink.emit, Record: *rec}}
	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected failure")
	}
	if len(collectPartials(sink)) != 1 {
		t.Fatal("the wire event must still be emitted with persistence off")
	}
	mu.Lock()
	defer mu.Unlock()
	if n := countPartialKind(*rows, partialRecordKind); n != 0 {
		t.Fatalf("default-off wrote %d partial record(s), want 0: %v", n, *rows)
	}
}

// TestPartialRecordPersistenceOptIn 验证完整接线：开关打开后，失败的尝试恰好写入一条
// "partial" 记录，其 payload 与事件及捕获快照一致，事件流本身保持不变。
func TestPartialRecordPersistenceOptIn(t *testing.T) {
	SetPartialRecordPersistence(true)
	defer SetPartialRecordPersistence(false)

	rec, rows, mu := collectPartialRows()
	sink := &eventSink{}
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("半截")
		onText("答案")
		return llm.Message{}, errors.New("provider failed")
	}}
	runner := &Runner{Opts: Options{
		Model: "model-x", Chatter: chat, Emit: sink.emit, Record: *rec,
	}}
	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected failure")
	}

	partials := collectPartials(sink)
	if len(partials) != 1 {
		t.Fatalf("partial events = %d, want 1: %+v", len(partials), partials)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := countPartialKind(*rows, partialRecordKind); n != 1 {
		t.Fatalf("partial records = %d, want 1: %v", n, *rows)
	}
	for _, row := range *rows {
		if row.kind != partialRecordKind {
			continue
		}
		payload, ok := row.payload.(map[string]string)
		if !ok {
			t.Fatalf("partial payload type = %T, want map[string]string", row.payload)
		}
		want := partialRecordPayload(partialSnapshot{
			Text: "半截答案", StopReason: StopProviderError, Model: "model-x", Turn: 0,
		})
		for key, value := range want {
			if payload[key] != value {
				t.Fatalf("persisted payload[%q] = %q, want %q (full: %v)", key, payload[key], value, payload)
			}
		}
		if strings.Join([]string{payload["text"], payload["stop_reason"]}, "|") != "半截答案|"+StopProviderError {
			t.Fatalf("persisted payload does not match the event: %v", payload)
		}
	}
}

// TestPartialRecordPersistenceOptInSuccessWritesNothing 验证开启开关不会给正常完成的操作
// 增加记录：只有失败/中断才会留下部分文本。
func TestPartialRecordPersistenceOptInSuccessWritesNothing(t *testing.T) {
	SetPartialRecordPersistence(true)
	defer SetPartialRecordPersistence(false)

	rec, rows, mu := collectPartialRows()
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("ok")
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Record: *rec}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := countPartialKind(*rows, partialRecordKind); n != 0 {
		t.Fatalf("clean run wrote %d partial record(s), want 0", n)
	}
}

// TestPartialRecordPersistenceNoRecorder 验证开关打开但未配置 Recorder 时依然安全：
// s.record 是空操作，运行照常结束。
func TestPartialRecordPersistenceNoRecorder(t *testing.T) {
	SetPartialRecordPersistence(true)
	defer SetPartialRecordPersistence(false)

	chat := &streamChatter{fn: func(int, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("boom")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat}}
	if _, err := runner.Run(context.Background(), "hi"); err == nil {
		t.Fatal("expected failure")
	}
}

// TestPartialRecordPersistenceConcurrentSwitch 在 -race 下检验原子开关：
// 在捕获发布的同时切换开关不得产生数据竞争或崩溃。
func TestPartialRecordPersistenceConcurrentSwitch(t *testing.T) {
	defer SetPartialRecordPersistence(false)
	rec, _, _ := collectPartialRows()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				SetPartialRecordPersistence(i%2 == 0)
				_ = partialRecordPersistenceEnabled()
				_ = partialRecordPayload(partialSnapshot{Text: "半"})
			}
		}(i)
	}
	// 还要驱动一次真实运行，使 emitPartial 并发读取该开关。
	chat := &streamChatter{fn: func(_ int, onText func(string)) (llm.Message, error) {
		onText("半")
		return llm.Message{}, errors.New("boom")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Record: *rec}}
	_, _ = runner.Run(context.Background(), "hi")
	wg.Wait()
}
