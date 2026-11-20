package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// spillPathInResult 从完整的溢出标记中提取恢复路径。
func spillPathInResult(t *testing.T, result string) string {
	t.Helper()
	i := strings.Index(result, mcpSpillMarkerOpen)
	if i < 0 {
		t.Fatalf("result has no spill marker: %q", result)
	}
	rest := result[i+len(mcpSpillMarkerOpen):]
	const link = "; full output: "
	j := strings.Index(rest, link)
	if j < 0 {
		t.Fatalf("marker has no recovery link: %q", rest)
	}
	pathPart := rest[j+len(link):]
	k := strings.Index(pathPart, mcpSpillMarkerEnd)
	if k < 0 {
		t.Fatalf("unterminated spill marker: %q", pathPart)
	}
	return pathPart[:k]
}

func TestSpillMCPOutputUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)

	text := strings.Repeat("a", 100)
	got, path, spilled := spillMCPOutput(text, 200)
	if spilled {
		t.Fatal("under-threshold output must not spill")
	}
	if path != "" {
		t.Fatalf("under-threshold output returned a path: %q", path)
	}
	if got != text {
		t.Fatalf("under-threshold output changed: got %q, want %q", got, text)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("read dir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("under-threshold output created artifacts: %v", entries)
	}
}

func TestSpillMCPOutputBoundary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)

	const threshold = 300
	at := strings.Repeat("x", threshold)
	if got, path, spilled := spillMCPOutput(at, threshold); spilled || path != "" || got != at {
		t.Fatalf("exactly-at-threshold output must not spill: got=%q path=%q spilled=%v", got, path, spilled)
	}

	over := strings.Repeat("y", threshold+1)
	got, path, spilled := spillMCPOutput(over, threshold)
	if !spilled || path == "" {
		t.Fatalf("one-byte-over output must spill: path=%q spilled=%v", path, spilled)
	}
	if len(got) >= len(over) {
		t.Fatalf("spilled result is not smaller: got %d bytes, want < %d", len(got), len(over))
	}
}

func TestSpillMCPOutputNoDir(t *testing.T) {
	t.Setenv(mcpSpillDirEnv, "")

	text := strings.Repeat("z", 5000)
	got, path, spilled := spillMCPOutput(text, 100)
	if spilled || path != "" {
		t.Fatalf("no configured dir must not spill: path=%q spilled=%v", path, spilled)
	}
	if got != text {
		t.Fatal("no-configured-dir output changed")
	}
	if strings.Contains(got, mcpSpillMarkerOpen) {
		t.Fatal("spill marker leaked with no dir configured")
	}
}

func TestSpillMCPOutputOverThresholdReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)

	text := strings.Repeat("0123456789", 2000) // 20000 字节
	got, path, spilled := spillMCPOutput(text, 1024)
	if !spilled || path == "" {
		t.Fatalf("oversized output must spill: path=%q spilled=%v", path, spilled)
	}
	if !pathWithinDir(dir, path) {
		t.Fatalf("artifact escaped dir: %q", path)
	}
	if !mcpArtifactName(filepath.Base(path)) {
		t.Fatalf("unexpected artifact filename: %q", filepath.Base(path))
	}
	if p := spillPathInResult(t, got); p != path {
		t.Fatalf("marker path %q != returned path %q", p, path)
	}
	// 完整、未修改的输出必须能从产物中恢复。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != text {
		t.Fatalf("artifact does not hold the full output: got %d bytes, want %d", len(data), len(text))
	}
	// 有界视图保留输出的开头和结尾。
	if !strings.HasPrefix(text, got[:strings.Index(got, mcpSpillMarkerOpen)]) {
		t.Fatal("spilled result lost the head window")
	}
	tail := got[strings.Index(got, mcpSpillMarkerEnd)+len(mcpSpillMarkerEnd):]
	if !strings.HasSuffix(text, tail) {
		t.Fatal("spilled result lost the tail window")
	}
}

func TestSpillMCPOutputCJKIsRuneSafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)

	text := strings.Repeat("你好世界🌏", 1000)
	if !utf8.ValidString(text) {
		t.Fatal("test input is not valid UTF-8")
	}
	got, path, spilled := spillMCPOutput(text, 512)
	if !spilled || path == "" {
		t.Fatalf("oversized CJK output must spill: path=%q spilled=%v", path, spilled)
	}

	head := got[:strings.Index(got, mcpSpillMarkerOpen)]
	tail := got[strings.Index(got, mcpSpillMarkerEnd)+len(mcpSpillMarkerEnd):]
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatalf("spill cut inside a UTF-8 rune: head=%q tail=%q", head, tail)
	}
	if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) {
		t.Fatal("rune-safe windows are not a prefix/suffix of the original")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CJK artifact: %v", err)
	}
	if string(data) != text {
		t.Fatal("CJK artifact does not hold the full output")
	}
}

func TestSpillMCPOutputAlreadySpilled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)

	path, err := SaveBlobArtifact(dir, []byte("full output"), ".txt")
	if err != nil {
		t.Fatalf("SaveBlobArtifact: %v", err)
	}
	marker := formatMCPSpillMarker(100, path)
	already := strings.Repeat("a", 2000) + marker + strings.Repeat("b", 2000)

	got, outPath, spilled := spillMCPOutput(already, 1000)
	if spilled || outPath != "" {
		t.Fatalf("intact marker naming a real artifact must not re-spill: path=%q spilled=%v", outPath, spilled)
	}
	if got != already {
		t.Fatal("already-spilled result was modified")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("read dir: %v", err)
	} else if len(entries) != 1 {
		t.Fatalf("re-spill created extra artifacts: %d total", len(entries))
	}
}

func TestCallToolSpillDisabledWithoutDir(t *testing.T) {
	// 即使阈值很小，没有目录也不会溢出：旧输出
	// 被逐字节返回。
	t.Setenv(mcpSpillDirEnv, "")
	t.Setenv(mcpSpillThresholdEnv, "10")

	big := strings.Repeat("data ", 100)
	response := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": big},
	}}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)

	got, err := c.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got != big {
		t.Fatalf("output changed with spill disabled: got %d bytes, want %d", len(got), len(big))
	}
	if strings.Contains(got, mcpSpillMarkerOpen) {
		t.Fatalf("spill marker leaked with spill disabled: %q", got)
	}
}

func TestCallToolSpillsLargeOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(mcpSpillDirEnv, dir)
	t.Setenv(mcpSpillThresholdEnv, "256")

	big := strings.Repeat("0123456789", 100) // 1000 字节
	response := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": big},
	}}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)

	got, err := c.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	path := spillPathInResult(t, got)
	if !pathWithinDir(dir, path) {
		t.Fatalf("CallTool artifact escaped dir: %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CallTool artifact: %v", err)
	}
	if string(data) != big {
		t.Fatalf("CallTool artifact does not hold the full output: got %d bytes, want %d", len(data), len(big))
	}
	if len(got) >= len(big) {
		t.Fatalf("CallTool result was not bounded: got %d bytes, want < %d", len(got), len(big))
	}
}
