package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLimitMCPOutput(t *testing.T) {
	text := "A" + strings.Repeat("中", 20000) + "B" // CJK 多字节，验证边界安全
	got := limitMCPOutput(text, 20480)
	if len(got) >= len(text) {
		t.Fatal("output not truncated")
	}
	if !strings.Contains(got, "full output:") || !strings.Contains(got, "bytes") {
		t.Fatalf("recovery marker missing: %.80s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("output split a UTF-8 rune")
	}
}

func TestLimitMCPOutputDisabled(t *testing.T) {
	text := strings.Repeat("x", 1000)
	if got := limitMCPOutput(text, 0); got != text {
		t.Fatal("limit<=0 must be no-op")
	}
	if got := limitMCPOutput("short", 20480); got != "short" {
		t.Fatal("under-limit must be no-op")
	}
}
