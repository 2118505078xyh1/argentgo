package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// personaTaskJSON builds delegate arguments that request a named persona.
func personaTaskJSON(description, prompt, persona string) string {
	return `{"description":"` + description + `","prompt":"` + prompt + `","persona":"` + persona + `"}`
}

// personaSystem keeps the "You are a subagent" marker that isSubagent relies on
// while carrying a persona-specific body so the test can assert it was used.
const personaSystem = "You are a subagent reviewer. Report only review findings."

func TestTaskWithPersonaUsesResolvedSystemAndTools(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
	var childSystem string
	var childSpecs []string
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, specs []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childSystem = messages[0].Content
			if len(childSpecs) == 0 {
				for _, spec := range specs {
					childSpecs = append(childSpecs, spec.Function.Name)
				}
			}
			return textReply("review complete"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, personaTaskJSON("review", "review the code", "reviewer"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Subagent: &SubagentConfig{
			ResolvePersona: func(name string) (SubagentConfig, bool) {
				if name != "reviewer" {
					return SubagentConfig{}, false
				}
				return SubagentConfig{System: personaSystem, AllowedTools: []string{"read"}}, true
			},
		},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "review complete") {
		t.Fatalf("text = %q, want the child's answer", res.Text)
	}
	if childSystem != personaSystem {
		t.Fatalf("child system = %q, want the persona system %q", childSystem, personaSystem)
	}
	joined := strings.Join(childSpecs, ",")
	if !strings.Contains(joined, "read") {
		t.Fatalf("child tools = %v, want the persona's read tool", childSpecs)
	}
	if strings.Contains(joined, "write") {
		t.Fatalf("child tools = %v, write must be filtered out by the persona", childSpecs)
	}
}

func TestTaskWithoutPersonaUsesGenericConfig(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
	var childSystem string
	var childSpecs []string
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, specs []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childSystem = messages[0].Content
			if len(childSpecs) == 0 {
				for _, spec := range specs {
					childSpecs = append(childSpecs, spec.Function.Name)
				}
			}
			return textReply("generic answer"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("generic", "do the task"))), nil
	}}
	resolveCalled := false
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Subagent: &SubagentConfig{
			ResolvePersona: func(string) (SubagentConfig, bool) {
				resolveCalled = true
				return SubagentConfig{}, false
			},
		},
	}}

	if _, err := runner.Run(context.Background(), "delegate"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if resolveCalled {
		t.Fatal("resolver was called without a persona name")
	}
	if !strings.Contains(childSystem, "You are a subagent working for another agent") {
		t.Fatalf("child system = %q, want the generic subagent template", childSystem)
	}
	joined := strings.Join(childSpecs, ",")
	if !strings.Contains(joined, "write") {
		t.Fatalf("child tools = %v, want the parent's full tool set", childSpecs)
	}
}

func TestTaskWithPersonaButNoResolverUsesGenericConfig(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
	var childSystem string
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childSystem = messages[0].Content
			return textReply("no resolver answer"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, personaTaskJSON("review", "review the code", "reviewer"))), nil
	}}
	// No ResolvePersona: the persona name must be ignored without a panic.
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		Subagent: &SubagentConfig{},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "no resolver answer") {
		t.Fatalf("text = %q, want the child's answer", res.Text)
	}
	if !strings.Contains(childSystem, "You are a subagent working for another agent") {
		t.Fatalf("child system = %q, want the generic template when no resolver is set", childSystem)
	}
}

func TestTaskWithUnknownPersonaUsesGenericConfig(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	var childSystem string
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childSystem = messages[0].Content
			return textReply("unknown persona answer"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, personaTaskJSON("review", "review the code", "missing"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   tools,
		Subagent: &SubagentConfig{
			ResolvePersona: func(string) (SubagentConfig, bool) { return SubagentConfig{}, false },
		},
	}}

	if _, err := runner.Run(context.Background(), "delegate"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(childSystem, "You are a subagent working for another agent") {
		t.Fatalf("child system = %q, want the generic template for an unknown persona", childSystem)
	}
}

func TestConfigForPersonaSeedsThenOverlaysCallerFields(t *testing.T) {
	onEvent := func(Event) {}
	resolve := func(name string) (SubagentConfig, bool) {
		if name != "reviewer" {
			return SubagentConfig{}, false
		}
		return SubagentConfig{
			System:       "persona system",
			AllowedTools: []string{"read"},
			Model:        "persona-model",
		}, true
	}
	parent := &SubagentConfig{
		Model:          "caller-model",
		MaxDepth:       3,
		OnEvent:        onEvent,
		ResolvePersona: resolve,
	}
	tt := &taskTools{cfg: parent}

	got := tt.configForPersona("reviewer")
	if got.System != "persona system" {
		t.Errorf("System = %q, want the persona system", got.System)
	}
	if len(got.AllowedTools) != 1 || got.AllowedTools[0] != "read" {
		t.Errorf("AllowedTools = %v, want the persona's [read]", got.AllowedTools)
	}
	if got.Model != "caller-model" {
		t.Errorf("Model = %q, want the caller's explicit model to win", got.Model)
	}
	if got.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want the caller's 3", got.MaxDepth)
	}
	if got.OnEvent == nil {
		t.Error("OnEvent was dropped from the caller config")
	}
	if got.ResolvePersona == nil {
		t.Error("ResolvePersona was dropped, nested tasks could not select personas")
	}

	// An unknown name leaves the caller config untouched.
	unknown := tt.configForPersona("missing")
	if unknown.System != "" || unknown.Model != "caller-model" {
		t.Errorf("unknown persona changed the config: %#v", unknown)
	}
}

func TestTaskToolSpecIncludesPersonaField(t *testing.T) {
	raw := string(TaskToolSpec().Function.Parameters)
	if !strings.Contains(raw, `"persona"`) {
		t.Fatalf("schema is missing the persona field: %s", raw)
	}
	if !strings.Contains(raw, `"role"`) {
		t.Fatalf("schema is missing the role alias: %s", raw)
	}
}
