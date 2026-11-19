package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

type skillWrite struct {
	root        string
	name        string
	description string
	body        string
}

type fakeSkillWriter struct {
	mu    sync.Mutex
	calls []skillWrite
	path  string
	err   error
}

func (f *fakeSkillWriter) Author(root, name, description, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, skillWrite{root: root, name: name, description: description, body: body})
	if f.err != nil {
		return "", f.err
	}
	if f.path != "" {
		return f.path, nil
	}
	return "/skills/" + name + "/SKILL.md", nil
}

func (f *fakeSkillWriter) recorded() []skillWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]skillWrite(nil), f.calls...)
}

type fakeSkillLister struct {
	entries []SkillEntry
}

func (f *fakeSkillLister) ListSkills() []SkillEntry { return f.entries }

func hasTool(specs []llm.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Function.Name == name {
			return true
		}
	}
	return false
}

func toolNames(specs []llm.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	return names
}

func TestSaveSkillAdvertisedOnlyWhenConfigured(t *testing.T) {
	cases := []struct {
		name   string
		writer SkillWriter
		want   bool
	}{
		{name: "configured", writer: &fakeSkillWriter{}, want: true},
		{name: "default", writer: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var specs []llm.ToolSpec
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, tools []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					specs = append([]llm.ToolSpec(nil), tools...)
				}
				return textReply("done"), nil
			}}
			runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, SkillAuthor: tc.writer}}
			if _, err := runner.Run(context.Background(), "hi"); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := hasTool(specs, SaveSkillToolName); got != tc.want {
				t.Errorf("save_skill advertised = %v, specs = %v", got, toolNames(specs))
			}
		})
	}
}

func TestExistingRunsNeverAdvertiseSkillTools(t *testing.T) {
	var specs []llm.ToolSpec
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, tools []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			specs = append([]llm.ToolSpec(nil), tools...)
		}
		return textReply("done"), nil
	}}
	// Even with subagents enabled, the default run must be unchanged.
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent: &SubagentConfig{},
	}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{SaveSkillToolName, ListSkillsToolName} {
		if hasTool(specs, name) {
			t.Errorf("%s leaked into an existing run: %v", name, toolNames(specs))
		}
	}
}

func TestSaveSkillPassesArgsToWriterAndEmitsNotice(t *testing.T) {
	const path = `/work/.argent/skills/deploy-service/SKILL.md`
	writer := &fakeSkillWriter{path: path}
	sink := &eventSink{}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		switch n {
		case 1:
			return toolReply(toolCall("s1", SaveSkillToolName,
				`{"name":"deploy-service","description":"Ship it","steps":[" build ","test",""]}`)), nil
		case 2:
			if got := lastToolContent(messages); !strings.Contains(got, path) {
				t.Errorf("tool result = %q, want the saved path", got)
			}
			return textReply("done"), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Root: "/work", Chatter: chat,
		Tools: &fakeTools{}, SkillAuthor: writer, Emit: sink.emit,
	}}
	if _, err := runner.Run(context.Background(), "save it"); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls := writer.recorded()
	if len(calls) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(calls))
	}
	want := skillWrite{
		root:        "/work",
		name:        "deploy-service",
		description: "Ship it",
		body:        "1. build\n2. test\n",
	}
	if calls[0] != want {
		t.Errorf("writer call = %+v, want %+v", calls[0], want)
	}

	found := false
	for _, event := range sink.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, path) {
			found = true
		}
	}
	if !found {
		t.Errorf("no EventNotice with the saved path: %+v", sink.events)
	}
}

func TestSaveSkillWriterErrorBecomesToolResult(t *testing.T) {
	writer := &fakeSkillWriter{err: errors.New("disk full")}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("s1", SaveSkillToolName, `{"name":"x","description":"d","steps":["s"]}`)), nil
		}
		if got := lastToolContent(messages); !strings.Contains(got, "disk full") {
			t.Errorf("tool result = %q, want the writer error", got)
		}
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, SkillAuthor: writer}}
	if _, err := runner.Run(context.Background(), "save"); err != nil {
		t.Fatalf("tool failure should not abort the run: %v", err)
	}
}

func TestSaveSkillRejectsBadArguments(t *testing.T) {
	for _, args := range []string{
		`{`,
		`{}`,
		`{"name":"x","description":"d","steps":[]}`,
	} {
		writer := &fakeSkillWriter{}
		chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			if n == 1 {
				return toolReply(toolCall("s1", SaveSkillToolName, args)), nil
			}
			if got := lastToolContent(messages); !strings.HasPrefix(got, "error: save_skill:") {
				t.Errorf("args %q: tool result = %q", args, got)
			}
			return textReply("ok"), nil
		}}
		runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, SkillAuthor: writer}}
		if _, err := runner.Run(context.Background(), "save"); err != nil {
			t.Fatalf("args %q: %v", args, err)
		}
		if len(writer.recorded()) != 0 {
			t.Errorf("args %q: writer was called with invalid input", args)
		}
	}
}

func TestListSkillsAdvertisedAndHandledThroughLocalInterface(t *testing.T) {
	lister := &fakeSkillLister{entries: []SkillEntry{
		{Name: "deploy", Description: "ship it"},
		{Name: "review", Description: "review a diff"},
	}}
	var specs []llm.ToolSpec
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, tools []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			specs = append([]llm.ToolSpec(nil), tools...)
			return toolReply(toolCall("l1", ListSkillsToolName, `{}`)), nil
		}
		got := lastToolContent(messages)
		if !strings.Contains(got, "deploy: ship it") || !strings.Contains(got, "review: review a diff") {
			t.Errorf("list result = %q", got)
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, SkillLister: lister}}
	if _, err := runner.Run(context.Background(), "list"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasTool(specs, ListSkillsToolName) {
		t.Errorf("list_skills not advertised: %v", toolNames(specs))
	}
	if hasTool(specs, SaveSkillToolName) {
		t.Errorf("save_skill advertised without a writer: %v", toolNames(specs))
	}
}

func TestSkillToolsComposeWithSubagent(t *testing.T) {
	writer := &fakeSkillWriter{}
	var specs []llm.ToolSpec
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, tools []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			specs = append([]llm.ToolSpec(nil), tools...)
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{},
		SkillAuthor: writer, Subagent: &SubagentConfig{},
	}}
	if _, err := runner.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	names := strings.Join(toolNames(specs), ",")
	if !strings.Contains(names, SaveSkillToolName) || !strings.Contains(names, TaskToolName) {
		t.Errorf("specs = %q, want save_skill and task", names)
	}
}
