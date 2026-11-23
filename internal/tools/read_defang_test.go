package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestReadDefangsMinifiedLine 锁定 #38：超长单行按 rune 截断并附标记，
// CJK 行不会被切坏，输出仍受既有 clip 预算约束。
func TestReadDefangsMinifiedLine(t *testing.T) {
	longASCII := strings.Repeat("a", 10000)
	longCJK := strings.Repeat("中文", 1400) // 2800 runes
	content := longASCII + "\n" + longCJK + "\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "min.js"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"min.js"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatal("read 输出含被切坏的 rune")
	}
	if strings.Count(out, lineTruncatedMarker) != 2 {
		t.Fatalf("期望两处 [line truncated]，得到 %d；输出 %q", strings.Count(out, lineTruncatedMarker), out[:min(len(out), 200)])
	}
	for _, line := range strings.Split(out, "\n") {
		runes := []rune(strings.TrimSuffix(line, lineTruncatedMarker))
		if len(runes) > readMaxLineRunes {
			t.Fatalf("单行仍有 %d runes，超过上限 %d", len(runes), readMaxLineRunes)
		}
	}
	// 中文必须完整保留（截断后仍是可读汉字，而非替换符）。
	if !strings.Contains(out, "中文") {
		t.Fatal("CJK 行被错误丢弃")
	}
}
