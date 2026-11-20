package workflow

import (
	"context"
	"strings"
	"testing"
)

// TestStepNameWhitespaceIsNormalisedForOutputReferences checks that a step
// name with surrounding whitespace — which Validate accepts because it
// already trims for the uniqueness check — resolves from a
// {{steps.NAME.output}} reference. Before normalisation the executor keyed
// outputs by the raw name while the reference carried the trimmed name, so
// every consumer was skipped and the token was left unsubstituted.
func TestStepNameWhitespaceIsNormalisedForOutputReferences(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "PAYLOAD", nil
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, "{\"steps\":[\n\t\t{\"name\":\"  scan \\t\",\"prompt\":\"produce\"},\n\t\t{\"name\":\"report\",\"prompt\":\"report {{steps.scan.output}}\"}\n\t]}")

	if got := def.Steps[0].Name; got != "scan" {
		t.Fatalf("parsed step name = %q, want %q", got, "scan")
	}
	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"produce", "report PAYLOAD"}
	if len(rec.prompts) != len(want) {
		t.Fatalf("prompts = %q, want %q", rec.prompts, want)
	}
	for i := range want {
		if rec.prompts[i] != want[i] {
			t.Errorf("prompt[%d] = %q, want %q", i, rec.prompts[i], want[i])
		}
	}
	if out, ok := run.Output("scan"); !ok || out != "PAYLOAD" {
		t.Errorf("Output(scan) = %q, ok=%v", out, ok)
	}
}

// TestRunNormalisesStepNamesOnACopy guards the direct-Definition path: a
// caller that builds a Definition in code (rather than parsing JSON) still
// gets working output references, and its original definition is not mutated.
func TestRunNormalisesStepNamesOnACopy(t *testing.T) {
	def := &Definition{Steps: []Step{
		{Name: " produce ", Prompt: "produce"},
		{Name: "report ", Prompt: "report {{steps.produce.output}}"},
	}}
	exec := &Executor{Prompter: prompterFunc(func(ctx context.Context, text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "DATA", nil
		}
		return "ok:" + text, nil
	})}

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	if run.Steps[0].Name != "produce" || run.Steps[1].Name != "report" {
		t.Errorf("result names = %q, %q", run.Steps[0].Name, run.Steps[1].Name)
	}
	if out, ok := run.Output("produce"); !ok || out != "DATA" {
		t.Errorf("Output(produce) = %q, ok=%v", out, ok)
	}
	// The caller's definition must be untouched.
	if def.Steps[0].Name != " produce " || def.Steps[1].Name != "report " {
		t.Errorf("Run mutated the caller's definition: %+v", def.Steps)
	}
}
