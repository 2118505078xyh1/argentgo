package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type prompterFunc func(ctx context.Context, text string) (string, error)

func (f prompterFunc) Prompt(ctx context.Context, text string) (string, error) { return f(ctx, text) }

type toolsFunc func(ctx context.Context, name, args string) (string, error)

func (f toolsFunc) Run(ctx context.Context, name, args string) (string, error) {
	return f(ctx, name, args)
}

// recorder 保存每个提示和工具调用以供断言。
type recorder struct {
	mu      sync.Mutex
	prompts []string
	calls   []string
}

func (r *recorder) prompter(answer func(text string) (string, error)) Prompter {
	return prompterFunc(func(ctx context.Context, text string) (string, error) {
		r.mu.Lock()
		r.prompts = append(r.prompts, text)
		r.mu.Unlock()
		return answer(text)
	})
}

func (r *recorder) tools(answer func(name, args string) (string, error)) ToolRunner {
	return toolsFunc(func(ctx context.Context, name, args string) (string, error) {
		r.mu.Lock()
		r.calls = append(r.calls, name+" "+args)
		r.mu.Unlock()
		return answer(name, args)
	})
}

func (r *recorder) promptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func mustDefinition(t *testing.T, body string) *Definition {
	t.Helper()
	def, err := ParseDefinition([]byte(body))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	return def
}

func TestParseDefinitionDefaults(t *testing.T) {
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"do a"},
		{"name":"b","tool":"read","args":"{\"path\":\"x\"}","retries":2,"timeout":"45s"}
	]}`)
	if def.Name != "workflow" {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Steps[0].OnFailure != OnFailureStop {
		t.Fatalf("on_failure default = %q", def.Steps[0].OnFailure)
	}
	if got := def.Steps[1].Timeout.Std(); got != 45*time.Second {
		t.Fatalf("timeout = %s", got)
	}
	if def.Steps[1].Retries != 2 || def.Steps[1].Tool != "read" {
		t.Fatalf("tool step = %+v", def.Steps[1])
	}
}

func TestDefinitionValidation(t *testing.T) {
	bad := []string{
		`{"steps":[]}`,
		`{"steps":[{"prompt":"no name"}]}`,
		`{"steps":[{"name":"a"}]}`,
		`{"steps":[{"name":"a","prompt":"x"},{"name":"a","prompt":"y"}]}`,
		`{"steps":[{"name":"a","prompt":"x","on_failure":"explode"}]}`,
		`{"steps":[{"name":"a","prompt":"x","timeout":"-5s"}]}`,
		`{"steps":[{"name":"a","prompt":"x","timeout":"soon"}]}`,
		`{"steps":`,
	}
	for _, body := range bad {
		if _, err := ParseDefinition([]byte(body)); err == nil {
			t.Fatalf("definition %q should be rejected", body)
		}
	}
	if _, err := ParseDefinition([]byte(`{"steps":[{"name":"a","prompt":"x","retries":99}]}`)); err != nil {
		t.Fatalf("a large retry count should be clamped, not rejected: %v", err)
	}
}

func TestRunSubstitutesTemplates(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if text == "list files" {
			return "files: a.go, b.go", nil
		}
		return "reviewed", nil
	})}
	def := mustDefinition(t, `{"name":"review","steps":[
		{"name":"scan","prompt":"list files"},
		{"name":"review","prompt":"review {{steps.scan.output}} for {{ input }}"}
	]}`)

	run, err := exec.Run(context.Background(), def, "the diff")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() || len(run.Steps) != 2 {
		t.Fatalf("unexpected run: %+v", run)
	}
	want := "review files: a.go, b.go for the diff"
	if rec.prompts[1] != want {
		t.Fatalf("prompt = %q, want %q", rec.prompts[1], want)
	}
	if out, ok := run.Output("scan"); !ok || out != "files: a.go, b.go" {
		t.Fatalf("scan output = %q, ok=%v", out, ok)
	}
}

func TestSubstituteKeepsUnknownPlaceholders(t *testing.T) {
	outputs := map[string]string{"scan": "S"}
	cases := map[string]string{
		"{{input}}":                  "I",
		"{{ input }}":                "I",
		"a {{steps.scan.output}} b":  "a S b",
		"{{steps.missing.output}}":   "{{steps.missing.output}}",
		"{{unknown}}":                "{{unknown}}",
		"{{unterminated":             "{{unterminated",
		"no placeholders":            "no placeholders",
		"{{steps.scan.output}}-tail": "S-tail",
	}
	for input, want := range cases {
		if got := Substitute(input, "I", outputs); got != want {
			t.Fatalf("Substitute(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRunConditions(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if text == "scan" {
			return "all good", nil
		}
		return "done", nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"scan","prompt":"scan"},
		{"name":"fix","prompt":"fix","when":{"previous_contains":"BUG"}},
		{"name":"report","prompt":"report","when":{"previous_not_contains":"BUG"}}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusOK {
		t.Fatalf("status = %q", run.Status)
	}
	fix := run.Steps[1]
	if fix.Status != StatusSkipped || fix.Error != "condition not met" {
		t.Fatalf("fix step = %+v", fix)
	}
	if run.Steps[2].Status != StatusOK {
		t.Fatalf("report should run when the previous output has no BUG: %+v", run.Steps[2])
	}
	if rec.promptCount() != 2 {
		t.Fatalf("prompter called %d times, want 2", rec.promptCount())
	}
}

func TestRunConditionOnFirstStepWithoutPrevious(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "ok", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","when":{"previous_contains":"x"}}]}`)
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Steps[0].Status != StatusSkipped || rec.promptCount() != 0 {
		t.Fatalf("a condition with no previous step should skip: %+v", run.Steps[0])
	}
}

