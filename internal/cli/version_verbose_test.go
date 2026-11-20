package cli

import (
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestVersionVerboseIncludesBuildDetails(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Version: "9.9.9"}, []string{"version", "--verbose"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"argentgo 9.9.9\n",
		"go: " + runtime.Version() + "\n",
		"platform: " + runtime.GOOS + "/" + runtime.GOARCH + "\n",
		"protocols: ",
		llm.ProtocolAnthropicMessages,
		llm.ProtocolChatCompletions,
		llm.ProtocolResponses,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose version missing %q; got:\n%s", want, got)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestVersionVerboseProtocolLineIsSortedAndStable(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Version: "9.9.9"}, []string{"version", "--long"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "protocols: " + llm.ProtocolAnthropicMessages + ", " +
		llm.ProtocolChatCompletions + ", " + llm.ProtocolResponses + "\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("protocol line = %q, want %q", out.String(), want)
	}
}

func TestVersionVerboseShortFlagMatchesLong(t *testing.T) {
	long, longOut, _ := newTestIO("", nil)
	short, shortOut, _ := newTestIO("", nil)
	args := App{IO: long, Version: "9.9.9"}
	if code := RunWith(args, []string{"version", "--verbose"}); code != 0 {
		t.Fatalf("long exit = %d", code)
	}
	args.IO = short
	if code := RunWith(args, []string{"version", "-v"}); code != 0 {
		t.Fatalf("short exit = %d", code)
	}
	if longOut.String() != shortOut.String() {
		t.Fatalf("-v = %q, --verbose = %q", shortOut.String(), longOut.String())
	}
}

func TestVersionSubcommandDefaultKeepsOneLine(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Version: "9.9.9"}, []string{"version"}); code != 0 {
		t.Fatalf("exit = %d, err = %q", code, errOut.String())
	}
	if got := out.String(); got != "argentgo 9.9.9\n" {
		t.Fatalf("out = %q", got)
	}
	if strings.Contains(out.String(), "go:") {
		t.Fatal("default version must not include build details")
	}
}

func TestVersionVerboseHelpGoesToStdout(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"version", "--help"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "usage: argent version") {
		t.Fatalf("out = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("err = %q", errOut.String())
	}
}

func TestVersionVerboseRejectsUnknownArg(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState}, []string{"version", "--nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "unexpected argument") || !strings.Contains(errOut.String(), "usage: argent version") {
		t.Fatalf("err = %q", errOut.String())
	}
}
