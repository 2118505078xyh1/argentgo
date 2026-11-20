package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestRunWithParamsSubstitutesDeclaredParameters(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) {
		return "files: a.go", nil
	})}
	def := mustDefinition(t, `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"},
		{"key":"branch","input_type":"string","requirement":"optional","default":"main"}
	],"steps":[
		{"name":"scan","prompt":"scan {{repo}} at {{branch}} for {{input}}"},
		{"name":"report","prompt":"report {{steps.scan.output}} from {{repo}}"}
	]}`)

	run, err := exec.RunWithParams(context.Background(), def, "the diff", map[string]string{"repo": "acme/api"})
	if err != nil {
		t.Fatalf("RunWithParams: %v", err)
	}
	if !run.Succeeded() || len(run.Steps) != 2 {
		t.Fatalf("unexpected run: %+v", run)
	}
	want := []string{
		"scan acme/api at main for the diff",
		"report files: a.go from acme/api",
	}
	if len(rec.prompts) != len(want) {
		t.Fatalf("prompts = %q, want %q", rec.prompts, want)
	}
	for i := range want {
		if rec.prompts[i] != want[i] {
			t.Fatalf("prompt[%d] = %q, want %q", i, rec.prompts[i], want[i])
		}
	}
}

func TestRunWithParamsBlocksUnresolvedParameters(t *testing.T) {
	const canary = "canary-secret-242"
	def := mustDefinition(t, `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"},
		{"key":"blob","input_type":"string","requirement":"optional"}
	],"steps":[{"name":"one","prompt":"review {{repo}} {{blob}}"}]}`)

	for _, tc := range []struct {
		name    string
		params  map[string]string
		wantErr string
	}{
		{"missing required", nil, `parameter "repo" is required`},
		{"empty required", map[string]string{"repo": ""}, `parameter "repo" is required`},
		{"oversized value", map[string]string{"repo": "x", "blob": strings.Repeat("y", maxParamValueBytes+1) + canary}, "value exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "ok", nil })}
			_, err := exec.RunWithParams(context.Background(), def, "diff", tc.params)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("error leaked the secret value: %q", err.Error())
			}
			if rec.promptCount() != 0 {
				t.Fatalf("the model must not run for an unresolved parameter: %q", rec.prompts)
			}
		})
	}

	// The backwards-compatible three-argument entry point obeys the same gate.
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "ok", nil })}
	if _, err := exec.Run(context.Background(), def, "diff"); err == nil || !strings.Contains(err.Error(), `parameter "repo" is required`) {
		t.Fatalf("Run with a required parameter unset should block, got %v", err)
	}
	if rec.promptCount() != 0 {
		t.Fatalf("the model must not run for an unresolved parameter: %q", rec.prompts)
	}
}

func TestSubstituteResolvesParamsWithoutShadowingBuiltins(t *testing.T) {
	outputs := map[string]string{"scan": "S"}
	params := map[string]string{
		"repo":              "R",
		"input":             "NOT-INPUT",
		"steps.scan.output": "NOT-OUTPUT",
	}
	got := Substitute("{{repo}} {{ input }} {{steps.scan.output}} {{missing}} {{steps.none.output}} {{ repo }}", "I", outputs, params)
	want := "R I S {{missing}} {{steps.none.output}} R"
	if got != want {
		t.Fatalf("Substitute = %q, want %q", got, want)
	}

	// Existing three-argument callers see unchanged behaviour.
	if got := Substitute("{{repo}} {{input}}", "I", outputs); got != "{{repo}} I" {
		t.Fatalf("Substitute without params = %q", got)
	}

	// Inserted values are not rescanned; a value cannot inject another token.
	if got := Substitute("{{repo}}", "I", nil, map[string]string{"repo": "{{input}}"}); got != "{{input}}" {
		t.Fatalf("parameter value was rescanned: %q", got)
	}
}
