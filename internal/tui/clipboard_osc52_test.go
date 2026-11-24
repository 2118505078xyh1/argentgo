//go:build !windows

package tui

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestOSC52Disabled(t *testing.T) {
	if !osc52Disabled(func(string) string { return "dumb" }) {
		t.Fatal("TERM=dumb must disable OSC 52")
	}
	if !osc52Disabled(func(string) string { return " DUMB " }) {
		t.Fatal("TERM=dumb (trimmed/case) must disable")
	}
	if osc52Disabled(func(string) string { return "xterm-256color" }) {
		t.Fatal("normal TERM must allow OSC 52")
	}
	// nil env 走 os.Getenv：在最常见情况下（非 dumb）应允许。
	// 用 t.Setenv 固定环境，避免 CI 上 TERM=dumb 造成偶发失败。
	t.Setenv("TERM", "xterm")
	if osc52Disabled(nil) {
		t.Fatal("nil env with TERM=xterm must allow OSC 52")
	}
}

func TestWriteClipboardOSC52(t *testing.T) {
	// 注入内存 writer，避免污染真实 stdout。
	var buf bytes.Buffer
	osc52Mu.Lock()
	prev := osc52Writer
	osc52Writer = &buf
	osc52Mu.Unlock()
	defer func() {
		osc52Mu.Lock()
		osc52Writer = prev
		osc52Mu.Unlock()
	}()

	text := "hello 中文 ✅"
	if err := writeClipboard(text); err != nil {
		t.Fatalf("writeClipboard: %v", err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\x07") {
		t.Fatalf("not an OSC 52 sequence: %q", got)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != text {
		t.Fatalf("clipboard payload = %q, want %q", decoded, text)
	}
}

func TestWriteClipboardDisabledOnDumbTerm(t *testing.T) {
	// TERM=dumb 时应完全不输出 OSC 52 序列。
	t.Setenv("TERM", "dumb")
	var buf bytes.Buffer
	osc52Mu.Lock()
	prev := osc52Writer
	osc52Writer = &buf
	osc52Mu.Unlock()
	defer func() {
		osc52Mu.Lock()
		osc52Writer = prev
		osc52Mu.Unlock()
	}()
	if err := writeClipboard("anything"); err != nil {
		t.Fatalf("writeClipboard: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("dumb TERM must emit nothing, got %q", buf.String())
	}
}
