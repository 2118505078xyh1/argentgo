// Package lsp is a minimal Language Server Protocol client: stdio JSON-RPC with
// Content-Length framing, diagnostics after edits, and definition/hover lookups.
//
// The client owns one child process and never guesses at language semantics; it
// starts the server the caller names, keeps documents synchronized, and hands
// the model plain values.
package lsp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Framing limits. A language server that exceeds them is treated as broken
// rather than allowed to exhaust memory.
const (
	maxHeaderLine  = 8 << 10
	maxMessageSize = 64 << 20
)

// Framing errors.
var (
	// ErrNoContentLength reports a frame without the mandatory header.
	ErrNoContentLength = errors.New("lsp: frame has no Content-Length header")
	// ErrMessageTooLarge reports a frame beyond maxMessageSize.
	ErrMessageTooLarge = errors.New("lsp: frame is too large")
)

// writeFrame writes one framed JSON body.
func writeFrame(w io.Writer, payload []byte) error {
	header := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("lsp: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("lsp: write body: %w", err)
	}
	return nil
}

// readFrame reads one framed JSON body. It returns io.EOF at a clean end of
// stream and an error for truncated or malformed frames.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line == "" {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lsp: read header: %w", err)
		}
		if len(line) > maxHeaderLine {
			return nil, fmt.Errorf("%w: header line over %d bytes", ErrMessageTooLarge, maxHeaderLine)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "content-length") {
			continue
		}
		size, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("lsp: invalid Content-Length %q", value)
		}
		length = size
	}
	if length < 0 {
		return nil, ErrNoContentLength
	}
	if length > maxMessageSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrMessageTooLarge, length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lsp: read body: %w", err)
	}
	return body, nil
}
