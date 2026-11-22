package llm

import (
	"bytes"
	"context"
	"testing"
)

func TestRecordRequestSnapshotNoopByDefault(t *testing.T) {
	// 默认 ctx 没有收集器：调用必须是零副作用 no-op（不 panic、不改动 body）。
	body := []byte(`{"model":"x"}`)
	before := append([]byte(nil), body...)
	recordRequestSnapshot(context.Background(), ProtocolChatCompletions, "m", body)
	if !bytes.Equal(body, before) {
		t.Fatal("body was mutated by a no-op snapshot call")
	}
}

func TestWithRequestSnapshotSinkDeliversExactBytes(t *testing.T) {
	var gotProtocol, gotModel string
	var gotBody []byte
	called := false
	sink := func(protocol, model string, body []byte) error {
		called = true
		gotProtocol, gotModel = protocol, model
		gotBody = append([]byte(nil), body...) // 实现者必须自行拷贝
		return nil
	}
	ctx := WithRequestSnapshotSink(context.Background(), sink)

	// CJK 与转义字符确保逐字节比对而非字符串近似。
	body := []byte(`{"messages":[{"content":"中文 \"quoted\" 😀"}]}`)
	recordRequestSnapshot(ctx, ProtocolAnthropicMessages, "model-v2", body)

	if !called {
		t.Fatal("sink was not called")
	}
	if gotProtocol != ProtocolAnthropicMessages || gotModel != "model-v2" {
		t.Fatalf("protocol/model = %q/%q", gotProtocol, gotModel)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body mismatch:\ngot  %q\nwant %q", gotBody, body)
	}
}

func TestWithRequestSnapshotSinkNilIsDisabled(t *testing.T) {
	called := false
	ctx := WithRequestSnapshotSink(context.Background(), func(string, string, []byte) error {
		called = true
		return nil
	})
	ctx = WithRequestSnapshotSink(ctx, nil) // 清除
	recordRequestSnapshot(ctx, ProtocolResponses, "m", []byte(`{}`))
	if called {
		t.Fatal("nil sink must disable snapshots")
	}
}
