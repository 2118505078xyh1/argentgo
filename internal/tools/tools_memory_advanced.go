package tools

// 高级记忆工具：reflect（只读的抽取式综合）和 learn
// （保留一条经验，并可选地提升为受管理的 SKILL.md）。两者都接受
// 注入的 *memory.Bank，与现有记忆工具一致，因此没有
// 全局状态，调用方可以接线任意库。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/memory"
)

// ---- memory_reflect ----

type memoryReflectTool struct{ bank *memory.Bank }

// NewMemoryReflectTool 构建只读的 memory_reflect 工具。
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

// NewMemoryLearnTool 构建具有变更性的 memory_learn 工具。
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
		// 经验仍被保留；把 id 连同失败一起呈现出来。
		return string(data), fmt.Errorf("memory_learn: %w", learnErr)
	}
	return string(data), nil
}

// MemoryAdvancedTools 以稳定顺序返回高级记忆工具，
// 可直接追加到注册表的默认工具列表。
func MemoryAdvancedTools(bank *memory.Bank) []Tool {
	return []Tool{NewMemoryReflectTool(bank), NewMemoryLearnTool(bank)}
}
