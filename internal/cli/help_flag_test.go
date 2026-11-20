package cli

import (
	"strings"
	"testing"
)

// TestHelpFlagSpellingsPrintUsageAndExitZero covers every spelling the Go flag
// package accepts for the help switches. parseArgs used to short-circuit only
// on the exact "-h"/"--help" tokens while recomputing Options.Help from that
// same narrow check, so the flag parser's other valid spellings were silently
// dropped: the command fell through to the empty-prompt path and printed usage
// to stderr with exit code 2.
func TestHelpFlagSpellingsPrintUsageAndExitZero(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"short", []string{"-h"}},
		{"long", []string{"--help"}},
		{"single dash long", []string{"-help"}},
		{"double dash short", []string{"--h"}},
		{"short equals true", []string{"-h=true"}},
		{"long equals true", []string{"--help=true"}},
		{"short equals one", []string{"-h=1"}},
		{"after a valid flag", []string{"--model", "m1", "-help"}},
		{"wins over an invalid flag", []string{"-help", "--protocol", "grpc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ioState, out, errOut := newTestIO("", nil)
			if code := RunWith(App{IO: ioState}, tc.args); code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
			}
			if !strings.Contains(out.String(), "usage: argent [flags] <prompt>") {
				t.Fatalf("stdout = %q, want usage", out.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", errOut.String())
			}
		})
	}
}

// TestHelpFalseDoesNotPrintHelp makes sure the widened help detection still
// respects an explicit false value instead of treating any "help" token as a
// request to print usage on stdout.
func TestHelpFalseDoesNotPrintHelp(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"--help=false"}); code != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", code, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errOut.String(), "usage: argent [flags] <prompt>") {
		t.Fatalf("stderr = %q, want usage", errOut.String())
	}
}
