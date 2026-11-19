package launcher

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// inject swaps both seams for the duration of a test and restores the
// previously selected browser name on cleanup.
func inject(t *testing.T, lp func(string) (string, error), st func(*exec.Cmd) error) {
	t.Helper()
	oldPath, oldStart, oldName := lookPath, start, Name()
	t.Cleanup(func() {
		lookPath, start = oldPath, oldStart
		setName(oldName)
	})
	lookPath, start = lp, st
}

func noneFound(string) (string, error) { return "", errors.New("not found") }

func TestAppArgsUseDedicatedProfileFlags(t *testing.T) {
	want := []string{
		"--app=http://localhost:1234/ui",
		"--user-data-dir=C:\\argent\\profile",
		"--no-first-run",
		"--window-size=1280,860",
	}
	if got := appArgs("http://localhost:1234/ui", `C:\argent\profile`); !reflect.DeepEqual(got, want) {
		t.Fatalf("appArgs() = %q, want %q", got, want)
	}
}

func TestCandidatesAreNonEmpty(t *testing.T) {
	if len(Candidates()) == 0 {
		t.Fatal("Candidates() returned no entries on this platform")
	}
}

func TestNameDefaultsToBrowser(t *testing.T) {
	setName("browser")
	if got := Name(); got != "browser" {
		t.Fatalf("Name() = %q, want browser", got)
	}
}

func TestOpenFallsBackToDefaultOpener(t *testing.T) {
	var captured *exec.Cmd
	inject(t, noneFound, func(cmd *exec.Cmd) error {
		captured = cmd
		return nil
	})

	err := Open(context.Background(), "http://localhost:9", "ignored")
	if !errors.Is(err, ErrFallback) {
		t.Fatalf("Open() error = %v, want ErrFallback", err)
	}
	if captured == nil {
		t.Fatal("start was not called for the fallback opener")
	}
	want := append([]string{defaultOpener()}, defaultOpenerArgs("http://localhost:9")...)
	if !reflect.DeepEqual(captured.Args, want) {
		t.Fatalf("fallback args = %q, want %q", captured.Args, want)
	}
	if got := Name(); got != "browser" {
		t.Fatalf("Name() = %q, want browser after fallback", got)
	}
}

func TestOpenReturnsAsSoonAsProcessIsStarted(t *testing.T) {
	called := make(chan struct{}, 1)
	inject(t, noneFound, func(*exec.Cmd) error {
		called <- struct{}{}
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- Open(context.Background(), "http://localhost:9", "profile")
	}()

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("start was never called")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrFallback) {
			t.Fatalf("Open() error = %v, want ErrFallback", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open blocked after the process was spawned")
	}
}

func TestOpenReturnsCancelledContextError(t *testing.T) {
	inject(t, noneFound, func(*exec.Cmd) error {
		t.Fatal("start must not run for a cancelled context")
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Open(ctx, "http://localhost:9", "profile"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", err)
	}
}

func TestOpenReportsStartFailure(t *testing.T) {
	boom := errors.New("spawn failed")
	inject(t, func(string) (string, error) {
		return "browser", nil
	}, func(*exec.Cmd) error { return boom })

	err := Open(context.Background(), "http://localhost:9", "profile")
	if !errors.Is(err, boom) {
		t.Fatalf("Open() error = %v, want wrapped spawn failure", err)
	}
}
