package cli

import (
	"context"
	"strings"
	"testing"
)

// recordingTUI 记录全屏前端被调用的次数与解析后的选项，用于验证
// "无参数默认进入 TUI" 这条分支确实被走到，且没有误触其它路径。
type recordingTUI struct {
	calls int
	opts  []Options
}

func (r *recordingTUI) run(_ context.Context, opts Options) error {
	r.calls++
	r.opts = append(r.opts, opts)
	return nil
}

func TestNoArgsDefaultsToTUI(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil) // newTestIO 默认 HasTerminal=true
	rec := &recordingTUI{}
	code := RunWith(App{IO: ioState, TUI: rec.run}, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (err %q)", code, errOut.String())
	}
	if rec.calls != 1 {
		t.Fatalf("TUI calls = %d, want 1", rec.calls)
	}
	if len(rec.opts) != 1 || !rec.opts[0].TUI {
		t.Fatalf("TUI opts = %+v, want single call with TUI=true", rec.opts)
	}
}

func TestNoArgsNonTTYShowsUsage(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	ioState.HasTerminal = func() bool { return false }
	rec := &recordingTUI{}
	code := RunWith(App{IO: ioState, TUI: rec.run}, nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: argentgo") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
	if rec.calls != 0 {
		t.Fatalf("TUI calls = %d, want 0 (non-TTY must not hang in the frontend)", rec.calls)
	}
}

func TestNoArgsNonTTYPipedStdinStillOneShot(t *testing.T) {
	ioState, out, _ := newTestIO("hi\n", nil)
	ioState.HasTerminal = func() bool { return false }
	factory := func(Options) (Runner, error) { return &stubRunner{reply: "ok"}, nil }
	rec := &recordingTUI{}
	code := RunWith(App{IO: ioState, NewRunner: factory, TUI: rec.run}, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out.String() != "ok\n" {
		t.Fatalf("stdout = %q, want one-shot answer", out.String())
	}
	if rec.calls != 0 {
		t.Fatalf("TUI calls = %d, want 0", rec.calls)
	}
}

func TestArgentNoTUIEscape(t *testing.T) {
	ioState, _, errOut := newTestIO("", map[string]string{"ARGENT_NO_TUI": "1"})
	rec := &recordingTUI{}
	code := RunWith(App{IO: ioState, TUI: rec.run}, nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: argentgo") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
	if rec.calls != 0 {
		t.Fatalf("TUI calls = %d, want 0", rec.calls)
	}
}

func TestHelpVersionStillWork(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"version"}} {
		ioState, out, _ := newTestIO("", nil)
		rec := &recordingTUI{}
		code := RunWith(App{IO: ioState, Version: "9.9.9", TUI: rec.run}, args)
		if code != 0 {
			t.Fatalf("args %v: exit = %d, want 0", args, code)
		}
		if rec.calls != 0 {
			t.Fatalf("args %v: TUI calls = %d, want 0", args, rec.calls)
		}
		if out.Len() == 0 {
			t.Fatalf("args %v: stdout empty", args)
		}
	}
}

func TestPositionalPromptBypassesTUI(t *testing.T) {
	for _, args := range [][]string{{"hi"}, {"--model", "m", "hi"}} {
		ioState, out, _ := newTestIO("", nil)
		factory := func(Options) (Runner, error) { return &stubRunner{reply: "ok"}, nil }
		rec := &recordingTUI{}
		code := RunWith(App{IO: ioState, NewRunner: factory, TUI: rec.run}, args)
		if code != 0 {
			t.Fatalf("args %v: exit = %d, want 0", args, code)
		}
		if out.String() != "ok\n" {
			t.Fatalf("args %v: stdout = %q, want one-shot answer", args, out.String())
		}
		if rec.calls != 0 {
			t.Fatalf("args %v: TUI calls = %d, want 0", args, rec.calls)
		}
	}
}

func TestSubcommandsStillDispatch(t *testing.T) {
	for _, args := range [][]string{{"models"}, {"doctor", "--help"}} {
		ioState, out, _ := newTestIO("", nil)
		rec := &recordingTUI{}
		code := RunWith(App{IO: ioState, TUI: rec.run}, args)
		if code != 0 {
			t.Fatalf("args %v: exit = %d, want 0", args, code)
		}
		if rec.calls != 0 {
			t.Fatalf("args %v: TUI calls = %d, want 0 (subcommand must not fall through to default TUI)", args, rec.calls)
		}
		if out.Len() == 0 {
			t.Fatalf("args %v: stdout empty, subcommand did not run", args)
		}
	}
}
