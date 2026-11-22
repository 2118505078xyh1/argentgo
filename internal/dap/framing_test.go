package dap

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestWriteReadFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"seq":1,"type":"request","command":"initialize"}`)
	var buf bytes.Buffer
	if err := writeFrame(&buf, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	want := "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n" + string(payload)
	if buf.String() != want {
		t.Fatalf("frame = %q, want %q", buf.String(), want)
	}
	got, err := readFrame(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

func TestReadFrameCaseInsensitiveHeader(t *testing.T) {
	frame := "content-length: 2\r\nX-Custom: ignored\r\n\r\n{}"
	got, err := readFrame(bufio.NewReader(strings.NewReader(frame)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("payload = %q", got)
	}
}

func TestReadFrameRejectsMissingAndTooLarge(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader("\r\n{}"))); err != ErrNoContentLength {
		t.Fatalf("missing header err = %v, want ErrNoContentLength", err)
	}
	big := "Content-Length: " + strconv.Itoa(maxMessageSize+1) + "\r\n\r\n"
	if _, err := readFrame(bufio.NewReader(strings.NewReader(big))); err != ErrMessageTooLarge {
		t.Fatalf("too large err = %v, want ErrMessageTooLarge", err)
	}
}
