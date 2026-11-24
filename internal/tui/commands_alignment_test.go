package tui

import (
	"testing"
)

// TestCommandCandidatesAlignDescriptions 验证命令候选两列对齐：不同长度
// 的命令名，其描述起始列一致（修复"命令显示很奇怪"的对齐问题）。
func TestCommandCandidatesAlignDescriptions(t *testing.T) {
	visible := []CompletionCandidate{
		{Command: "help", Description: "show help"},
		{Command: "compact", Description: "compact context"},
		{Command: "name", Description: "rename session"},
	}
	hints := map[string]string{}
	m := New(Config{})
	lines := m.commandsCandidateLines(visible, hints, 80, 3, 0, 0)
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	// 描述起点 = 2（mark）+ primaryWidth + 2（gap）。所有行描述起点必须一致。
	descCol := -1
	for _, line := range lines {
		// 找描述文本 "show/compact/rename" 的起始 span。
		for i, span := range line.Spans {
			if span.Text == "show help" || span.Text == "compact context" || span.Text == "rename session" {
				col := 0
				for _, s := range line.Spans[:i] {
					col += TextWidth(s.Text)
				}
				if descCol == -1 {
					descCol = col
				} else if col != descCol {
					t.Fatalf("description column drift: %d vs %d", col, descCol)
				}
			}
		}
	}
	if descCol < 0 {
		t.Fatal("no description spans found")
	}
}

// TestCommandPrimaryWidthClamped 验证主列宽钳制在 [12,32]。
func TestCommandPrimaryWidthClamped(t *testing.T) {
	visible := []CompletionCandidate{{Command: "a", Description: "x"}}
	if got := commandPrimaryWidth(visible, nil); got != 12 {
		t.Fatalf("min clamp = %d, want 12", got)
	}
	long := make([]rune, 50)
	for i := range long {
		long[i] = 'x'
	}
	visible = []CompletionCandidate{{Command: string(long), Description: "x"}}
	if got := commandPrimaryWidth(visible, nil); got != 32 {
		t.Fatalf("max clamp = %d, want 32", got)
	}
}