func TestRunRetriesThenSucceeds(t *testing.T) {
	rec := &recorder{}
	var attempts int
	exec := &Executor{Backoff: time.Millisecond, Prompter: rec.prompter(func(string) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("flaky")
		}
		return "third time", nil
	})}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","retries":2}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusOK {
		t.Fatalf("status = %q", run.Status)
	}
	if run.Steps[0].Attempts != 3 || run.Steps[0].Output != "third time" {
		t.Fatalf("step = %+v", run.Steps[0])
	}
}

func TestRunRetriesExhausted(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Backoff: time.Millisecond, Prompter: rec.prompter(func(string) (string, error) {
		return "", errors.New("boom")
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"a","retries":1},
		{"name":"b","prompt":"b"}
	]}`)
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if run.Steps[0].Attempts != 2 || run.Steps[0].Error != "boom" {
		t.Fatalf("step = %+v", run.Steps[0])
	}
	if run.Steps[1].Status != StatusSkipped {
		t.Fatalf("the default on_failure should stop the run: %+v", run.Steps[1])
	}
	if rec.promptCount() != 2 {
		t.Fatalf("prompter called %d times", rec.promptCount())
	}
}

func TestRunOnFailureContinue(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Backoff: time.Millisecond, Prompter: rec.prompter(func(text string) (string, error) {
		if text == "a" {
			return "", errors.New("boom")
		}
		return "ok " + text, nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"a","on_failure":"continue"},
		{"name":"b","prompt":"b"}
	]}`)
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if run.Steps[1].Status != StatusOK {
		t.Fatalf("continue should run the next step: %+v", run.Steps[1])
	}
}

func TestRunOnFailureRetryWithoutRetriesStillAttemptsTwice(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Backoff: time.Millisecond, Prompter: rec.prompter(func(string) (string, error) {
		return "", errors.New("boom")
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"a","on_failure":"retry"},
		{"name":"b","prompt":"b"}
	]}`)
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Steps[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", run.Steps[0].Attempts)
	}
	if run.Steps[1].Status != StatusSkipped || run.Status != StatusFailed {
		t.Fatalf("retry should stop the workflow once attempts run out: %+v", run.Steps)
	}
}

func TestRunStepTimeout(t *testing.T) {
	exec := &Executor{Backoff: time.Millisecond, Prompter: prompterFunc(func(ctx context.Context, text string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","timeout":"20ms"}]}`)
	started := time.Now()
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if !strings.Contains(run.Steps[0].Error, "timed out after 20ms") {
		t.Fatalf("error = %q", run.Steps[0].Error)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the timeout did not interrupt the step (%s)", elapsed)
	}
}

func TestRunToolStep(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{
		Prompter: rec.prompter(func(string) (string, error) { return "scan-output", nil }),
		Tools: rec.tools(func(name, args string) (string, error) {
			return name + ":" + args, nil
		}),
	}
	def := mustDefinition(t, `{"steps":[
		{"name":"scan","prompt":"scan"},
		{"name":"write","tool":"write","args":"{\"path\":\"{{input}}\",\"body\":\"{{steps.scan.output}}\"}"}
	]}`)
	run, err := exec.Run(context.Background(), def, "out.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	want := `write {"path":"out.txt","body":"scan-output"}`
	if len(rec.calls) != 1 || rec.calls[0] != want {
		t.Fatalf("tool calls = %q, want %q", rec.calls, want)
	}
}

func TestRunRejectsInvalidDefinitions(t *testing.T) {
	exec := &Executor{}
	if _, err := exec.Run(context.Background(), nil, ""); err == nil {
		t.Fatal("a nil definition should fail")
	}
	if _, err := exec.Run(context.Background(), &Definition{}, ""); err == nil {
		t.Fatal("a definition without steps should fail")
	}
	rec := &recorder{}
	exec = &Executor{Prompter: rec.prompter(func(string) (string, error) { return "", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","tool":"read"}]}`)
	if _, err := exec.Run(context.Background(), def, ""); err == nil {
		t.Fatal("a tool step without a tool runner should fail")
	}
}

func TestLoadDefinitionFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	body := `{"name":"x","steps":[{"name":"a","prompt":"do it","on_failure":"continue"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	def, err := LoadDefinition(path)
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if def.Name != "x" || def.Steps[0].OnFailure != OnFailureContinue {
		t.Fatalf("loaded = %+v", def)
	}
	if _, err := LoadDefinition(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing file should fail")
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadDefinition(path); err == nil {
		t.Fatal("broken JSON should fail")
	}
}
