package proc

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSwitchWriterHoldsIncompleteRune guards the detached spawn path's UTF-8
// carry: exec reads a child's output in arbitrary chunks, so the last bytes of
// a read can be the prefix of a multi-byte rune. Callers of Output (notably
// RunWithTimeout when it promotes a slow child) must never observe a dangling
// sequence that decodes as U+FFFD. Run has carried runes since its capture was
// introduced; the writer behind StartDetached/RunWithTimeout did not.
func TestSwitchWriterHoldsIncompleteRune(t *testing.T) {
	out := newSwitchWriter()

	// "prefix\n" followed by the first two bytes of 中 (E4 B8 AD).
	if _, err := out.Write([]byte("prefix\n\xe4\xb8")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := out.Captured()
	if !utf8.ValidString(got) {
		t.Fatalf("Captured() = %q is not valid UTF-8: a split rune leaked", got)
	}
	if got != "prefix\n" {
		t.Fatalf("Captured() = %q, want the complete prefix only", got)
	}

	// The held-back bytes are not lost: once the final byte arrives the whole
	// rune is emitted, and Flush at EOF changes nothing further.
	if _, err := out.Write([]byte{0xAD}); err != nil {
		t.Fatalf("Write final byte: %v", err)
	}
	out.flush()
	if got := out.Captured(); got != "prefix\n中" {
		t.Fatalf("Captured() = %q, want %q", got, "prefix\n中")
	}
}

// TestSwitchWriterHandoffFlushesPendingRune makes sure promoting a running
// child to a log cannot drop a rune that was still incomplete at that instant.
func TestSwitchWriterHandoffFlushesPendingRune(t *testing.T) {
	out := newSwitchWriter()
	_, _ = out.Write([]byte("a\xe4\xb8")) // "a" plus the first two bytes of 中

	var dst bytes.Buffer
	out.Handoff(&dst)
	if got := dst.String(); got != "a\xe4\xb8" {
		t.Fatalf("handoff = %q, want the captured prefix and dangling bytes verbatim", got)
	}

	// After the swap, writes still carry incomplete runes, now into the log.
	_, _ = out.Write([]byte{0xAD})
	out.flush()
	if got := dst.String(); got != "a中" {
		t.Fatalf("log = %q, want %q", got, "a中")
	}
}

const detachedUTF8Marker = "ARGENT_PROC_DETACHED_UTF8_HELPER"

// TestDetachedUTF8Helper is a re-executed test binary that writes a rune in
// two pieces and waits for the parent to release the second piece. It lets the
// parent observe output while a rune is genuinely split across pipe reads.
func TestDetachedUTF8Helper(t *testing.T) {
	if os.Getenv(detachedUTF8Marker) == "" {
		return
	}
	_, _ = os.Stdout.Write([]byte("detached-utf8-ready\n"))
	_, _ = os.Stdout.Write([]byte{0xE4, 0xB8}) // prefix of 中
	var b [1]byte
	_, _ = os.Stdin.Read(b[:]) // wait until the parent has inspected Output
	_, _ = os.Stdout.Write([]byte{0xAD})
}

// TestDetachedOutputHoldsSplitRuneUntilComplete exercises the real OS path:
// a child blocked mid-rune must not make Process.Output invalid UTF-8, and the
// full rune must appear once the child finishes.
func TestDetachedOutputHoldsSplitRuneUntilComplete(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	p, err := StartDetached(Command{
		Path:  os.Args[0],
		Args:  []string{"-test.run=^TestDetachedUTF8Helper$"},
		Env:   []string{detachedUTF8Marker + "=1"},
		Stdin: pr,
	})
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(p.Output(), "detached-utf8-ready\n") {
			break
		}
		if time.Now().After(deadline) {
			_, _ = pw.Write([]byte("x"))
			t.Fatal("helper never produced its ready marker")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mid := p.Output()
	if !utf8.ValidString(mid) {
		t.Errorf("Output() mid-rune = %q, want valid UTF-8", mid)
	}
	if strings.ContainsRune(mid, utf8.RuneError) {
		t.Errorf("Output() mid-rune = %q, want no U+FFFD", mid)
	}

	_, _ = pw.Write([]byte("x"))
	if code := p.Wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out := p.Output(); !strings.Contains(out, "中") {
		t.Fatalf("final output = %q, want the completed rune", out)
	}
}
