package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBashToolEmitsStartProgress 验证命令执行派发前先发出 "bash: running …" 阶段标签，
// 且命令摘要进入标签（长任务据此可判断"在做什么"，而不只是"工具执行中…"）。
func TestBashToolEmitsStartProgress(t *testing.T) {
	var lines []string
	ctx := context.WithValue(context.Background(), toolProgressKey, func(text string) {
		lines = append(lines, text)
	})
	_, err := (bashTool{}).Run(ctx, t.TempDir(), json.RawMessage(`{"command":"echo progress-ok"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("progress lines = %d, want 1: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "bash: running ") {
		t.Fatalf("progress = %q, want prefix bash: running", lines[0])
	}
	if !strings.Contains(lines[0], "echo progress-ok") {
		t.Fatalf("progress = %q, want command summary", lines[0])
	}
}

// TestBashToolProgressSilentWithoutEmitter 验证没有门控发射器时发布进度是空操作。
func TestBashToolProgressSilentWithoutEmitter(t *testing.T) {
	if _, err := (bashTool{}).Run(context.Background(), t.TempDir(), json.RawMessage(`{"command":"echo ok"}`)); err != nil {
		t.Fatalf("Run without emitter: %v", err)
	}
}

// TestBashProgressSummaryStable 验证摘要折叠空白并按 rune 限长，CJK 不被从字符中间切断。
func TestBashProgressSummaryStable(t *testing.T) {
	if got, want := bashProgressSummary("  go   test\n./...\t-count=1 "), "go test ./... -count=1"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	got := bashProgressSummary(strings.Repeat("阶段", 40))
	if !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) != 33 {
		t.Fatalf("long summary = %q (runes=%d), want 32 runes + ellipsis", got, utf8.RuneCountInString(got))
	}
}
