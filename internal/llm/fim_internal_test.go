package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestChatRequestStopTokenIDsSerialization 固定线协议字段名和
// omitempty 约定：Chat（停止 id 为 nil）必须完全不发出该字段，而
// FillMiddle 的 id 作为顶层整数数组出现。
func TestChatRequestStopTokenIDsSerialization(t *testing.T) {
	base := chatRequest{Model: "m", Messages: []chatMessage{{Role: "user", Content: "hi"}}, Stream: true}

	without, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "stop_token_ids") {
		t.Fatalf("nil stop ids emitted a field: %s", without)
	}

	with, err := json.Marshal(chatRequest{
		Model:        "m",
		Messages:     base.Messages,
		StopTokenIDs: []int64{151643, 151645},
		Stream:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"stop_token_ids":[151643,151645]`) {
		t.Fatalf("stop ids not encoded as a top-level integer array: %s", with)
	}

	// 显式为空（非 nil）的切片也会被 omitempty 省略，因此
	// 调用方不会意外发送空的停止列表。
	empty, err := json.Marshal(chatRequest{Model: "m", Messages: base.Messages, StopTokenIDs: []int64{}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "stop_token_ids") {
		t.Fatalf("empty stop ids emitted a field: %s", empty)
	}
}
