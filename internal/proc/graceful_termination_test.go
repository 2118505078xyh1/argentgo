//go:build !windows

package proc

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	graceHelperOnEnv   = "ARGENT_PROC_GRACE_HELPER"
	graceHelperModeEnv = "ARGENT_PROC_GRACE_MODE"
	graceHelperFileEnv = "ARGENT_PROC_GRACE_FILE"
)

// TestProcGraceHelper is a re-executed test binary that plays the child under
// test. It is a no-op in the normal test run; the harness sets the marker env
// to turn it into a helper process. The log file is the handshake channel: the
// child appends READY once its signal handling is installed, and GRACEFUL if
// it actually caught SIGTERM before being killed.
func TestProcGraceHelper(t *testing.T) {
	if os.Getenv(graceHelperOnEnv) == "" {
		return
	}
	f, err := os.OpenFile(os.Getenv(graceHelperFileEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	// No defer: every path below ends in os.Exit, which would skip it anyway.
	// os.File writes are unbuffered, so the markers are on disk immediately.

	switch os.Getenv(graceHelperModeEnv) {
	case "graceful":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		_, _ = f.WriteString("READY\n")
		<-ch
		_, _ = f.WriteString("GRACEFUL\n")
		os.Exit(0)
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		_, _ = f.WriteString("READY\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func setCancellationGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := cancellationGracePeriod
	cancellationGracePeriod = d
	t.Cleanup(func() { cancellationGracePeriod = old })
}

type graceRun struct {
	res     Result
	err     error
	elapsed time.Duration
	log     string
}

// runGraceHelper launches the re-executed child, waits for its READY marker so
// the signal handler is known to be installed, then either cancels the context
// (cancel=true) or lets Command.Timeout fire (cancel=false).
func runGraceHelper(t *testing.T, mode string, cancel bool, timeout time.Duration) graceRun {
	t.Helper()
	file := filepath.Join(t.TempDir(), "child.log")
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	cmd := Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProcGraceHelper$"},
		Env: []string{
			graceHelperOnEnv + "=1",
			graceHelperModeEnv + "=" + mode,
			graceHelperFileEnv + "=" + file,
		},
		KillTree: true,
		Timeout:  timeout,
	}

	type outcome struct {
		res     Result
		err     error
		elapsed time.Duration
	}
	out := make(chan outcome, 1)
	go func() {
		started := time.Now()
		res, err := Run(ctx, cmd)
		out <- outcome{res: res, err: err, elapsed: time.Since(started)}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), "READY") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never became ready (log=%q)", b)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if cancel {
		cancelFn()
	}

	select {
	case o := <-out:
		b, _ := os.ReadFile(file)
		return graceRun{res: o.res, err: o.err, elapsed: o.elapsed, log: string(b)}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the helper was signalled")
		return graceRun{}
	}
}

func TestCancelLetsGracefulChildExitBeforeSIGKILL(t *testing.T) {
	// A generous window: the cooperative child should exit in milliseconds, so
	// a fast return proves Run honoured SIGTERM instead of waiting to escalate.
	setCancellationGrace(t, 2*time.Second)

	got := runGraceHelper(t, "graceful", true, 0)

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", got.err)
	}
	if !strings.Contains(got.log, "GRACEFUL") {
		t.Fatalf("child did not catch SIGTERM before the kill; log=%q", got.log)
	}
	if got.res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (graceful exit, not SIGKILL)", got.res.ExitCode)
	}
	if got.elapsed >= time.Second {
		t.Fatalf("Run took %s; a graceful child must exit well within the grace window", got.elapsed)
	}
}

func TestCancelEscalatesToSIGKILLAfterGraceWhenSIGTERMIgnored(t *testing.T) {
	setCancellationGrace(t, 300*time.Millisecond)

	got := runGraceHelper(t, "ignore", true, 0)

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", got.err)
	}
	if strings.Contains(got.log, "GRACEFUL") {
		t.Fatalf("an SIGTERM-ignoring child must not report graceful cleanup: %q", got.log)
	}
	if got.elapsed < 250*time.Millisecond {
		t.Fatalf("Run took %s; it must wait the grace period before escalating to SIGKILL", got.elapsed)
	}
	if got.res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1 (killed by SIGKILL)", got.res.ExitCode)
	}
}

func TestTimeoutKillsImmediatelyWithoutGraceWait(t *testing.T) {
	// The timeout path must ignore the grace window entirely. A long grace
	// period here makes any accidental wait obvious.
	setCancellationGrace(t, 2*time.Second)

	got := runGraceHelper(t, "ignore", false, 150*time.Millisecond)

	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", got.err)
	}
	if got.elapsed >= time.Second {
		t.Fatalf("Run took %s; the timeout path must not wait for the grace period", got.elapsed)
	}
}
