package tools

// Wrappers around the persistent memory bank. Each tool carries its own bank,
// so there is no global state and tests can inject a temp-dir bank.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/memory"
)

// ---- memory_retain ----

type memoryRetainTool struct{ bank *memory.Bank }

func newMemoryRetainTool(bank *memory.Bank) *memoryRetainTool { return &memoryRetainTool{bank: bank} }

func (t *memoryRetainTool) Name() string { return "memory_retain" }

func (t *memoryRetainTool) Description() string {
	return "Persist a durable fact with optional tags and source into the cross-session memory bank. Secrets are redacted before storage. Returns the stored id."
}

func (t *memoryRetainTool) Mutating() bool { return true }

func (t *memoryRetainTool) Schema() string {
	return `{"type":"object","properties":{"text":{"type":"string","description":"the fact to remember"},"tags":{"type":"array","items":{"type":"string"},"description":"optional labels used by memory_recall"},"source":{"type":"string","description":"optional origin of the fact"}},"required":["text"]}`
}

func (t *memoryRetainTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.bank == nil {
		return "", fmt.Errorf("memory_retain: no bank configured")
	}
	var args struct {
		Text   string   `json:"text"`
		Tags   []string `json:"tags"`
		Source string   `json:"source"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("memory_retain: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("memory_retain: text must not be empty")
	}
	id, err := t.bank.Retain(args.Text, args.Tags, args.Source)
	if err != nil {
		return "", fmt.Errorf("memory_retain: %w", err)
	}
	return "stored memory " + id, nil
}

// ---- memory_recall ----

type memoryRecallTool struct{ bank *memory.Bank }

func newMemoryRecallTool(bank *memory.Bank) *memoryRecallTool { return &memoryRecallTool{bank: bank} }

func (t *memoryRecallTool) Name() string { return "memory_recall" }

func (t *memoryRecallTool) Description() string {
	return "Search the cross-session memory bank by word overlap and tags. Results are ranked by relevance, newest first; an empty query returns the latest memories."
}

func (t *memoryRecallTool) Mutating() bool { return false }

func (t *memoryRecallTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"words to match the stored text; empty returns the latest memories"},"tags":{"type":"array","items":{"type":"string"},"description":"only memories carrying one of these tags"},"limit":{"type":"integer","description":"maximum number of results, default 10"}}}`
}

func (t *memoryRecallTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.bank == nil {
		return "", fmt.Errorf("memory_recall: no bank configured")
	}
	var args struct {
		Query string   `json:"query"`
		Tags  []string `json:"tags"`
		Limit int      `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("memory_recall: bad arguments: %w", err)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	hits := t.bank.Recall(args.Query, args.Tags, limit)
	if len(hits) == 0 {
		return "no memories found", nil
	}
	data, err := json.MarshalIndent(hits, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_recall: encode: %w", err)
	}
	return string(data), nil
}

// ---- memory_forget ----

type memoryForgetTool struct{ bank *memory.Bank }

func newMemoryForgetTool(bank *memory.Bank) *memoryForgetTool { return &memoryForgetTool{bank: bank} }

func (t *memoryForgetTool) Name() string { return "memory_forget" }

func (t *memoryForgetTool) Description() string {
	return "Remove one fact from the cross-session memory bank by the id returned from memory_retain. Forgetting an unknown id is a no-op."
}

func (t *memoryForgetTool) Mutating() bool { return true }

func (t *memoryForgetTool) Schema() string {
	return `{"type":"object","properties":{"id":{"type":"string","description":"the id returned by memory_retain"}},"required":["id"]}`
}

func (t *memoryForgetTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.bank == nil {
		return "", fmt.Errorf("memory_forget: no bank configured")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("memory_forget: bad arguments: %w", err)
	}
	id := strings.TrimSpace(args.ID)
	if id == "" {
		return "", fmt.Errorf("memory_forget: id must not be empty")
	}
	if err := t.bank.Forget(id); err != nil {
		return "", fmt.Errorf("memory_forget: %w", err)
	}
	return "forgot memory " + id, nil
}
