package lsp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	var buffer bytes.Buffer
	if err := writeFrame(&buffer, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	want := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	if !strings.HasPrefix(buffer.String(), want) {
		t.Fatalf("header = %q, want prefix %q", buffer.String(), want)
	}

	body, err := readFrame(bufio.NewReader(&buffer))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %q", body)
	}
}

func TestReadFrameHandlesBackToBackAndExtraHeaders(t *testing.T) {
	var buffer bytes.Buffer
	buffer.WriteString("Content-Length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}")
	buffer.WriteString("Content-Length: 4\r\n\r\nnull")

	reader := bufio.NewReader(&buffer)
	first, err := readFrame(reader)
	if err != nil || string(first) != "{}" {
		t.Fatalf("first frame = %q, %v", first, err)
	}
	second, err := readFrame(reader)
	if err != nil || string(second) != "null" {
		t.Fatalf("second frame = %q, %v", second, err)
	}
	if _, err := readFrame(reader); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream error = %v, want io.EOF", err)
	}
}

func TestReadFrameRejectsMalformedFrames(t *testing.T) {
	cases := map[string]string{
		"missing header":       "\r\n{}",
		"invalid length":       "Content-Length: nope\r\n\r\n{}",
		"negative length":      "Content-Length: -2\r\n\r\n{}",
		"unreasonably large":   "Content-Length: 999999999999\r\n\r\n{}",
		"truncated body":       "Content-Length: 10\r\n\r\n{}",
		"header line too long": "Content-Length: " + strings.Repeat("0", maxHeaderLine) + "\r\n\r\n{}",
	}
	for label, raw := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
			if err == nil {
				t.Fatalf("readFrame accepted %s", label)
			}
		})
	}
}

// TestReadFrameBoundsHeaderLineWithoutNewline verifies the size guard applies
// before the whole line is buffered. ReadString used to scan an unbounded
// slice looking for '\n', so a header line with no terminator could exhaust
// memory before the limit was checked.
func TestReadFrameBoundsHeaderLineWithoutNewline(t *testing.T) {
	cases := map[string]string{
		"over limit, no newline": "Content-Length: " + strings.Repeat("1", maxHeaderLine),
		"over reader buffer":     "Content-Length: " + strings.Repeat("1", 128<<10),
	}
	for label, raw := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
			if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("readFrame error = %v, want ErrMessageTooLarge", err)
			}
		})
	}
}

func TestReadFrameReportsMissingContentLength(t *testing.T) {
	_, err := readFrame(bufio.NewReader(strings.NewReader("Content-Type: application/json\r\n\r\n{}")))
	if !errors.Is(err, ErrNoContentLength) {
		t.Fatalf("error = %v, want ErrNoContentLength", err)
	}
}
