package mcp

import (
	"bytes"
	"testing"
)

func TestRingBufferLargeWriteStaysBounded(t *testing.T) {
	const limit = 4096
	r := &ringBuffer{limit: limit}

	payload := bytes.Repeat([]byte("x"), 1<<20)
	tail := []byte("---end-of-stderr---")
	copy(payload[len(payload)-len(tail):], tail)

	n, err := r.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if len(r.buf) > limit || cap(r.buf) > limit {
		t.Fatalf("buffer exceeded limit: len=%d cap=%d limit=%d", len(r.buf), cap(r.buf), limit)
	}
	want := string(payload[len(payload)-limit:])
	if got := r.String(); got != want {
		t.Fatalf("String() kept the wrong tail:\n got %q\nwant %q", got, want)
	}
}

func TestRingBufferByteByByteWriteStaysBounded(t *testing.T) {
	const limit = 1024
	const total = 100 * 1024
	r := &ringBuffer{limit: limit}

	for i := 0; i < total; i++ {
		if n, err := r.Write([]byte{byte('a' + i%26)}); err != nil || n != 1 {
			t.Fatalf("Write(byte %d) = (%d, %v)", i, n, err)
		}
	}
	if len(r.buf) > limit || cap(r.buf) > limit {
		t.Fatalf("buffer exceeded limit: len=%d cap=%d limit=%d", len(r.buf), cap(r.buf), limit)
	}

	want := make([]byte, limit)
	for i := range want {
		want[i] = byte('a' + (total-limit+i)%26)
	}
	if got := r.String(); got != string(want) {
		t.Fatalf("String() kept the wrong tail:\n got %q\nwant %q", got, want)
	}
}

func TestRingBufferDefaultLimit(t *testing.T) {
	r := &ringBuffer{}
	payload := bytes.Repeat([]byte("z"), stderrRingBytes+100)
	_, _ = r.Write(payload)

	if len(r.buf) != stderrRingBytes || cap(r.buf) != stderrRingBytes {
		t.Fatalf("buffer = (len %d, cap %d), want %d", len(r.buf), cap(r.buf), stderrRingBytes)
	}
	if got, want := r.String(), string(payload[len(payload)-stderrRingBytes:]); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestRingBufferKeepsLastBytesAcrossWrites(t *testing.T) {
	r := &ringBuffer{limit: 8}
	_, _ = r.Write([]byte("hello "))
	_, _ = r.Write([]byte("world"))

	if got, want := r.String(), "lo world"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
