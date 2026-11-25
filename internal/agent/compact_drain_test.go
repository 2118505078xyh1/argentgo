package agent

import (
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestDrainOldestForBudget(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "oldest user message that should be dropped first"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second user message"},
		{Role: "user", Content: "latest prompt"},
	}
	original := append([]llm.Message(nil), messages...)

	// window 设为很小，迫使淘汰；上限 2 条。
	got, removed := drainOldestForBudget(messages, 1, 2)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if len(got) != len(messages)-2 {
		t.Fatalf("len = %d, want %d", len(got), len(messages)-2)
	}
	if got[0].Content != "system prompt" {
		t.Fatal("system message was dropped")
	}
	if got[len(got)-1].Content != "latest prompt" {
		t.Fatal("last message was dropped")
	}
	// 入参未被修改。
	for i := range original {
		if messages[i].Content != original[i].Content {
			t.Fatalf("input mutated at %d", i)
		}
	}
}

func TestDrainOldestForBudgetNoopCases(t *testing.T) {
	msgs := []llm.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	if got, n := drainOldestForBudget(msgs, 0, 10); n != 0 || len(got) != 2 {
		t.Fatal("must keep system + last message")
	}
	if got, n := drainOldestForBudget(msgs, 1000, 10); n != 0 || len(got) != 2 {
		t.Fatal("no drain needed when under budget")
	}
}

func TestSetCompactDrainToggle(t *testing.T) {
	t.Cleanup(func() { SetCompactDrain(false) })
	SetCompactDrain(true)
	if !compactDrainEnabled() {
		t.Fatal("enable failed")
	}
	SetCompactDrain(false)
	if compactDrainEnabled() {
		t.Fatal("disable failed")
	}
}
