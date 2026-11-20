package cli

import (
	"context"
	"strings"
	"testing"
)

func TestHasTerminalNeedsAtLeastOneInteractiveStream(t *testing.T) {
	cases := []struct {
		name   string
		stdin  bool
		stdout bool
		want   bool
	}{
		{"both interactive", true, true, true},
		{"stdin only", true, false, true},
		{"stdout only", false, true, true},
		{"neither interactive", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := func(fd uintptr) bool {
				switch fd {
				case 0:
					return tc.stdin
				case 1:
					return tc.stdout
				default:
					return false
				}
			}
			if got := hasTerminal(fake, 0, 1); got != tc.want {
				t.Fatalf("hasTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTUIWithoutTerminalBailsBeforeFrontend(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"tui subcommand", []string{"tui"}},
		{"tui flag", []string{"--tui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			frontend := func(context.Context, Options) error {
				called = true
				return nil
			}
			ioState, _, errOut := newTestIO("", nil)
			ioState.HasTerminal = func() bool { return false }
			app := App{IO: ioState, TUI: frontend}

			code := RunWith(app, tc.args)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr %q", code, errOut.String())
			}
			if called {
				t.Fatal("TUI frontend was called despite no terminal")
			}
			if !strings.Contains(errOut.String(), "interactive terminal") {
				t.Fatalf("stderr = %q, want terminal hint", errOut.String())
			}
		})
	}
}

func TestTUIWithTerminalReachesFrontend(t *testing.T) {
	called := false
	frontend := func(context.Context, Options) error {
		called = true
		return nil
	}
	ioState, _, errOut := newTestIO("", nil)
	ioState.HasTerminal = func() bool { return true }
	app := App{IO: ioState, TUI: frontend}

	if code := RunWith(app, []string{"tui"}); code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut.String())
	}
	if !called {
		t.Fatal("TUI frontend was not called despite an available terminal")
	}
}
