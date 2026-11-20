package agent

import (
	"strings"
	"testing"
)

// TestLoopDetectionWindowIgnoresOldRepeats verifies that a signature which
// repeated many times but fell out of the sliding window no longer blocks a
// later call with that signature. The full-run counter used to trip here.
func TestLoopDetectionWindowIgnoresOldRepeats(t *testing.T) {
	guard := newDedupeWindow(1, 4)
	// Two identical calls overflow the allowance (max = 1). The guard is queried
	// directly here, so the return values are irrelevant -- only the window
	// contents matter.
	guard.check("read", `{"path":"old"}`)
	guard.check("read", `{"path":"old"}`)

	// Four distinct signatures push both "old" entries out of a size-4 window.
	for _, args := range []string{`{"path":"a"}`, `{"path":"b"}`, `{"path":"c"}`, `{"path":"d"}`} {
		if blocked, msg := guard.check("read", args); blocked {
			t.Fatalf("filler call %s blocked unexpectedly: %s", args, msg)
		}
	}

	if blocked, msg := guard.check("read", `{"path":"old"}`); blocked {
		t.Fatalf("evicted old repeat must not block: %s", msg)
	}
}

// TestLoopDetectionWindowStillBlocksRecentRepeats verifies that the original
// behaviour is preserved inside the window: exceeding max still blocks.
func TestLoopDetectionWindowStillBlocksRecentRepeats(t *testing.T) {
	guard := newDedupeWindow(2, 10)
	for i := 0; i < 2; i++ {
		if blocked, msg := guard.check("read", `{"path":"a"}`); blocked {
			t.Fatalf("call %d blocked too early: %s", i+1, msg)
		}
	}
	blocked, msg := guard.check("read", `{"path": "a"}`)
	if !blocked {
		t.Fatal("third identical call inside the window should be blocked")
	}
	if !strings.Contains(msg, "duplicate call blocked") {
		t.Fatalf("message = %q", msg)
	}
}

// TestLoopDetectionOutputSignatureCatchesDifferentArgsSameOutput verifies the
// second signal: the same tool returning identical output for several distinct
// argument sets is recognised as a pseudo-progress loop, even though no single
// argument signature repeats often enough to trip the argument window.
func TestLoopDetectionOutputSignatureCatchesDifferentArgsSameOutput(t *testing.T) {
	guard := newDedupeWindow(1, 10)
	paths := []string{`{"path":"a"}`, `{"path":"b"}`, `{"path":"c"}`}
	for i, raw := range paths {
		normalized := NormalizeArgs(raw)
		blocked, msg := guard.checkNormalized("read", normalized)
		guard.recordOutput("read", normalized, "identical body")
		if i == len(paths)-1 {
			if !blocked {
				t.Fatal("same output for three different arguments should be blocked")
			}
			if !strings.Contains(msg, "duplicate call blocked") {
				t.Fatalf("message = %q", msg)
			}
		} else if blocked {
			t.Fatalf("call %d blocked too early: %s", i+1, msg)
		}
	}
}

// TestLoopDetectionOutputSignatureNeedsDistinctArgs verifies that repeating the
// same argument set with the same output does not double-count in the output
// window; the argument signature already covers that case.
func TestLoopDetectionOutputSignatureNeedsDistinctArgs(t *testing.T) {
	guard := newDedupeWindow(2, 10)
	normalized := NormalizeArgs(`{"path":"a"}`)
	for i := 0; i < 3; i++ {
		guard.checkNormalized("read", normalized)
		guard.recordOutput("read", normalized, "identical body")
	}
	if guard.outputWouldBlock("read") {
		t.Fatal("one argument set returning the same output must not trip the output signal")
	}
}
