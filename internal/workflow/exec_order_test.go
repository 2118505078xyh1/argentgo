package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRunExecutesStepsInDependencyOrder checks that a step-output reference is
// resolved at run time even when the producer is declared after the consumer.
// ValidateDAG accepts forward references and computes the order in which the
// dependencies come first; the executor must honour that order or the
// consumer's prompt keeps a literal {{steps...output}} token.
func TestRunExecutesStepsInDependencyOrder(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "PAYLOAD", nil
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, `{"name":"forward","steps":[
		{"name":"consume","prompt":"consume {{steps.produce.output}}"},
		{"name":"produce","prompt":"produce data"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	want := []string{"produce data", "consume PAYLOAD"}
	if len(rec.prompts) != len(want) {
		t.Fatalf("prompts = %q, want %q", rec.prompts, want)
	}
	for i := range want {
		if rec.prompts[i] != want[i] {
			t.Errorf("prompt[%d] = %q, want %q", i, rec.prompts[i], want[i])
		}
	}
	if out, ok := run.Output("consume"); !ok || out != "consumed:consume PAYLOAD" {
		t.Errorf("consume output = %q, ok=%v", out, ok)
	}
	if len(run.Steps) != 2 || run.Steps[0].Name != "produce" || run.Steps[1].Name != "consume" {
		t.Errorf("step results = %+v, want producer before consumer", run.Steps)
	}
}

// TestRunSkipsDependentWhenProducerFails checks that a consumer is never run
// with an unresolved step-output placeholder when its producer failed.
func TestRunSkipsDependentWhenProducerFails(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(text string) (string, error) {
		if strings.HasPrefix(text, "produce") {
			return "", errors.New("producer boom")
		}
		return "consumed:" + text, nil
	})}
	def := mustDefinition(t, `{"steps":[
		{"name":"consume","prompt":"consume {{steps.produce.output}}"},
		{"name":"produce","prompt":"produce data"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	if rec.promptCount() != 1 {
		t.Fatalf("the consumer must not run after its producer failed: %q", rec.prompts)
	}
	if len(run.Steps) != 2 || run.Steps[0].Name != "produce" || run.Steps[1].Name != "consume" || run.Steps[1].Status != StatusSkipped {
		t.Errorf("step results = %+v, want producer failed and consumer skipped", run.Steps)
	}
}
