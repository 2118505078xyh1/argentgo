package proc

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingDetachedStdin blocks forever in Read until released. It lets a test
// observe the detached stdin copier without depending on real I/O timing.
type blockingDetachedStdin struct {
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingDetachedStdin() *blockingDetachedStdin {
	return &blockingDetachedStdin{read: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingDetachedStdin) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.read) })
	<-r.release
	return 0, io.EOF
}

// TestDetachedDoneIsNotBlockedByStdinCopier guards the detached spawn path's
// stdin wiring. exec.Cmd.Wait waits for the copy goroutine it starts for a
// non-*os.File Stdin. A detached job whose Stdin reader never returns must
// still be reaped (Done closed) once the child exits or is killed, otherwise a
// promoted background job can never be observed as finished and its log file
// is never closed. Run already owns its stdin pipe; the detached path must do
// the same instead of handing an arbitrary io.Reader straight to exec.
func TestDetachedDoneIsNotBlockedByStdinCopier(t *testing.T) {
	path, args := testShell(t, "echo detached-stdin-ready")
	stdin := newBlockingDetachedStdin()
	defer close(stdin.release)

	p, err := StartDetached(Command{Path: path, Args: args, Stdin: stdin})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}

	// Make sure the copier actually engaged before judging Done.
	select {
	case <-stdin.read:
	case <-time.After(2 * time.Second):
		t.Fatal("detached stdin copier never read from Stdin")
	}

	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done never closed: cmd.Wait blocked on the stdin copy goroutine")
	}
}

const detachedStdinEchoMarker = "ARGENT_PROC_DETACHED_STDIN_ECHO"

// TestDetachedStdinEchoHelper is a re-executed test binary that copies stdin
// to stdout. It avoids console-dependent tools such as Windows more.exe when a
// detached child (no inherited console) must consume piped input.
func TestDetachedStdinEchoHelper(t *testing.T) {
	if os.Getenv(detachedStdinEchoMarker) == "" {
		return
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
}

// TestDetachedStillFeedsStdin makes sure owning the pipe ourselves does not
// break the useful case: bytes from a finite Stdin reader must reach the child.
func TestDetachedStillFeedsStdin(t *testing.T) {
	p, err := StartDetached(Command{
		Path:  os.Args[0],
		Args:  []string{"-test.run=^TestDetachedStdinEchoHelper$"},
		Env:   []string{detachedStdinEchoMarker + "=1"},
		Stdin: strings.NewReader("detached-stdin-payload\n"),
	})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}
	if code := p.Wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out := p.Output(); !strings.Contains(out, "detached-stdin-payload") {
		t.Fatalf("captured output = %q, want the stdin payload", out)
	}
}
