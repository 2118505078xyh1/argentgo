package cli

import (
	"strings"
	"testing"
)

// A help flag is a request for help no matter where it appears in a jobs
// subcommand's argument list. It must be answered on stdout with exit code 0,
// not reported as "help requested" on stderr with exit code 2.
func TestJobsHelpAfterOtherArgsGoesToStdout(t *testing.T) {
	commands := []string{"attach", "logs", "jobs", "follow", "stop"}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			ioState, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState}, []string{command, "job1", "-h"})
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (err %q)", code, errOut.String())
			}
			if !strings.Contains(out.String(), "usage: argent attach") {
				t.Fatalf("stdout = %q", out.String())
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q", errOut.String())
			}
		})
	}
}

// The long --help spelling and a help flag between other flags behave the
// same way as -h at the end.
func TestJobsHelpLongSpellingAndMiddlePosition(t *testing.T) {
	for _, args := range [][]string{
		{"attach", "job1", "--help"},
		{"follow", "job1", "-n", "5", "-h"},
		{"stop", "-h", "job1"},
	} {
		ioState, out, errOut := newTestIO("", nil)
		code := RunWith(App{IO: ioState}, args)
		if code != 0 {
			t.Fatalf("%v: exit = %d, want 0 (err %q)", args, code, errOut.String())
		}
		if !strings.Contains(out.String(), "usage: argent attach") {
			t.Fatalf("%v: stdout = %q", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("%v: stderr = %q", args, errOut.String())
		}
	}
}

// A genuine parse error without a help flag still exits 2; the help sentinel
// must not swallow unrelated errors.
func TestJobsUnknownArgStillExitsTwo(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState}, []string{"attach", "job1", "--bogus"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "unexpected argument") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}
