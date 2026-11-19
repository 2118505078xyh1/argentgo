package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ---------------------------------------------------------------- fixtures

// fakeTools is a tiny tool table for the fake loop: just enough behaviour to
// exercise the scenarios, with the real gates (policy, approval, uniqueness).
type fakeTools struct {
	dir     string
	approve bool
	deny    []string
	asked   []string
}

type toolArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
	Pattern string `json:"pattern"`
}

func (t *fakeTools) denied(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	for _, deny := range t.deny {
		if clean == filepath.Clean(filepath.FromSlash(deny)) {
			return true
		}
	}
	return false
}

func (t *fakeTools) path(rel string) string {
	return filepath.Join(t.dir, filepath.FromSlash(rel))
}

func (t *fakeTools) run(name, arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	var args toolArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "error: bad arguments: " + err.Error()
	}

	switch name {
	case "read":
		if t.denied(args.Path) {
			return fmt.Sprintf("error: the path %q is denied by policy", args.Path)
		}
		data, err := os.ReadFile(t.path(args.Path))
		if err != nil {
			return fmt.Sprintf("error: cannot read %q", args.Path)
		}
		return string(data)

	case "write", "edit":
		if t.denied(args.Path) {
			return fmt.Sprintf("error: the path %q is denied by policy", args.Path)
		}
		// Mutating tools always pass the approval gate first.
		t.asked = append(t.asked, name+":"+args.Path)
		if !t.approve {
			return fmt.Sprintf("error: the user denied the %s to %q", name, args.Path)
		}
		if name == "write" {
			target := t.path(args.Path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "error: " + err.Error()
			}
			if err := os.WriteFile(target, []byte(args.Content), 0o644); err != nil {
				return "error: " + err.Error()
			}
			return "wrote " + args.Path
		}
		data, err := os.ReadFile(t.path(args.Path))
		if err != nil {
			return fmt.Sprintf("error: cannot read %q", args.Path)
		}
		content := string(data)
		if args.OldText == "" {
			return "error: old_text is empty"
		}
		switch count := strings.Count(content, args.OldText); {
		case count == 0:
			return fmt.Sprintf("error: old_text was not found in %q", args.Path)
		case count > 1:
			return fmt.Sprintf("error: old_text appears %d times in %q", count, args.Path)
		default:
			updated := strings.Replace(content, args.OldText, args.NewText, 1)
			if err := os.WriteFile(t.path(args.Path), []byte(updated), 0o644); err != nil {
				return "error: " + err.Error()
			}
			return "edited " + args.Path
		}

	case "grep":
		var hits []string
		_ = filepath.WalkDir(t.dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			relative, err := filepath.Rel(t.dir, path)
			if err != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			for i, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, args.Pattern) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", relative, i+1, line))
				}
			}
			return nil
		})
		sort.Strings(hits)
		if len(hits) == 0 {
			return "no matches for " + args.Pattern
		}
		return strings.Join(hits, "\n")

	case "ls":
		entries, err := os.ReadDir(t.path(args.Path))
		if err != nil {
			return "error: cannot list " + args.Path
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) == 0 {
			return "(empty)"
		}
		return strings.Join(names, "\n")
	}
	return fmt.Sprintf("error: unknown tool %q", name)
}

