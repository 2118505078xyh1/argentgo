package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestAssistantRecordCarriesReasoningSignature 锁定推理签名在 agent 循环中的
// 回放链：提供方回复携带签名时，写入的助手记录与返回给调用方的转录视图
// 都必须保留它，使后续轮次能带着签名回放思考块。
func TestAssistantRecordCarriesReasoningSignature(t *testing.T) {
	const (
		reasoning = "逐步思考：先读文件，再修改。"
		signature = "sig-loop"
	)
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done", Reasoning: reasoning, ReasoningSignature: signature}, nil
	}}
	type row struct {
		kind    string
		payload any
	}
	var rows []row
	rec := RecorderFunc(func(kind string, payload any) error {
		rows = append(rows, row{kind: kind, payload: payload})
		return nil
	})
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Record: rec}}
	result, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	recordHasSignature := false
	for _, r := range rows {
		if r.kind != "assistant" {
			continue
		}
		msg, ok := r.payload.(llm.Message)
		if !ok {
			t.Fatalf("assistant payload = %T, want llm.Message", r.payload)
		}
		if msg.ReasoningSignature == signature {
			recordHasSignature = true
		}
	}
	if !recordHasSignature {
		t.Fatalf("no assistant record carried signature %q; rows = %+v", signature, rows)
	}

	transcriptHasSignature := false
	for _, m := range result.Messages {
		if m.Role == "assistant" && m.ReasoningSignature == signature {
			transcriptHasSignature = true
		}
	}
	if !transcriptHasSignature {
		t.Fatalf("result transcript lost signature: %+v", result.Messages)
	}
}

// TestFinalAnswerCarriesReasoningSignature 验证预算耗尽后的最终答案请求
// 若携带签名，合成的助手轮次也会保留它，供后续轮次回放。
func TestFinalAnswerCarriesReasoningSignature(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	const signature = "sig-final"
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return llm.Message{Role: "assistant", Content: "final answer", Reasoning: "思考", ReasoningSignature: signature}, nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 1}}
	res, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != "assistant" || last.ReasoningSignature != signature {
		t.Fatalf("last transcript entry = %+v, want signature %q", last, signature)
	}
}
