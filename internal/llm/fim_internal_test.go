package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestChatRequestStopTokenIDsSerialization pins the wire field name and the
// omitempty contract: Chat (nil stop ids) must not emit the field at all, while
// FillMiddle's ids appear as a top-level integer array.
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

	// An explicitly empty (non-nil) slice is also omitted by omitempty, so a
	// caller cannot accidentally send an empty stop list.
	empty, err := json.Marshal(chatRequest{Model: "m", Messages: base.Messages, StopTokenIDs: []int64{}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "stop_token_ids") {
		t.Fatalf("empty stop ids emitted a field: %s", empty)
	}
}
