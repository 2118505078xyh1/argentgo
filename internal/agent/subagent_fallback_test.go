package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// A delegated child inherits the parent's fallback chain: when its primary
// model fails with a retryable error, the child retries the turn on the
// parent's backup instead of failing the task.
func TestSubagentInheritsFallbackChain(t *testing.T) {
	var chat *fakeChatter
	chat = &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		models := chat.modelsSeen()
		model := models[len(models)-1]
		if isSubagent(messages) {
			if model == "primary" {
				return llm.Message{}, errors.New("HTTP 429: too many requests")
			}
			return textReply("child answer from " + model), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find it"))), nil
	}}
	runner := &Runner{Opts: Options{
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Tools:          &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent:       &SubagentConfig{},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "child answer from backup") {
		t.Fatalf("text = %q, want the backup's child answer", res.Text)
	}
	if got := strings.Join(subagentModels(chat), ","); got != "primary,backup" {
		t.Fatalf("child models = %q, want primary,backup (child must inherit the parent's fallback chain)", got)
	}
}

// An explicit child model replaces only the primary entry of the inherited
// chain; the parent's backups are still available to the child.
func TestSubagentExplicitModelKeepsParentBackups(t *testing.T) {
	var chat *fakeChatter
	chat = &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		models := chat.modelsSeen()
		model := models[len(models)-1]
		if isSubagent(messages) {
			if model == "secondary" {
				return llm.Message{}, errors.New("HTTP 503: service unavailable")
			}
			return textReply("child answer from " + model), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find it"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:          "primary",
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
		Tools:          &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent:       &SubagentConfig{Model: "secondary"},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "child answer from backup") {
		t.Fatalf("text = %q, want the backup's child answer", res.Text)
	}
	if got := strings.Join(subagentModels(chat), ","); got != "secondary,backup" {
		t.Fatalf("child models = %q, want secondary,backup (explicit model must replace only the primary)", got)
	}
}

// subagentModels returns the models used by subagent chat calls, in order.
func subagentModels(chat *fakeChatter) []string {
	seen, models := chat.requestsSeen(), chat.modelsSeen()
	var out []string
	for i, messages := range seen {
		if isSubagent(messages) {
			out = append(out, models[i])
		}
	}
	return out
}
