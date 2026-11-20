package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestToolNamesAreNormalizedBeforeDispatch covers a boundary the existing
// validation only checks halfway: validateTurn accepts a tool name with
// surrounding whitespace (it checks strings.TrimSpace(call.Name)), but the
// scripted providers forwarded the raw name to the tool runner. The real tool
// registry does an exact map lookup, so " read " became "unknown tool" at run
// time even though the document was accepted as valid. The canonical name is
// the trimmed one; both providers must dispatch that same value validation
// inspected.
func TestToolNamesAreNormalizedBeforeDispatch(t *testing.T) {
	t.Run("validation accepts padded names", func(t *testing.T) {
		turn := ScriptTurn{Respond: Respond{ToolCalls: []ToolCall{{
			Name: " ls ", Arguments: json.RawMessage(`{"path":"."}`),
		}}}}
		if err := (Scenario{Name: "case", Provider: []ScriptTurn{turn}}).Validate(); err != nil {
			t.Fatalf("Validate rejected a padded tool name: %v", err)
		}
	})

	t.Run("scripted provider dispatches the trimmed name", func(t *testing.T) {
		provider := NewScripted([]ScriptTurn{{
			Respond: Respond{ToolCalls: []ToolCall{{
				Name: "  read  ", Arguments: json.RawMessage(`{"path":"a.txt"}`),
			}}},
		}}, false)
		reply, err := provider.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if len(reply.ToolCalls) != 1 {
			t.Fatalf("tool calls = %+v", reply.ToolCalls)
		}
		if got := reply.ToolCalls[0].Function.Name; got != "read" {
			t.Fatalf("tool name = %q, want %q", got, "read")
		}
	})

	t.Run("orchestration provider dispatches the trimmed name", func(t *testing.T) {
		chatter := newOrchChatter("root", []OrchTurn{{
			Target:  orchTargetParent,
			Respond: Respond{ToolCalls: []ToolCall{{Name: "  task  "}}},
		}})
		messages := []llm.Message{
			{Role: "system", Content: "root"},
			{Role: "user", Content: "delegate"},
		}
		reply, err := chatter.Chat(context.Background(), "m", messages, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if len(reply.ToolCalls) != 1 {
			t.Fatalf("tool calls = %+v", reply.ToolCalls)
		}
		if got := reply.ToolCalls[0].Function.Name; got != "task" {
			t.Fatalf("tool name = %q, want %q", got, "task")
		}
	})

	// End to end: fakeTools only knows "ls", so an untrimmed name would come
	// back as an unknown tool and the expected tool sequence would not match.
	t.Run("a padded name still reaches the real tool through the runner", func(t *testing.T) {
		scenario := Scenario{
			Name:   "padded name",
			Prompt: "list",
			Provider: []ScriptTurn{
				{Respond: Respond{ToolCalls: []ToolCall{{
					Name: " ls ", Arguments: json.RawMessage(`{"path":"."}`),
				}}}},
				{Respond: Respond{Text: "listed"}},
			},
			Expect: Expect{
				ToolCalls:    []string{"ls"},
				TextContains: []string{"listed"},
				Turns:        intPtr(2),
			},
		}
		var out bytes.Buffer
		report, err := Run(context.Background(), []Scenario{scenario}, fakeRun, Options{Out: &out})
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, out.String())
		}
		if report.Passed != 1 {
			t.Fatalf("report = %+v\n%s", report, out.String())
		}
	})
}
