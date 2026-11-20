package mcp

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// readLoopHarness builds a transport whose readLoop can run without a live
// child process: cmd.Process is nil so finish skips killing, and stdin is a
// pipe writer so finish can close it.
func readLoopHarness() *stdioTransport {
	_, stdin := io.Pipe()
	return &stdioTransport{
		name:    "oversize",
		cmd:     &exec.Cmd{},
		stdin:   stdin,
		waiters: newWaiters(),
		diag:    &ringBuffer{},
		done:    make(chan struct{}),
	}
}

// countingReader records how many bytes the consumer actually pulled from the
// source. It has no cap of its own, so it can expose a consumer that drains an
// unbounded line into memory instead of stopping at the frame limit.
type countingReader struct {
	src  io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += n
	return n, err
}

// TestReadLoopDoesNotDrainOversizeLine is a regression test for the frame cap:
// a server that emits a huge line with no newline must not make readLoop read
// (and buffer) the whole thing before rejecting it. ReadBytes, used previously,
// kept accumulating until EOF, so a malicious server could force memory growth
// far beyond maxLineBytes. A bounded reader stops within one buffer chunk of
// the cap.
func TestReadLoopDoesNotDrainOversizeLine(t *testing.T) {
	huge := append(bytes.Repeat([]byte("x"), maxLineBytes*2), '\n')
	counter := &countingReader{src: bytes.NewReader(huge)}

	tpt := readLoopHarness()
	tpt.readLoop(counter)

	if counter.read > maxLineBytes+256*1024 {
		t.Fatalf("readLoop consumed %d bytes for an oversize frame; it should stop near the %d byte cap",
			counter.read, maxLineBytes)
	}
	if !strings.Contains(tpt.closedError("").Error(), "larger than") {
		t.Fatalf("oversize frame was not reported: %v", tpt.closedError(""))
	}
}
