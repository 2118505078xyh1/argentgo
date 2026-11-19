package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

func TestSteeringInjectedBeforeTheFirstTurn(t *testing.T) {
	steer := make(chan string, 2)
	steer <- "be brief"
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if !containsUser(messages, "be brief") {
			t.Fatal("queued steering was not injected before the first request")
		}
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Steer: steer}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestSteeringInjectedMidRun(t *testing.T) {
	steer := make(chan string, 4)
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			steer <- "focus on tests"
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		if !containsUser(messages, "focus on tests") {
			t.Fatal("steering was not injected between turns")
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, Steer: steer}}

	res, err := runner.Run(context.Background(), "review the code")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestSteeringAfterTheFinalAnswerStartsAnotherTurn(t *testing.T) {
	steer := make(chan string, 4)
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			steer <- "one more thing"
			return textReply("first answer"), nil
		}
		if !containsUser(messages, "one more thing") {
			t.Fatal("steering was not injected after the final answer")
		}
		return textReply("second answer"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Steer: steer}}

	res, err := runner.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "second answer" {
		t.Fatalf("text = %q, want the answer to the correction", res.Text)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
}

func TestClosedSteeringChannelIsHarmless(t *testing.T) {
	steer := make(chan string)
	close(steer)
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Steer: steer}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
}
