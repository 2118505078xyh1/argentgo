package cli

import (
	"context"
	"testing"
)

// promptCaptureRunner 记录 CLI 在交给 agent 之前解析出的 prompt，
// 这样测试就能区分字面量 "-" prompt 和从 stdin 读取的内容。
type promptCaptureRunner struct {
	prompt string
	reply  string
}

func (r *promptCaptureRunner) Prompt(_ context.Context, prompt string, _ func(string)) (string, error) {
	r.prompt = prompt
	return r.reply, nil
}

func (r *promptCaptureRunner) Abort() {}

func TestRunPromptDashReadsStdin(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"lone dash", []string{"-"}},
		{"dash with flags", []string{"--model", "m1", "-"}},
		{"dash before flag", []string{"-", "-m", "m2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := &promptCaptureRunner{reply: "ok"}
			factory := func(Options) (Runner, error) { return captured, nil }
			ioState, out, errOut := newTestIO("from stdin\n", nil)
			if code := RunWith(App{IO: ioState, NewRunner: factory}, tc.args); code != 0 {
				t.Fatalf("exit = %d, err %q", code, errOut.String())
			}
			if captured.prompt != "from stdin" {
				t.Fatalf("prompt = %q, want the stdin contents", captured.prompt)
			}
			if out.String() != "ok\n" {
				t.Fatalf("out = %q", out.String())
			}
		})
	}
}

func TestRunPromptDashAfterTerminatorIsLiteral(t *testing.T) {
	// "--" makes every following word literal, so a trailing "-" is a real
	// prompt word and must not be swallowed as the stdin spelling.
	captured := &promptCaptureRunner{reply: "ok"}
	factory := func(Options) (Runner, error) { return captured, nil }
	ioState, _, errOut := newTestIO("stdin contents\n", nil)
	if code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"--", "-"}); code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if captured.prompt != "-" {
		t.Fatalf("prompt = %q, want literal %q", captured.prompt, "-")
	}
}
