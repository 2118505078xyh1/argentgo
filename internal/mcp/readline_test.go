package mcp

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBoundedLineSplitsFrames(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("first\nsecond\nthird\n"), 8)
	for _, want := range []string{"first\n", "second\n", "third\n"} {
		line, err := readBoundedLine(reader, 64)
		if err != nil {
			t.Fatalf("readBoundedLine: %v", err)
		}
		if string(line) != want {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}
	if _, err := readBoundedLine(reader, 64); !errors.Is(err, io.EOF) {
		t.Fatalf("after last frame: err = %v, want io.EOF", err)
	}
}

func TestReadBoundedLineReturnsFinalLineAtEOF(t *testing.T) {
	// A final line with no trailing newline is delivered along with io.EOF.
	reader := bufio.NewReaderSize(strings.NewReader("tail"), 4)
	line, err := readBoundedLine(reader, 64)
	if string(line) != "tail" || !errors.Is(err, io.EOF) {
		t.Fatalf("line = %q err = %v, want %q and io.EOF", line, err, "tail")
	}
}

func TestReadBoundedLineRejectsOversizeWithoutDraining(t *testing.T) {
	const limit = 16
	// The reader's buffer is tiny relative to the input, forcing many
	// ErrBufferFull cycles; the helper must stop as soon as the cap is crossed.
	counter := &countingReader{src: strings.NewReader(strings.Repeat("x", 4096))}
	reader := bufio.NewReaderSize(counter, 8)

	line, err := readBoundedLine(reader, limit)
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v, want errLineTooLong", err)
	}
	if len(line) <= limit {
		t.Fatalf("line length = %d, want > %d", len(line), limit)
	}
	// bufio enforces a 16-byte minimum buffer, so a single ReadSlice chunk can
	// be 16 bytes; the cap check tolerates one such chunk of overshoot.
	if counter.read > limit+16 {
		t.Fatalf("read %d bytes, want at most %d (cap %d + one buffer)", counter.read, limit+16, limit)
	}
}
