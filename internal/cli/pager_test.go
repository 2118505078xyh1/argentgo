package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/server"
)

// errWriter fails every write with err, standing in for a stdout pipe whose
// reading end (e.g. `head`) has already been closed.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWritePagedSwallowsBrokenPipe(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"EPIPE", syscall.EPIPE},
		{"wrapped EPIPE", fmt.Errorf("write /dev/stdout: %w", syscall.EPIPE)},
		{"closed file", os.ErrClosed},
		{"closed pipe", io.ErrClosedPipe},
		{"windows message", errors.New("write |1: The pipe is being closed.")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := writePaged(errWriter{err: tc.err}, "some content", "less -R", true)
			if got != nil {
				t.Fatalf("writePaged returned %v, want nil for a broken pipe", got)
			}
		})
	}
}

func TestWritePagedWritesContentNormally(t *testing.T) {
	content := "line one\nline two\n"
	var out bytes.Buffer
	if err := writePaged(&out, content, "", true); err != nil {
		t.Fatalf("writePaged returned %v, want nil", err)
	}
	if out.String() != content {
		t.Fatalf("output = %q, want %q", out.String(), content)
	}
}

func TestWritePagedNonTTYDoesNotPage(t *testing.T) {
	// Long content plus a configured pager still goes straight to the writer
	// when stdout is not a terminal; no subprocess is involved.
	content := strings.Repeat("x\n", pagerMinBytes)
	var out bytes.Buffer
	if err := writePaged(&out, content, "less -R", false); err != nil {
		t.Fatalf("writePaged returned %v, want nil", err)
	}
	if out.String() != content {
		t.Fatalf("non-TTY output = %d bytes, want %d", out.Len(), len(content))
	}
}

func TestShouldPage(t *testing.T) {
	long := strings.Repeat("a", pagerMinBytes)
	cases := []struct {
		name    string
		pager   string
		content string
		isTTY   bool
		want    bool
	}{
		{"no pager", "", long, true, false},
		{"blank pager", "  ", long, true, false},
		{"not a terminal", "less -R", long, false, false},
		{"short output", "less -R", "tiny", true, false},
		{"pager wanted", "less -R", long, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPage(tc.pager, tc.content, tc.isTTY); got != tc.want {
				t.Fatalf("shouldPage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsBrokenPipe(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"EPIPE", syscall.EPIPE, true},
		{"closed", os.ErrClosed, true},
		{"unrelated", errors.New("disk full"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBrokenPipe(tc.err); got != tc.want {
				t.Fatalf("isBrokenPipe = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionsShowBrokenPipeStaysSuccess(t *testing.T) {
	// Wiring check: a closed downstream pipe must not turn `sessions show`
	// into a failed command with a spurious error on stderr.
	ioState, _, errOut := newTestIO("", nil)
	ioState.Out = errWriter{err: syscall.EPIPE}
	store := &fakeStore{events: map[string][]server.SessionEvent{
		"s1": {
			{Kind: "text", Data: []byte(`{"text":"hello"}`)},
			{Kind: "text", Data: []byte(`{"text":"world"}`)},
		},
	}}
	app := App{IO: ioState, Sessions: store}

	if code := runSessions(app, context.Background(), []string{"show", "s1"}); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}
