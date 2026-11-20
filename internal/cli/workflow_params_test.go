package cli

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// workflowCaptureRunner records the exact text the executor sends to the
// model, so the test can prove a declared parameter was substituted.
type workflowCaptureRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *workflowCaptureRunner) Prompt(_ context.Context, text string, _ func(string)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, text)
	return "done", nil
}

func (r *workflowCaptureRunner) Abort() {}

func (r *workflowCaptureRunner) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

func writeParamWorkflow(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.json")
	writeFile(t, path, body)
	return path
}

func TestWorkflowRunSubstitutesParamFlags(t *testing.T) {
	path := writeParamWorkflow(t, `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"},
		{"key":"branch","input_type":"string","requirement":"optional"}
	],"steps":[{"name":"one","prompt":"review {{repo}} on {{branch}} for {{input}}"}]}`)
	runner := &workflowCaptureRunner{}
	ioState, out, errOut := newTestIO("", nil)
	factory := func(Options) (Runner, error) { return runner, nil }
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{
		"workflow", "run", path, "--input", "the diff", "--param", "repo=acme/api", "--param=branch=dev",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	got := runner.recorded()
	if len(got) != 1 || got[0] != "review acme/api on dev for the diff" {
		t.Fatalf("prompts = %q", got)
	}
	if !strings.Contains(out.String(), "ok   one") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestWorkflowRunBlocksMissingRequiredParam(t *testing.T) {
	path := writeParamWorkflow(t, `{"name":"review","parameters":[
		{"key":"repo","input_type":"string","requirement":"required"}
	],"steps":[{"name":"one","prompt":"review {{repo}}"}]}`)
	ioState, _, errOut := newTestIO("", nil)
	// No runner: a missing required value must block before the runner is even
	// constructed, so the reported error cannot be "no runner configured".
	code := RunWith(App{IO: ioState}, []string{"workflow", "run", path})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (err %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), `parameter "repo" is required`) {
		t.Fatalf("err = %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "no runner configured") {
		t.Fatalf("the run should have been blocked before building a runner: %q", errOut.String())
	}
}

func TestWorkflowRunFillsParamDefault(t *testing.T) {
	path := writeParamWorkflow(t, `{"name":"review","parameters":[
		{"key":"branch","input_type":"string","requirement":"optional","default":"main"}
	],"steps":[{"name":"one","prompt":"use {{branch}}"}]}`)
	runner := &workflowCaptureRunner{}
	ioState, _, errOut := newTestIO("", nil)
	factory := func(Options) (Runner, error) { return runner, nil }
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"workflow", "run", path})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if got := runner.recorded(); len(got) != 1 || got[0] != "use main" {
		t.Fatalf("prompts = %q", got)
	}
}

func TestWorkflowRunRejectsOversizedParam(t *testing.T) {
	const canary = "canary-secret-242"
	path := writeParamWorkflow(t, `{"name":"review","parameters":[
		{"key":"blob","input_type":"string","requirement":"optional"}
	],"steps":[{"name":"one","prompt":"use {{blob}}"}]}`)
	runner := &workflowCaptureRunner{}
	ioState, _, errOut := newTestIO("", nil)
	factory := func(Options) (Runner, error) { return runner, nil }
	oversized := strings.Repeat("y", (128<<10)+1) + canary
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{
		"workflow", "run", path, "--param=blob=" + oversized,
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (err %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "value exceeds") {
		t.Fatalf("err = %q", errOut.String())
	}
	if strings.Contains(errOut.String(), canary) {
		t.Fatalf("error leaked the secret value: %q", errOut.String())
	}
	if got := runner.recorded(); len(got) != 0 {
		t.Fatalf("the model must not run for an oversized value: %q", got)
	}
}

func TestWorkflowRunRejectsMalformedParamFlag(t *testing.T) {
	path := writeParamWorkflow(t, `{"name":"review","steps":[{"name":"one","prompt":"hi"}]}`)
	for _, args := range [][]string{
		{"workflow", "run", path, "--param"},
		{"workflow", "run", path, "--param", "repo"},
		{"workflow", "run", path, "--param= =value"},
	} {
		t.Run(strings.Join(args[2:], " "), func(t *testing.T) {
			ioState, _, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState}, args)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (err %q)", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), "key=value") {
				t.Fatalf("err = %q", errOut.String())
			}
		})
	}
}
