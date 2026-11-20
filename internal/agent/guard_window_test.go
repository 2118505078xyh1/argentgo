package agent

import (
	"strings"
	"testing"
)

// TestLoopDetectionWindowIgnoresOldRepeats 验证一个曾重复多次但已滑出窗口的签名，不再阻塞之后具有该签名的调用。
// 以前的全运行计数器会在这里误触发。
// （接上行）
func TestLoopDetectionWindowIgnoresOldRepeats(t *testing.T) {
	guard := newDedupeWindow(1, 4)
	// 两次相同调用会超出额度（max = 1）。这里直接查询防护器，因此返回值无关紧要——
	// 只有窗口内容重要。
	// （接上行）
	guard.check("read", `{"path":"old"}`)
	guard.check("read", `{"path":"old"}`)

	// 四个不同的签名把两个旧条目挤出大小为 4 的窗口。
	for _, args := range []string{`{"path":"a"}`, `{"path":"b"}`, `{"path":"c"}`, `{"path":"d"}`} {
		if blocked, msg := guard.check("read", args); blocked {
			t.Fatalf("filler call %s blocked unexpectedly: %s", args, msg)
		}
	}

	if blocked, msg := guard.check("read", `{"path":"old"}`); blocked {
		t.Fatalf("evicted old repeat must not block: %s", msg)
	}
}

// TestLoopDetectionWindowStillBlocksRecentRepeats 验证窗口内的原有行为被保留：超过 max 仍会阻断。
// （接上行）
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

// TestLoopDetectionOutputSignatureCatchesDifferentArgsSameOutput 验证第二个信号：同一工具对多个不同参数集返回相同输出，
// 会被识别为伪进展循环，即使没有任何单一参数签名的重复次数足以触发参数窗口。
// （接上行）
// （接上行）
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

// TestLoopDetectionOutputSignatureNeedsDistinctArgs 验证用相同输出重复相同参数集，在输出窗口中不会重复计数；
// 参数签名已经覆盖了那种情况。
// （接上行）
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
