package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestFallbackSwitchesOn429ThenSucceeds(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return llm.Message{}, errors.New("HTTP 429: too many requests")
		}
		return textReply("ok from backup"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:          "legacy-primary",
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Emit:           sink.emit,
	}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "ok from backup" || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, want the backup's answer", res)
	}
	if got := strings.Join(chat.modelsSeen(), ","); got != "primary,backup" {
		t.Fatalf("models = %q, want primary,backup (FallbackModels[0] is primary)", got)
	}
	var notice string
	for _, event := range sink.events {
		if event.Kind == EventNotice {
			notice = event.Text
		}
	}
	if !strings.Contains(notice, "primary") || !strings.Contains(notice, "backup") {
		t.Fatalf("switch notice = %q, want both model names", notice)
	}
}

func TestFallbackAllModelsFailListsTriedModels(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("HTTP 503: service unavailable")
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"first", "second", "third"},
		Chatter:        chat,
	}}

	_, err := runner.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("all models failed but Run returned nil error")
	}
	for _, model := range []string{"first", "second", "third"} {
		if !strings.Contains(err.Error(), model) {
			t.Fatalf("error %q does not mention tried model %q", err, model)
		}
	}
	if chat.count() != 3 {
		t.Fatalf("requests = %d, want 3 attempts", chat.count())
	}
}

func TestFallbackDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n != 1 {
			t.Fatal("chatter called again after cancellation")
		}
		cancel()
		return llm.Message{}, errors.New("HTTP 429: too many requests")
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"a", "b"},
		Chatter:        chat,
	}}

	_, err := runner.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if chat.count() != 1 {
		t.Fatalf("requests = %d, want 1 (cancellation must not retry)", chat.count())
	}
}

func TestFallbackDoesNotRetryValidationErrors(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return llm.Message{}, errors.New("400 invalid request: unknown role")
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"a", "b"},
		Chatter:        chat,
	}}

	_, err := runner.Run(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "invalid request") {
		t.Fatalf("err = %v, want the validation error", err)
	}
	if chat.count() != 1 {
		t.Fatalf("requests = %d, want 1 (validation must not retry)", chat.count())
	}
}

func TestSubagentUsesSecondaryModel(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			return textReply("child answer"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find it"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "primary",
		Chatter:  chat,
		Tools:    tools,
		Subagent: &SubagentConfig{Model: "secondary"},
	}}

	if _, err := runner.Run(context.Background(), "delegate"); err != nil {
		t.Fatalf("run: %v", err)
	}
	seen := chat.requestsSeen()
	models := chat.modelsSeen()
	childCalls, rootCalls := 0, 0
	for i, model := range models {
		if isSubagent(seen[i]) {
			if model != "secondary" {
				t.Fatalf("child model = %q, want secondary", model)
			}
			childCalls++
			continue
		}
		if model != "primary" {
			t.Fatalf("parent model = %q, want primary (must not change)", model)
		}
		rootCalls++
	}
	if childCalls == 0 || rootCalls == 0 {
		t.Fatalf("root calls = %d, child calls = %d, want both", rootCalls, childCalls)
	}
}

func TestFallbackKeepsHistoryWithoutDuplicates(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		switch n {
		case 1:
			return toolReply(toolCall("c1", "read", `{"path":"a.txt"}`)), nil
		case 2:
			return llm.Message{}, errors.New("HTTP 500: internal server error")
		default:
			return textReply("done"), nil
		}
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Tools:          tools,
	}}

	res, err := runner.Run(context.Background(), "read a.txt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	users, assistants, toolResults := 0, 0, 0
	for _, msg := range res.Messages {
		switch msg.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		case "tool":
			toolResults++
		}
	}
	if users != 1 || toolResults != 1 || assistants != 2 {
		t.Fatalf("roles = %d user / %d assistant / %d tool, want 1 / 2 / 1: %+v",
			users, assistants, toolResults, res.Messages)
	}
	if tools.count() != 1 {
		t.Fatalf("tool ran %d times, want 1", tools.count())
	}
	seen := chat.requestsSeen()
	if len(seen) != 3 {
		t.Fatalf("requests = %d, want 3", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if got := lastToolContent(seen[i]); got != "read ok" {
			t.Fatalf("request %d saw tool result %q, want the original result", i+1, got)
		}
	}
	if countUser(res.Messages, "read a.txt") != 1 {
		t.Fatal("final history must contain exactly one copy of the user prompt")
	}
}

// countUser counts user messages with exactly the given text.
func countUser(messages []llm.Message, text string) int {
	n := 0
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content == text {
			n++
		}
	}
	return n
}