// fakeRun is a hand-written stand-in for the real agent loop: it asks the
// scripted provider for a turn, runs the requested tools, and reports what
// happened. It honours the scenario hints (turn budget, cancellation, approval,
// denied paths) so the scenarios keep their Rust intent without importing the
// agent package.
func fakeRun(ctx context.Context, scenario Scenario, workspace Workspace) (Trace, error) {
	approve := true
	if scenario.Approve != nil {
		approve = *scenario.Approve
	}
	tools := &fakeTools{dir: workspace.Dir, approve: approve, deny: scenario.DenyPaths}

	maxTurns := scenario.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}
	runCtx := ctx
	cancel := context.CancelFunc(func() {})
	if scenario.CancelAfter > 0 {
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	var trace Trace
	messages := []llm.Message{
		{Role: "system", Content: workspace.System},
		{Role: "user", Content: workspace.Prompt},
	}
	if scenario.PromptCache && !scenario.NoPromptCache && messages[0].Content != "" {
		// Mimic a cache-capable client's stable-prefix assembly so the
		// scripted provider can assert opt-in and opt-out wire behaviour.
		messages[0].CacheControl = llm.EphemeralCacheControl()
	}
	for turn := 1; turn <= maxTurns; turn++ {
		if scenario.CancelAfter > 0 && turn > scenario.CancelAfter {
			cancel()
			return trace, runCtx.Err()
		}
		reply, err := workspace.Chatter.Chat(runCtx, workspace.Model, messages, nil, nil)
		if err != nil {
			return trace, err
		}
		messages = append(messages, llm.Message{Role: reply.Role, Content: reply.Content, ToolCalls: reply.ToolCalls})
		trace.Text = reply.Content
		if reply.Usage != nil {
			trace.Usage.PromptTokens += reply.Usage.PromptTokens
			trace.Usage.CompletionTokens += reply.Usage.CompletionTokens
			trace.Usage.CachedTokens += reply.Usage.CachedTokens
			trace.Usage.CacheWriteTokens += reply.Usage.CacheWriteTokens
		}
		if len(reply.ToolCalls) == 0 {
			return trace, nil
		}
		for _, call := range reply.ToolCalls {
			trace.Tools = append(trace.Tools, call.Function.Name)
			output := tools.run(call.Function.Name, call.Function.Arguments)
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: output})
		}
	}
	trace.Stopped = true
	return trace, nil
}

// ------------------------------------------------------------ suite tests

func loadCore(t *testing.T) []Scenario {
	t.Helper()
	scenarios, err := Load(filepath.Join("..", "..", "evals", "core.json"))
	if err != nil {
		t.Fatalf("Load(evals/core.json): %v", err)
	}
	return scenarios
}

func TestCoreSuitePassesThroughTheFakeLoop(t *testing.T) {
	scenarios := loadCore(t)
	if len(scenarios) != 10 {
		t.Fatalf("core.json holds %d scenarios, want 10", len(scenarios))
	}

	var out bytes.Buffer
	report, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out, Verbose: true})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if report.Passed != 10 || report.Failed != 0 {
		t.Fatalf("report = %d passed, %d failed\n%s", report.Passed, report.Failed, out.String())
	}

	text := out.String()
	for _, want := range []string{"1..10", "ok 1 - answers a simple question", "ok 8 - the turn limit ends a run that never stops"} {
		if !strings.Contains(text, want) {
			t.Errorf("report misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "not ok") {
		t.Errorf("report holds a failure:\n%s", text)
	}
	if !strings.Contains(text, "#   tools=[read] turns=2 stopped=false") {
		t.Errorf("verbose diagnostics missing:\n%s", text)
	}
}

func TestCacheSuitePassesThroughTheFakeLoop(t *testing.T) {
	scenarios, err := Load(filepath.Join("..", "..", "evals", "cache.json"))
	if err != nil {
		t.Fatalf("Load(evals/cache.json): %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("cache.json holds %d scenarios, want 2", len(scenarios))
	}

	var out bytes.Buffer
	report, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("report = %d passed, %d failed\n%s", report.Passed, report.Failed, out.String())
	}
	for _, want := range []string{"ok 1 - reports cached prompt usage", "ok 2 - prompt cache opt-out leaves the request cache-free"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report misses %q:\n%s", want, out.String())
		}
	}
}

