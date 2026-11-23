package tui

import (
	"strings"
	"testing"
)

// 状态栏必须显示“思考”二字，且点击它展开/收起全部思考块。
func TestStatusThinkingLabelIsClickable(t *testing.T) {
	m := New(Config{})
	m.thinking = "high"
	m.locale = "zh-CN"
	buf := m.Frame(100, 24)
	usage := buf.RowText(m.usageY)
	if !strings.Contains(usage, "high") {
		t.Fatalf("status line has no thinking level: %q", usage)
	}
	if m.statusReasoningFrom >= m.statusReasoningTo {
		t.Fatalf("no thinking hit range recorded: %d..%d", m.statusReasoningFrom, m.statusReasoningTo)
	}
	col := (m.statusReasoningFrom + m.statusReasoningTo) / 2
	m.MouseDown(col, m.usageY)
	if !m.reasoningAllExpanded {
		t.Fatal("clicking 思考 must expand reasoning")
	}
	m.MouseDown(col, m.usageY)
	if m.reasoningAllExpanded {
		t.Fatal("clicking 思考 again must collapse reasoning")
	}
}
