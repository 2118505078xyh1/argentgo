package proc

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

func TestClassifyCancel(t *testing.T) {
	wrappedDeadline := fmt.Errorf("proc: %q exceeded %s: %w", "sleep", 500*time.Millisecond, context.DeadlineExceeded)
	wrappedCanceled := fmt.Errorf("proc: %q: %w", "sleep", context.Canceled)
	doublyWrapped := fmt.Errorf("outer: %w", wrappedDeadline)
	plain := errors.New("proc: spawn failed")

	tests := []struct {
		name string
		err  error
		want CancelReason
	}{
		{"nil is no cancellation", nil, CancelNone},
		{"bare deadline", context.DeadlineExceeded, CancelTimeout},
		{"bare canceled", context.Canceled, CancelUser},
		{"wrapped deadline", wrappedDeadline, CancelTimeout},
		{"wrapped canceled", wrappedCanceled, CancelUser},
		{"multiply wrapped deadline", doublyWrapped, CancelTimeout},
		{"unrelated error", plain, CancelUnknown},
		{"spawn style error", fmt.Errorf("proc: spawn %q: %w", "missing", errors.New("no such file")), CancelUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyCancel(tt.err); got != tt.want {
				t.Fatalf("ClassifyCancel(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

func TestCancelReasonString(t *testing.T) {
	tests := []struct {
		reason CancelReason
		want   string
	}{
		{CancelNone, "none"},
		{CancelTimeout, "timeout"},
		{CancelUser, "user"},
		{CancelSignal, "signal"},
		{CancelUnknown, "unknown"},
		{CancelReason(99), "CancelReason(99)"},
	}
	for _, tt := range tests {
		if got := tt.reason.String(); got != tt.want {
			t.Fatalf("CancelReason(%d).String() = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

// longRunningLine is a child that outlives both test timeouts so the only
// reason it stops is the one the test arranges.
func longRunningLine() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1"
	}
	return "sleep 30"
}

func TestClassifyCancelOnRunTimeout(t *testing.T) {
	path, args := testShell(t, longRunningLine())
	_, err := Run(context.Background(), Command{
		Path:     path,
		Args:     args,
		Timeout:  500 * time.Millisecond,
		KillTree: true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run timeout err = %v, want DeadlineExceeded", err)
	}
	if got := ClassifyCancel(err); got != CancelTimeout {
		t.Fatalf("ClassifyCancel(timeout err) = %s, want timeout", got)
	}
}

func TestClassifyCancelOnRunUserCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	path, args := testShell(t, longRunningLine())
	_, err := Run(ctx, Command{Path: path, Args: args, KillTree: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run cancel err = %v, want Canceled", err)
	}
	if got := ClassifyCancel(err); got != CancelUser {
		t.Fatalf("ClassifyCancel(cancel err) = %s, want user", got)
	}
}
