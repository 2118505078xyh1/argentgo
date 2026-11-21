package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStripUTF8BOM 验证 BOM 只剥一次、不改入参、无 BOM 时逐字节不变。
func TestStripUTF8BOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	body := []byte("中文首行\n次行")

	input := append(append([]byte{}, bom...), body...)
	original := append([]byte{}, input...)
	got := stripUTF8BOM(input)
	if !bytes.Equal(got, body) {
		t.Fatalf("stripUTF8BOM = %q, want %q", got, body)
	}
	if !bytes.Equal(input, original) {
		t.Fatalf("入参被修改: %q, want %q", input, original)
	}

	plain := []byte("no bom here")
	if got := stripUTF8BOM(plain); !bytes.Equal(got, plain) {
		t.Fatalf("no-BOM data changed: %q", got)
	}
	if got := stripUTF8BOM(nil); len(got) != 0 {
		t.Fatalf("nil data = %q, want empty", got)
	}

	// 连续两个 BOM 只剥掉第一个，避免吞掉正文内容。
	double := append(append([]byte{}, bom...), input...)
	wantOnce := append(append([]byte{}, bom...), body...)
	if got := stripUTF8BOM(double); !bytes.Equal(got, wantOnce) {
		t.Fatalf("double BOM = %q, want %q", got, wantOnce)
	}
}

// TestReadStripsUTF8BOM 验证读取流程把 BOM 从首行移除，CJK 正文完整。
func TestReadStripsUTF8BOM(t *testing.T) {
	root := t.TempDir()
	content := "中文首行\r\n次行 with BOM\n末行😀"
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(content)...)
	if err := os.WriteFile(filepath.Join(root, "bom.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"bom.txt"}`))
	if err != nil {
		t.Fatalf("read BOM file: %v", err)
	}
	if out != content {
		t.Fatalf("read = %q, want %q", out, content)
	}
	if strings.ContainsRune(out, '\ufeff') {
		t.Fatalf("read result still contains BOM rune: %q", out)
	}
}

// TestReadWithoutBOMByteIdentical 验证无 BOM 文件逐字节不变。
func TestReadWithoutBOMByteIdentical(t *testing.T) {
	root := t.TempDir()
	content := "line 1: café 中文 😀\nline 2\n"
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"plain.txt"}`))
	if err != nil {
		t.Fatalf("read plain file: %v", err)
	}
	if out != content {
		t.Fatalf("read = %q, want %q", out, content)
	}
}

// TestReadWithBOMStillRejectsBinary 验证 BOM 不会绕过二进制拒绝。
func TestReadWithBOMStillRejectsBinary(t *testing.T) {
	root := t.TempDir()
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("binary\x00data")...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"blob.bin"}`))
	if !errors.Is(err, errBinaryFile) {
		t.Fatalf("err = %v, want errBinaryFile", err)
	}
}

// TestReadWithBOMStillRejectsGBK 验证 GBK 文件仍显式报非 UTF-8 错误。
func TestReadWithBOMStillRejectsGBK(t *testing.T) {
	root := t.TempDir()
	// "中文" 的 GBK 编码：D6 D0 CE C4。
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	if err := os.WriteFile(filepath.Join(root, "gbk.txt"), gbk, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"gbk.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "not UTF-8") {
		t.Fatalf("err = %v, want an explicit not-UTF-8 error", err)
	}
}
