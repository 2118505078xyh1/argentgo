package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"example.com/argentgo/internal/memory"
)

// 本文件提供 memory_consolidate 工具：显式触发记忆库的近重复整合。
// 它刻意不进默认注册表（golden 逐字节不变），由 app 在 opt-in 时注册。

const memoryConsolidateName = "memory_consolidate"

type memoryConsolidateTool struct{}

func (t *memoryConsolidateTool) Name() string { return memoryConsolidateName }
func (t *memoryConsolidateTool) Description() string {
	return "Consolidate the cross-session memory bank: merge near-duplicate facts, combine their sources, and sort by recency. Modifies the memory log; use only when you want to clean up."
}
func (t *memoryConsolidateTool) Mutating() bool { return true }
func (t *memoryConsolidateTool) Schema() string { return `{"type":"object","properties":{}}` }

func (t *memoryConsolidateTool) Run(ctx context.Context, root string, _ json.RawMessage) (string, error) {
	// bank 只是文件路径的封装：用 root/.argent 自建实例即可操作与其他
	// memory_* 工具相同的磁盘日志，无需 Registry 共享实例（也避免锁定热路径）。
	bank := memory.NewBank(filepath.Join(root, ".argent"))
	stats, err := bank.ConsolidateBank()
	if err != nil {
		return "", fmt.Errorf("memory_consolidate: %w", err)
	}
	data, err := json.Marshal(stats)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RegisterMemoryConsolidate 显式注册工具。nil 是空操作。
func RegisterMemoryConsolidate(r *Registry) {
	if r == nil {
		return
	}
	if _, exists := r.tools[memoryConsolidateName]; exists {
		return
	}
	r.tools[memoryConsolidateName] = &memoryConsolidateTool{}
	r.order = append(r.order, memoryConsolidateName)
}
