package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

type stringerResult string

func (s stringerResult) String() string { return string(s) }

func TestNormalizeToolResultAcceptsSupportedTypes(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want string
	}{
		{"string", "hello", "hello"},
		{"byte slice", []byte("bytes"), "bytes"},
		{"raw message", json.RawMessage(`{"ok":true}`), `{"ok":true}`},
		{"stringer", stringerResult("stringer"), "stringer"},
		{
			"struct falls back to json",
			struct {
				Name string `json:"name"`
			}{Name: "struct"},
			`{"name":"struct"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeToolResult(tc.raw)
			if err != nil {
				t.Fatalf("NormalizeToolResult: %v", err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeToolResult(%T) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeToolResultNilUsesSentinel(t *testing.T) {
	got, err := NormalizeToolResult(nil)
	if err != nil {
		t.Fatalf("NormalizeToolResult(nil): %v", err)
	}
	if got != emptyToolResultSentinel {
		t.Fatalf("nil result = %q, want %q", got, emptyToolResultSentinel)
	}
}

func TestNormalizeToolResultEmptyAfterCleaningUsesSentinel(t *testing.T) {
	got, err := NormalizeToolResult("\x1b[31m\x1b[0m\x00\x07")
	if err != nil {
		t.Fatalf("NormalizeToolResult: %v", err)
	}
	if got != emptyToolResultSentinel {
		t.Fatalf("control-only result = %q, want %q", got, emptyToolResultSentinel)
	}
}

func TestNormalizeToolResultTruncatesOversizedUTF8OnRuneBoundary(t *testing.T) {
	// 30,000 CJK runes = 90,000 bytes, well over the 64 KiB cap.
	raw := strings.Repeat("界", 30000)
	got, err := NormalizeToolResult(raw)
	if err != nil {
		t.Fatalf("NormalizeToolResult: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated result is not valid UTF-8: %q", got)
	}
	if len(got) > maxNormalizedResultBytes {
		t.Fatalf("result is %d bytes, want <= %d", len(got), maxNormalizedResultBytes)
	}
	if !strings.HasSuffix(got, resultEllipsis) {
		t.Fatalf("oversized result should end with an ellipsis: %q", got)
	}
}

func TestNormalizeToolResultStripsANSIFromBytes(t *testing.T) {
	raw := []byte("\x1b[31merr\x1b[0m\x00\x07: \nboom")
	got, err := NormalizeToolResult(raw)
	if err != nil {
		t.Fatalf("NormalizeToolResult: %v", err)
	}
	const want = "err: \nboom"
	if got != want {
		t.Fatalf("NormalizeToolResult = %q, want %q", got, want)
	}
}

func TestNormalizeToolResultKeepsTextOnMarshalError(t *testing.T) {
	got, err := NormalizeToolResult(make(chan int))
	if err == nil {
		t.Fatal("expected an error for an unmarshalable value")
	}
	if got == "" {
		t.Fatal("an error result must still carry readable text")
	}
	if !strings.Contains(got, "unrenderable") {
		t.Fatalf("error result text = %q, want a readable sentinel", got)
	}
}
