package agent

import (
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestMultimodalTrimEstimateAndByteIdentity(t *testing.T) {
	// 构造带图消息：Content 为空，ContentParts 是 OpenAI 风格 part 数组。
	msg := llm.BuildImageMessage("看这张图", []llm.ImageInput{
		{MimeType: "image/png", Data: make([]byte, 1024), Detail: "high"},
	})
	if len(msg.ContentParts) == 0 {
		t.Fatal("expected ContentParts to be set")
	}

	SetMultimodalTrim(false)
	baseline := EstimateMessage(msg)
	SetMultimodalTrim(true)
	boosted := EstimateMessage(msg)
	defer SetMultimodalTrim(false)

	if boosted <= baseline {
		t.Fatalf("multimodal estimate must exceed baseline: baseline=%d boosted=%d", baseline, boosted)
	}
	if boosted < imageTokensHigh {
		t.Fatalf("image part not accounted: boosted=%d", boosted)
	}
}

func TestEstimateContentPartsTextAndLowDetail(t *testing.T) {
	raw, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "hello world"},
		{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA", "detail": "low"}},
	})
	got := estimateContentParts(raw)
	if got < imageTokensLow+1 {
		t.Fatalf("text + low-detail image underestimated: %d", got)
	}
}

func TestEstimateContentPartsInvalidJSONFallsBack(t *testing.T) {
	if got := estimateContentParts(json.RawMessage(`not-json`)); got <= 0 {
		t.Fatalf("invalid ContentParts should still yield a positive estimate, got %d", got)
	}
}

func TestEstimateMessageTextOnlyUnchanged(t *testing.T) {
	msg := llm.Message{Role: "user", Content: "plain text"}
	SetMultimodalTrim(false)
	off := EstimateMessage(msg)
	SetMultimodalTrim(true)
	on := EstimateMessage(msg)
	defer SetMultimodalTrim(false)
	if off != on {
		t.Fatalf("text-only estimate changed with opt-in: off=%d on=%d", off, on)
	}
}

func TestTrimDoesNotSplitImageMessage(t *testing.T) {
	// 带图消息不是 tool 结果，Trim 只回收 tool 结果，因此不可裁剪原子项语义成立。
	SetMultimodalTrim(true)
	defer SetMultimodalTrim(false)
	image := llm.BuildImageMessage("图", []llm.ImageInput{{MimeType: "image/png", Data: make([]byte, 512)}})
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		image,
		{Role: "tool", ToolCallID: "x", Content: "some tool output"},
	}
	res := Trim(msgs, TrimOptions{MaxTokens: 1, KeepTurns: 0})
	if len(res.Messages) != len(msgs) {
		t.Fatalf("message count changed: %d", len(res.Messages))
	}
	if string(res.Messages[1].ContentParts) != string(image.ContentParts) {
		t.Fatal("image message ContentParts was modified")
	}
}
