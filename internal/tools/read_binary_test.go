package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLooksLikeBinary 覆盖二进制判定的命中与不误判边界。
func TestLooksLikeBinary(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty file", []byte{}, false},
		{"nil data", nil, false},
		{"plain ASCII text", []byte("hello world"), false},
		{"normal text with newline and tab", []byte("a\tb\nc\r\nd\ve\ff\n"), false},
		{"CJK text", []byte("你好世界，这是中文文本"), false},
		{"emoji text", []byte("hi 😀🎉🚀 emoji"), false},
		{"mixed UTF-8 prose", []byte("line 1: café 中文 😀\nline 2: naïve\n"), false},
		{"single NUL in text", []byte{'a', 'b', 0, 'c', 'd'}, true},
		{"valid UTF-8 with NUL", []byte("你好\x00世界"), true},
		{"pure ASCII control bytes", []byte("\x01\x02\x03\x04\x05\x06\x07\x08\x0e\x0f"), true},
		{"dense control bytes below threshold", []byte("a\x01bcdefghij"), false},
		{"dense DEL bytes", []byte("\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f"), true},
		{"invalid UTF-8 with NUL", []byte{0xff, 0xfe, 0x00, 0x80}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeBinary(tc.data); got != tc.want {
				t.Fatalf("looksLikeBinary(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// TestReadRejectsBinaryFile 验证接线：读工具把二进制文件变成
// 带 errBinaryFile 的类型化错误，而不是把字节塞进上下文。
func TestReadRejectsBinaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blob.bin")
	// 合法的 UTF-8 字节里夹带 NUL——正是旧实现会漏判的情形。
	if err := os.WriteFile(path, []byte("PNG\x00\x01\x02binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"blob.bin"}`))
	if err == nil {
		t.Fatal("read of a binary file must fail")
	}
	if !errors.Is(err, errBinaryFile) {
		t.Fatalf("err = %v, want errors.Is(err, errBinaryFile)", err)
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("err = %q, want a model-readable binary hint", err.Error())
	}
	if !strings.Contains(err.Error(), "blob.bin") {
		t.Fatalf("err = %q, want the file path in the message", err.Error())
	}
}

// TestReadPureControlFileRejected 验证纯 ASCII 控制字节文件也被拒绝。
func TestReadPureControlFileRejected(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ctrl.bin")
	data := strings.Repeat("\x01\x02\x03\x04", 32)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"ctrl.bin"}`))
	if !errors.Is(err, errBinaryFile) {
		t.Fatalf("err = %v, want errors.Is(err, errBinaryFile)", err)
	}
}

// TestReadStillAllowsUnicode 验证接线没有误伤正常多语言文本。
func TestReadStillAllowsUnicode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	content := "中文\twith tab\n😀 emoji\r\nfinal line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"note.txt"}`))
	if err != nil {
		t.Fatalf("read of normal Unicode text failed: %v", err)
	}
	if out != content {
		t.Fatalf("read = %q, want %q", out, content)
	}
}
