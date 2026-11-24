package tui

import (
	"testing"
)

// TestBindingsFromRaw 验证配置块 → 重绑定表的端到端解析：三种块名都被识别，
// 未知动作被忽略（fail-closed），结果 键串→动作 可直接注入 SetBindings。
func TestBindingsFromRaw(t *testing.T) {
	raw := map[string]any{
		"key_aliases": map[string]any{
			"undo":           "ctrl+z",
			"scroll_up":      []any{"k", "up"},
			"unknown_action": "x", // 未知动作，应忽略
		},
	}
	b := BindingsFromRaw(raw)
	if got, ok := b["ctrl+z"]; !ok || got != ActUndo {
		t.Fatalf("undo binding = %q,%v", got, ok)
	}
	if got, ok := b["k"]; !ok || got != ActScrollUp {
		t.Fatalf("scroll_up alias k = %q,%v", got, ok)
	}
	if _, ok := b["x"]; ok {
		t.Fatal("unknown action must be ignored")
	}
	// 空/无配置返回空表（非 nil 也无妨，len==0 时 SetBindings 恢复默认）。
	if got := len(BindingsFromRaw(nil)); got != 0 {
		t.Fatalf("nil raw produced %d bindings", got)
	}
}

// TestSetBindingsResetsToDefault 验证 SetBindings(nil) 恢复默认分派。
func TestSetBindingsResetsToDefault(t *testing.T) {
	m := New(Config{})
	m.SetBindings(Bindings{"z": ActUndo})
	if len(m.bindings) != 1 {
		t.Fatalf("bindings not set: %v", m.bindings)
	}
	m.SetBindings(nil)
	if m.bindings != nil {
		t.Fatalf("SetBindings(nil) must clear bindings, got %v", m.bindings)
	}
}
