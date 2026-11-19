package proc

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUTF8CarryWriterJoinsRuneSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	// 中 is E4 B8 AD; the first Write holds back an incomplete prefix.
	if n, err := cw.Write([]byte{0xE4, 0xB8}); n != 2 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (2, nil)", n, err)
	}
	if buf.Len() != 0 {
		t.Fatalf("incomplete prefix must be held back, got %x", buf.Bytes())
	}
	if n, err := cw.Write([]byte{0xAD}); n != 1 || err != nil {
		t.Fatalf("second Write = (%d, %v), want (1, nil)", n, err)
	}
	// The completed rune reaches the underlying writer before any Flush.
	if got := buf.String(); got != "中" {
		t.Fatalf("output = %q, want 中", got)
	}
	cw.Flush()
	if got := buf.String(); got != "中" {
		t.Fatalf("Flush must not duplicate completed data, got %q", got)
	}
}

func TestUTF8CarryWriterPassesASCIIThroughUnchanged(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	in := []byte("plain ASCII output 1234!@#\n")
	if n, err := cw.Write(in); n != len(in) || err != nil {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(in))
	}
	cw.Flush()
	if got := buf.String(); got != string(in) {
		t.Fatalf("output = %q, want %q", got, in)
	}
	if len(cw.carry) != 0 {
		t.Fatalf("carry = %x after Flush, want empty", cw.carry)
	}
}

func TestUTF8CarryWriterMixedTextAtEveryBoundary(t *testing.T) {
	// ASCII, 3-byte CJK, 4-byte emoji and a trailing ASCII tail.
	const want = "hello中文world Go语言🌍end"

	// Two-way split at every possible byte boundary.
	for i := 0; i <= len(want); i++ {
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		if _, err := cw.Write([]byte(want[:i])); err != nil {
			t.Fatalf("split %d first Write: %v", i, err)
		}
		if _, err := cw.Write([]byte(want[i:])); err != nil {
			t.Fatalf("split %d second Write: %v", i, err)
		}
		cw.Flush()
		if got := buf.String(); got != want {
			t.Fatalf("split %d: output = %q, want %q", i, got, want)
		}
	}

	// One byte at a time: every rune is assembled from maximal fragments.
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		for i := 0; i < len(want); i++ {
			if _, err := cw.Write([]byte(want[i : i+1])); err != nil {
				t.Fatalf("byte %d Write: %v", i, err)
			}
		}
		cw.Flush()
		if got := buf.String(); got != want {
			t.Fatalf("byte-at-a-time output = %q, want %q", got, want)
		}
	}
}

func TestUTF8CarryWriterFlushDanglingSequence(t *testing.T) {
	var buf bytes.Buffer
	cw := newUTF8CarryWriter(&buf)

	// A leading byte plus one continuation: still missing the final byte.
	if _, err := cw.Write([]byte{0xE4, 0xB8}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	cw.Flush() // must not panic
	if want := []byte{0xE4, 0xB8}; !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("Flush output = %x, want %x (written verbatim)", buf.Bytes(), want)
	}
	// string() copies raw bytes verbatim; U+FFFD appears only when the bytes
	// are decoded rune by rune downstream (JSON, range, []rune, ...).
	if got := []rune(buf.String()); len(got) != 2 || got[0] != utf8.RuneError || got[1] != utf8.RuneError {
		t.Fatalf("rune decode = %v, want two U+FFFD", got)
	}
	// Flush is idempotent.
	cw.Flush()
	if !bytes.Equal(buf.Bytes(), []byte{0xE4, 0xB8}) {
		t.Fatalf("second Flush changed output to %x", buf.Bytes())
	}
}

func TestUTF8CarryWriterCapsCarry(t *testing.T) {
	// With the default cap, malformed garbage must never grow the carry beyond
	// the largest possible incomplete sequence (utf8.UTFMax-1 bytes).
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		garbage := bytes.Repeat([]byte{0xF0, 0x9F, 0x98, 0x80, 0x80, 0xC0}, 64)
		for i := 0; i < len(garbage); i++ {
			if _, err := cw.Write(garbage[i : i+1]); err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
			if len(cw.carry) > maxCarryBytes {
				t.Fatalf("carry grew to %d bytes, cap is %d", len(cw.carry), maxCarryBytes)
			}
		}
		cw.Flush()
	}

	// Force a cap smaller than a natural incomplete tail to exercise the
	// clamp: the excess bytes are written through rather than lost or held.
	{
		var buf bytes.Buffer
		cw := newUTF8CarryWriter(&buf)
		cw.maxCarry = 1
		in := []byte{0xF0, 0x9F} // 4-byte lead with only one continuation
		if _, err := cw.Write(in); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if len(cw.carry) != 1 {
			t.Fatalf("carry = %x, want exactly 1 byte", cw.carry)
		}
		// Total bytes written to the underlying writer plus the retained carry
		// equals the input: the clamp dropped nothing.
		if buf.Len()+len(cw.carry) != len(in) {
			t.Fatalf("lost bytes: buf=%d carry=%d input=%d", buf.Len(), len(cw.carry), len(in))
		}
		cw.Flush()
		if buf.Len() != len(in) {
			t.Fatalf("after Flush buf has %d bytes, want %d", buf.Len(), len(in))
		}
	}
}

func TestUTF8CarryWriterWithCappedBufferPreservesCap(t *testing.T) {
	old := MaxOutputBytes
	MaxOutputBytes = 40
	defer func() { MaxOutputBytes = old }()

	cb := &cappedBuffer{max: MaxOutputBytes}
	cw := newUTF8CarryWriter(cb)
	// Repeated multi-byte runes written one byte at a time exceed the cap.
	chunk := []byte("中文测试输出")
	for i := 0; i < 16; i++ {
		for j := 0; j < len(chunk); j++ {
			_, _ = cw.Write(chunk[j : j+1])
		}
	}
	cw.Flush()

	if !cb.dropped {
		t.Fatal("output over the cap must be truncated")
	}
	if !strings.Contains(cb.String(), truncatedMarker) {
		t.Fatalf("no truncation marker in %q", cb.String())
	}
	head, tail, found := strings.Cut(cb.String(), truncatedMarker)
	if !found || head == "" || tail == "" {
		t.Fatalf("head/tail truncation broken: %q", cb.String())
	}
	if !strings.Contains(cb.String(), "中") {
		t.Fatalf("CJK rune mangled by truncation: %q", cb.String())
	}
}
