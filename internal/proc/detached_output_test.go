package proc

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDetachedSpawnCapturesChildOutput guards the writer wiring in startOS:
// exec copies the child's stdout/stderr into one switchWriter, and the returned
// Process must expose that very writer. A second writer allocated by NewProcess
// used to swallow every byte, leaving RunWithTimeout's partial output and the
// promoted job's log empty.
func TestDetachedSpawnCapturesChildOutput(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"stdout", "echo DETACHED_STDOUT", "DETACHED_STDOUT"},
		{"stderr", "echo DETACHED_STDERR 1>&2", "DETACHED_STDERR"},
		{"both streams", "echo DETACHED_STDOUT & echo DETACHED_STDERR 1>&2", "DETACHED_STDOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, args := testShell(t, tc.line)
			if runtime.GOOS != "windows" {
				// POSIX shell spelling of the two-stream case.
				args[len(args)-1] = strings.Replace(args[len(args)-1], " & ", "; ", 1)
			}
			p, err := StartDetached(Command{Path: path, Args: args})
			if err != nil {
				t.Fatalf("StartDetached: %v", err)
			}
			if code := p.Wait(); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if out := p.Output(); !strings.Contains(out, tc.want) {
				t.Fatalf("captured output = %q, want it to contain %q", out, tc.want)
			}
		})
	}
}

// TestRunWithTimeoutReturnsPartialOutputOnKeepOnTimeout exercises the real OS
// starter: a child that prints before blocking must hand that prefix back in
// the Result, and the promoted Process must still own the same captured bytes.
func TestRunWithTimeoutReturnsPartialOutputOnKeepOnTimeout(t *testing.T) {
	line := "echo PARTIAL_OUTPUT && sleep 30"
	if runtime.GOOS == "windows" {
		line = "echo PARTIAL_OUTPUT && ping -n 30 127.0.0.1"
	}
	path, args := testShell(t, line)

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:          path,
		Args:          args,
		Timeout:       100 * time.Millisecond,
		KeepOnTimeout: true,
	})
	if live != nil {
		defer live.Kill()
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !strings.Contains(res.Stdout, "PARTIAL_OUTPUT") {
		t.Fatalf("result lost the partial output: %q", res.Stdout)
	}
	if live == nil {
		t.Fatal("KeepOnTimeout must return the live child")
	}
	if !strings.Contains(live.Output(), "PARTIAL_OUTPUT") {
		t.Fatalf("live child lost the partial output: %q", live.Output())
	}
}
