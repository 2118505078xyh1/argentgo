package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// 本文件实现 `models` 工具：列出内置 provider 模板及其默认模型。
// 数据由 app 层从 provider.List() 转成安全子集后注入（只含公开字段，
// 不含 API key 或其环境变量名），tools 包因此不依赖 provider，零依赖闭环。
// 与 CLI 的 `argentgo models` 同口径：纯静态、不联网。

const modelsMaxBytes = 32 << 10

// ModelInfo 是一个 provider 模板对外可见的安全子集。
type ModelInfo struct {
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model"`
}

type modelsTool struct {
	models []ModelInfo
}

func (modelsTool) Name() string { return "models" }

func (modelsTool) Description() string {
	return "List the built-in provider templates (id, protocol, API base URL, default model). " +
		"Static offline catalog — does not query the network or reveal credentials. " +
		"Use the returned provider id with --provider / model configuration."
}

func (modelsTool) Mutating() bool { return false }
func (modelsTool) Schema() string {
	return `{"type":"object","properties":{"provider":{"type":"string","description":"optional provider id to filter the list"}}}`
}

func (t modelsTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Provider string `json:"provider"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
	}
	models := t.models
	if filter := strings.TrimSpace(args.Provider); filter != "" {
		filtered := make([]ModelInfo, 0, 1)
		for _, m := range models {
			if m.Name == filter {
				filtered = append(filtered, m)
			}
		}
		models = filtered
		if len(models) == 0 {
			return "", nil
		}
	}
	// 防御性排序：即便注入方给的顺序不同，模型看到的也稳定。
	out := append([]ModelInfo(nil), models...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	text := string(data)
	if len(text) > modelsMaxBytes {
		text = text[:modelsMaxBytes] + "\n… [truncated]"
	}
	return text, nil
}

// RegisterModels 把 models 工具显式追加到 r 的末尾（幂等）。
// 绝不被 New/NewWithLimit 调用，默认 Specs()/Names() 与 golden 逐字节不变。
func RegisterModels(r *Registry, models []ModelInfo) {
	if r == nil {
		return
	}
	const name = "models"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = modelsTool{models: append([]ModelInfo(nil), models...)}
	r.order = append(r.order, name)
}
