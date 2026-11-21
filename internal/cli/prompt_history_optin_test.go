package cli

import "testing"

// TestPromptHistoryOptInRegistered 保证 ARGENT_TUI_PROMPT_HISTORY 已登记为
// 独立门控（唯一真源）。文档一致性由 optin_docs_guard_test.go 的三个守卫
// （Documented / HaveSourceReadPoints / NoGhost）统一覆盖。
func TestPromptHistoryOptInRegistered(t *testing.T) {
	item, ok := LookupIndependentOptIn("ARGENT_TUI_PROMPT_HISTORY")
	if !ok {
		t.Fatal("ARGENT_TUI_PROMPT_HISTORY 未登记到 IndependentOptIns")
	}
	if item.Key != "tui_prompt_history" {
		t.Fatalf("Key = %q, want tui_prompt_history", item.Key)
	}
	if item.OptInKey != "" {
		t.Fatalf("OptInKey = %q, want 空（仅独立 env）", item.OptInKey)
	}
	if item.Historical {
		t.Fatal("该门控有真实源码读点，不应标记为 Historical")
	}
}
