package tools

// Advanced memory tools: reflect (read-only extractive synthesis) and learn
// (retain a lesson and optionally promote it into a managed SKILL.md). Both take
// an injected *memory.Bank, matching the existing memory tools, so there is no
// global state and callers can wire any bank they like.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/memory"
)

// ---- memory_reflect ----

type memoryReflectTool struct{ bank *memory.Bank }

// NewMemoryReflectTool builds the read-only memory_reflect tool.
func NewMemoryReflectTool(bank *memory.Bank) *memoryReflectTool {
	return &memoryReflectTool{bank: bank}
}

func (t *memoryReflectTool) Name() string { return "memory_reflect" }

func (t *memoryReflectTool) Description() string {
	return "Synthesise an answer to a question from the cross-session memory bank. Extractive and local: it concatenates the most relevant distinct memories (no model call)."
}

func (t *memoryReflectTool) Mutating() bool { return false }

func (t *memoryReflectTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"words to reflect over; empty uses the latest memories"},"limit":{"type":"integer","description":"maximum source memories, default 10"}}}`
}

func (t *memoryReflectTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.bank == nil {
		return "", fmt.Errorf("memory_reflect: no bank configured")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("memory_reflect: bad arguments: %w", err)
	}
	return t.bank.Reflect(args.Query, args.Limit), nil
}

// ---- memory_learn ----

type memoryLearnTool struct{ bank *memory.Bank }

// NewMemoryLearnTool builds the mutating memory_learn tool.
func NewMemoryLearnTool(bank *memory.Bank) *memoryLearnTool { return &memoryLearnTool{bank: bank} }

func (t *memoryLearnTool) Name() string { return "memory_learn" }

func (t *memoryLearnTool) Description() string {
	return "Capture a reusable lesson in the cross-session memory bank. With promote=true also write a SKILL.md-compatible file under the bank's skills directory. Returns the stored id and optional skill path."
}

func (t *memoryLearnTool) Mutating() bool { return true }

func (t *memoryLearnTool) Schema() string {
	return `{"type":"object","properties":{"text":{"type":"string","description":"the reusable lesson to remember"},"tags":{"type":"array","items":{"type":"string"},"description":"optional labels for later recall"},"promote":{"type":"boolean","description":"also write a SKILL.md file for the lesson"}},"required":["text"]}`
}

func (t *memoryLearnTool) Run(_ context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.bank == nil {
		return "", fmt.Errorf("memory_learn: no bank configured")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args struct {
		Text    string   `json:"text"`
		Tags    []string `json:"tags"`
		Promote bool     `json:"promote"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("memory_learn: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Text) == "" {
		return "", fmt.Errorf("memory_learn: text must not be empty")
	}
	id, skillPath, learnErr := t.bank.Learn(args.Text, args.Tags, args.Promote)
	data, err := json.Marshal(struct {
		ID        string `json:"id"`
		SkillPath string `json:"skillPath"`
	}{ID: id, SkillPath: skillPath})
	if err != nil {
		return "", fmt.Errorf("memory_learn: encode: %w", err)
	}
	if learnErr != nil {
		// The lesson was still retained; surface the id alongside the failure.
		return string(data), fmt.Errorf("memory_learn: %w", learnErr)
	}
	return string(data), nil
}

// MemoryAdvancedTools returns the advanced memory tools in a stable order,
// ready to be appended to a registry's default tool list.
func MemoryAdvancedTools(bank *memory.Bank) []Tool {
	return []Tool{NewMemoryReflectTool(bank), NewMemoryLearnTool(bank)}
}