func TestRunReportsAWrongCachedTokenCount(t *testing.T) {
	scenario := Scenario{
		Name:     "cached usage",
		Prompt:   "answer",
		Provider: []ScriptTurn{{Respond: Respond{Text: "ok", Usage: &llm.Usage{PromptTokens: 20, CachedTokens: 5}}}},
		Expect:   Expect{CachedTokens: intPtr(7)},
	}
	var out bytes.Buffer
	report, err := Run(context.Background(), []Scenario{scenario}, fakeRun, Options{Out: &out})
	if err == nil {
		t.Fatal("a wrong cached-token count passed")
	}
	if !strings.Contains(report.Results[0].Reason, "expected 7 cached tokens, the model reported 5") {
		t.Fatalf("reason = %q", report.Results[0].Reason)
	}
}

func TestRunReportsAFailureAndKeepsGoing(t *testing.T) {
	scenarios := []Scenario{
		{
			Name:     "good",
			Prompt:   "answer",
			Provider: []ScriptTurn{{Respond: Respond{Text: "fine"}}},
			Expect:   Expect{TextContains: []string{"fine"}},
		},
		{
			Name:     "bad",
			Prompt:   "answer",
			Provider: []ScriptTurn{{Respond: Respond{Text: "fine"}}},
			Expect:   Expect{TextContains: []string{"something else"}},
		},
		{
			Name:     "still runs",
			Prompt:   "answer",
			Provider: []ScriptTurn{{Respond: Respond{Text: "fine"}}},
			Expect:   Expect{Turns: intPtr(1)},
		},
	}
	var out bytes.Buffer
	report, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out})
	if err == nil {
		t.Fatal("Run returned no error for a failing suite")
	}
	if report.Passed != 2 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed, want 2/1", report.Passed, report.Failed)
	}
	text := out.String()
	if !strings.Contains(text, "not ok 2 - bad: the answer does not contain \"something else\"") {
		t.Errorf("failure line missing:\n%s", text)
	}
	if !strings.Contains(text, "ok 3 - still runs") {
		t.Errorf("the suite stopped early:\n%s", text)
	}
	if !strings.Contains(text, "# 2 passed, 1 failed") {
		t.Errorf("summary missing:\n%s", text)
	}
}

func TestARunWithoutARunnerIsAnError(t *testing.T) {
	if _, err := Run(context.Background(), nil, nil, Options{}); err == nil {
		t.Fatal("Run accepted a nil RunFunc")
	}
}

func TestWriteGoesThroughTheApprovalGate(t *testing.T) {
	var write Scenario
	for _, scenario := range loadCore(t) {
		if strings.Contains(scenario.Name, "write needs approval") {
			write = scenario
		}
	}
	if write.Name == "" {
		t.Fatal("core.json has no write scenario")
	}
	denied := false
	write.Approve = &denied

	var out bytes.Buffer
	report, err := Run(context.Background(), []Scenario{write}, fakeRun, Options{Out: &out})
	if err == nil {
		t.Fatalf("a denied write still passed:\n%s", out.String())
	}
	if report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Results[0].Reason, `file "out.txt" does not exist`) {
		t.Fatalf("unexpected reason: %q", report.Results[0].Reason)
	}
}

func TestCancellationIsAStopNotAFailure(t *testing.T) {
	scenario := Scenario{
		Name:        "cancel",
		Prompt:      "loop",
		Repeat:      true,
		CancelAfter: 2,
		Provider: []ScriptTurn{{
			Respond: Respond{ToolCalls: []ToolCall{{Name: "ls", Arguments: json.RawMessage(`{"path":"."}`)}}},
		}},
		Expect: Expect{ToolCalls: []string{"ls", "ls"}, Turns: intPtr(2), Stopped: boolPtr(true)},
	}
	var out bytes.Buffer
	report, err := Run(context.Background(), []Scenario{scenario}, fakeRun, Options{Out: &out})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if !report.Results[0].Stopped {
		t.Fatal("the result is not marked stopped")
	}
}

