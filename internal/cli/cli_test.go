package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type stubRunner struct {
	reply   string
	chunks  []string
	fail    error
	mu      sync.Mutex
	aborted bool
}

func (s *stubRunner) Prompt(_ context.Context, _ string, onText func(string)) (string, error) {
	for _, chunk := range s.chunks {
		if onText != nil {
			onText(chunk)
		}
	}
	return s.reply, s.fail
}

func (s *stubRunner) Abort() {
	s.mu.Lock()
	s.aborted = true
	s.mu.Unlock()
}

func TestRunVersion(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, Version: "9.9.9"}, []string{"--version"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out.String() != "argent-go 9.9.9\n" {
		t.Fatalf("out = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunVersionKeepsLegacyFormat(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := Run([]string{"--version"}, ioState); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out.String() != "argent-go "+Version+"\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestRunHelpGoesToStdout(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--help"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "usage: argent [flags]") {
		t.Fatalf("out = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunUnknownFlagExitsTwo(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined") {
		t.Fatalf("err = %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "usage: argent") {
		t.Fatalf("usage missing from stderr: %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestRunInvalidFlagValueExitsTwo(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--mode", "yaml"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--mode") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunMissingPromptExitsTwo(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, nil); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage: argent") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunPromptTextMode(t *testing.T) {
	var captured Options
	factory := func(opts Options) (Runner, error) {
		captured = opts
		return &stubRunner{reply: "answer", chunks: []string{"an", "swer"}}, nil
	}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, Version: "9.9.9", NewRunner: factory}, []string{"hello"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if out.String() != "answer\n" {
		t.Fatalf("out = %q", out.String())
	}
	if captured.Model != "gpt-4o-mini" || captured.Mode != ModeText || captured.MaxTurns != 12 {
		t.Fatalf("options = %+v", captured)
	}
	if captured.Cwd == "" {
		t.Fatal("cwd was not resolved")
	}
}

func TestRunPromptJSONMode(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{reply: "partial", chunks: []string{"par", "tial"}}, nil
	}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"--mode", "json", "hi"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %v", lines)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0: %v", err)
	}
	if first["type"] != "text" || first["text"] != "par" {
		t.Fatalf("line 0 = %v", first)
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil {
		t.Fatalf("line 2: %v", err)
	}
	if last["type"] != "result" || last["text"] != "partial" {
		t.Fatalf("line 2 = %v", last)
	}
}

func TestRunPromptErrorExitsOne(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{fail: errors.New("boom")}, nil
	}
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"hi"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunWithoutRunnerFails(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState}, []string{"hi"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "no runner configured") {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestRunPromptFromStdin(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{reply: "ok"}, nil
	}
	ioState, out, _ := newTestIO("from stdin\n", nil)
	if code := RunWith(App{IO: ioState, NewRunner: factory}, nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out.String() != "ok\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestRunServeRefusesWithoutToken(t *testing.T) {
	factory := func(Options) (Runner, error) { return &stubRunner{}, nil }
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"--serve", "--serve-port", "0"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("err = %q", errOut.String())
	}
}