// ------------------------------------------------------------ grading tests

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestEvaluateChecksEveryExpectation(t *testing.T) {
	tests := []struct {
		name     string
		expect   Expect
		trace    Trace
		observed observation
		want     string // "" means the scenario must pass
	}{
		{
			name:   "a matching run passes",
			expect: Expect{ToolCalls: []string{"read"}, TextContains: []string{"notes"}, Turns: intPtr(2), Stopped: boolPtr(false)},
			trace:  Trace{Tools: []string{"read"}, Text: "the notes say hi"},
			observed: observation{
				calls: 2,
			},
		},
		{
			name:     "a different tool sequence fails",
			expect:   Expect{ToolCalls: []string{"read", "write"}},
			trace:    Trace{Tools: []string{"read"}},
			observed: observation{calls: 1},
			want:     "expected tools [read write], ran [read]",
		},
		{
			name:     "an empty tool list means no tools at all",
			expect:   Expect{ToolCalls: []string{}},
			trace:    Trace{Tools: []string{"ls"}},
			observed: observation{calls: 1},
			want:     "expected tools [], ran [ls]",
		},
		{
			name:     "a nil tool list is not checked",
			expect:   Expect{},
			trace:    Trace{Tools: []string{"ls"}},
			observed: observation{calls: 1},
		},
		{
			name:     "missing answer text fails",
			expect:   Expect{TextContains: []string{"answer"}},
			trace:    Trace{Text: "nope"},
			observed: observation{calls: 1},
			want:     `the answer does not contain "answer"`,
		},
		{
			name:     "forbidden answer text fails",
			expect:   Expect{TextNotContains: []string{"panic"}},
			trace:    Trace{Text: "a panic happened"},
			observed: observation{calls: 1},
			want:     `the answer must not contain "panic"`,
		},
		{
			name:     "the turn count is checked",
			expect:   Expect{Turns: intPtr(3)},
			trace:    Trace{},
			observed: observation{calls: 2},
			want:     "expected 3 turns, the model was called 2 times",
		},
		{
			name:     "the stop flag is checked",
			expect:   Expect{Stopped: boolPtr(true)},
			trace:    Trace{Stopped: false},
			observed: observation{calls: 1},
			want:     "expected stopped=true, got false",
		},
		{
			name:     "a broken request expectation fails first",
			expect:   Expect{TextContains: []string{"fine"}},
			trace:    Trace{Text: "fine"},
			observed: observation{calls: 1, violations: []string{`turn 2: the request does not contain "needle"`}},
			want:     `turn 2: the request does not contain "needle"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := evaluate(Scenario{Expect: test.expect}, test.trace, test.observed, t.TempDir())
			if test.want == "" {
				if got != "" {
					t.Fatalf("evaluate = %q, want a pass", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("evaluate = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestCheckWorkspaceChecksSeededFiles(t *testing.T) {
	dir := t.TempDir()
	if err := seed(dir, map[string]string{"notes.txt": "hello world\n", "nested/a.txt": "a\n"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "a.txt")); err != nil {
		t.Fatalf("nested file was not seeded: %v", err)
	}

	tests := []struct {
		name  string
		check Check
		want  string
	}{
		{name: "existing files pass", check: Check{FilesExist: []string{"notes.txt", "nested/a.txt"}}},
		{name: "content passes", check: Check{FileContains: map[string]string{"notes.txt": "world"}}},
		{name: "a missing file fails", check: Check{FilesExist: []string{"missing.txt"}}, want: `file "missing.txt" does not exist`},
		{name: "wrong content fails", check: Check{FileContains: map[string]string{"notes.txt": "goodbye"}}, want: `file "notes.txt" does not contain "goodbye"`},
		{name: "an unreadable file fails", check: Check{FileContains: map[string]string{"missing.txt": "x"}}, want: `cannot read "missing.txt"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := checkWorkspace(test.check, dir)
			if test.want == "" {
				if got != "" {
					t.Fatalf("checkWorkspace = %q, want a pass", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("checkWorkspace = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

func TestARunSeesTheSeededWorkspaceAndItsChecks(t *testing.T) {
	scenario := Scenario{
		Name:     "seed check",
		Prompt:   "read",
		Files:    map[string]string{"notes.txt": "seeded\n"},
		Provider: []ScriptTurn{{Respond: Respond{Text: "done"}}},
		Expect:   Expect{TextContains: []string{"done"}},
		Check:    Check{FilesExist: []string{"notes.txt"}, FileContains: map[string]string{"notes.txt": "seeded"}},
	}
	seeded := false
	run := func(ctx context.Context, scenario Scenario, workspace Workspace) (Trace, error) {
		data, err := os.ReadFile(filepath.Join(workspace.Dir, "notes.txt"))
		if err == nil && string(data) == "seeded\n" && workspace.Model == DefaultModel {
			seeded = true
		}
		return fakeRun(ctx, scenario, workspace)
	}
	report, err := Run(context.Background(), []Scenario{scenario}, run, Options{Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !seeded {
		t.Fatal("the RunFunc did not see the seeded workspace")
	}
	if report.Passed != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// ------------------------------------------------------------ parsing tests

func TestParseAcceptsADocumentAndABareArray(t *testing.T) {
	document := []byte(`{"scenarios":[{"name":"a","prompt":"p"},{"name":"b","prompt":"q"}]}`)
	array := []byte(`[{"name":"a","prompt":"p"},{"name":"b","prompt":"q"}]`)
	for _, data := range [][]byte{document, array} {
		scenarios, err := Parse(data)
		if err != nil {
			t.Fatalf("Parse(%s): %v", data, err)
		}
		if len(scenarios) != 2 || scenarios[0].Name != "a" || scenarios[1].Name != "b" {
			t.Fatalf("Parse = %+v", scenarios)
		}
	}
}

func TestParseAppliesDefaultsAndHints(t *testing.T) {
	data := []byte(`{
		"scenarios": [{
			"prompt": "p",
			"repeat": true,
			"max_turns": 2,
			"cancel_after": 1,
			"approve": false,
			"deny_paths": ["secret.env"],
			"provider": [{"respond": {"tool_calls": [{"name": "read", "arguments": "{\"path\":\"a\"}"}]}}],
			"expect": {"tool_calls": ["read"]}
		}]
	}`)
	scenarios, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	scenario := scenarios[0]
	if scenario.Name != "scenario 1" || scenario.Model != DefaultModel {
		t.Fatalf("defaults not applied: %+v", scenario)
	}
	if !scenario.Repeat || scenario.MaxTurns != 2 || scenario.CancelAfter != 1 {
		t.Fatalf("hints not decoded: %+v", scenario)
	}
	if scenario.Approve == nil || *scenario.Approve {
		t.Fatalf("approve hint not decoded: %+v", scenario.Approve)
	}
	if len(scenario.DenyPaths) != 1 || scenario.DenyPaths[0] != "secret.env" {
		t.Fatalf("deny paths not decoded: %+v", scenario.DenyPaths)
	}
	call := scenario.Provider[0].Respond.ToolCalls[0]
	if call.Name != "read" || call.ArgumentsString() != `{"path":"a"}` {
		t.Fatalf("string arguments not decoded: %+v", call)
	}
}

func TestParseRejectsAnEmptyDocument(t *testing.T) {
	if _, err := Parse([]byte(`{"scenarios":[]}`)); err == nil {
		t.Fatal("Parse accepted an empty suite")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("Parse accepted invalid JSON")
	}
}

func TestScenarioOmittingToolCallsMeansNoCheck(t *testing.T) {
	scenarios, err := Parse([]byte(`{"scenarios":[{"name":"n","prompt":"p","expect":{}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if scenarios[0].Expect.ToolCalls != nil {
		t.Fatalf("an absent list decoded as %#v", scenarios[0].Expect.ToolCalls)
	}
}

// ---------------------------------------------------- scripted provider tests

func TestScriptedProviderReplaysAndLoops(t *testing.T) {
	provider := NewScripted([]ScriptTurn{{Respond: Respond{Text: "tick"}}}, true)
	for i := 0; i < 4; i++ {
		reply, err := provider.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if reply.Content != "tick" || reply.Role != "assistant" {
			t.Fatalf("reply = %+v", reply)
		}
	}
	if provider.Calls() != 4 {
		t.Fatalf("Calls = %d, want 4", provider.Calls())
	}

	once := NewScripted([]ScriptTurn{{Respond: Respond{Text: "only"}}}, false)
	for i := 0; i < 2; i++ {
		reply, err := once.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if i == 1 && reply.Content != "the script ended" {
			t.Fatalf("an exhausted script said %q", reply.Content)
		}
	}
}

func TestScriptedProviderStreamsTextAndBuildsToolCalls(t *testing.T) {
	provider := NewScripted([]ScriptTurn{{
		Respond: Respond{
			Text:      "checking",
			ToolCalls: []ToolCall{{Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}, {Name: "ls"}},
		},
	}}, false)

	var streamed strings.Builder
	reply, err := provider.Chat(context.Background(), "m", nil, nil, func(delta string) {
		streamed.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if streamed.String() != "checking" {
		t.Fatalf("streamed %q", streamed.String())
	}
	if len(reply.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", reply.ToolCalls)
	}
	if reply.ToolCalls[0].ID == reply.ToolCalls[1].ID {
		t.Fatalf("tool call ids are not unique: %+v", reply.ToolCalls)
	}
	if reply.ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("arguments = %q", reply.ToolCalls[0].Function.Arguments)
	}
	if reply.ToolCalls[1].Function.Arguments != "{}" {
		t.Fatalf("missing arguments should default to {}, got %q", reply.ToolCalls[1].Function.Arguments)
	}
}

func TestScriptedProviderReturnsUsageAndChecksCacheMarkers(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 100, CompletionTokens: 5, CachedTokens: 40}
	provider := NewScripted([]ScriptTurn{
		{
			ExpectRequestCached: true,
			Respond:             Respond{Text: "warm", Usage: usage},
		},
		{
			ExpectRequestCacheFree: true,
			Respond:                Respond{Text: "cold"},
		},
		{
			ExpectRequestCached: true,
			Respond:             Respond{Text: "missing marker"},
		},
	}, false)

	marked := []llm.Message{{Role: "system", Content: "stable", CacheControl: llm.EphemeralCacheControl()}}
	reply, err := provider.Chat(context.Background(), "m", marked, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Usage != usage {
		t.Fatalf("usage was not carried into the reply: %+v", reply.Usage)
	}
	if _, err := provider.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "x"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := provider.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "x"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	violations := provider.Violations()
	if len(violations) != 1 || !strings.Contains(violations[0], "turn 3: the request has no cache marker") {
		t.Fatalf("violations = %v", violations)
	}
}

func TestScriptedProviderChecksTheRequest(t *testing.T) {
	provider := NewScripted([]ScriptTurn{{
		ExpectRequestContains: []string{"needle", "also missing"},
		Respond:               Respond{Text: "ok"},
	}}, false)

	messages := []llm.Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "haystack with a needle"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.txt"}`}}}},
		{Role: "tool", ToolCallID: "call-1-1", Content: "file body"},
	}
	if _, err := provider.Chat(context.Background(), "m", messages, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	violations := provider.Violations()
	if len(violations) != 1 || !strings.Contains(violations[0], "also missing") {
		t.Fatalf("violations = %v", violations)
	}
	history := provider.History()
	if len(history) != 1 || len(history[0]) != 4 {
		t.Fatalf("history = %+v", history)
	}
}

func TestScriptedProviderHonoursCancellation(t *testing.T) {
	provider := NewScripted([]ScriptTurn{{Respond: Respond{Text: "never"}}}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Chat(ctx, "m", nil, nil, nil); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if provider.Calls() != 0 {
		t.Fatalf("a cancelled Chat counted as a turn")
	}
}
