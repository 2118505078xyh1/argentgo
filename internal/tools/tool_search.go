package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/pluginapi"
	"example.com/argentgo/internal/tools/toolrank"
)

// ---- tool_search ----

const toolSearchMaxResults = 20

// toolSearchTool 读取注册表自身的 spec，因此不需要依赖
// 消费这些 spec 的 agent 包。
type toolSearchTool struct {
	registry *Registry
}

func (toolSearchTool) Name() string { return "tool_search" }
func (toolSearchTool) Description() string {
	return "Discover other available tools by name or description. Use this whenever a task needs a capability not in your current tool list (e.g. git, web, LSP, memory, time, repo map). Returns up to 20 matches; call the discovered tool by its exact name afterwards."
}
func (toolSearchTool) Mutating() bool { return false }
func (toolSearchTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"tool name or description words, case-insensitive"},"limit":{"type":"integer","description":"maximum matches, capped at 20"}},"required":["query"]}`
}

func (t toolSearchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("tool_search: bad arguments: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if len(toolrank.Tokenize(query)) == 0 {
		return "", fmt.Errorf("tool_search: query must not be empty")
	}
	if t.registry == nil {
		return "(no tools registered)", nil
	}

	limit := args.Limit
	if limit <= 0 || limit > toolSearchMaxResults {
		limit = toolSearchMaxResults
	}

	// 从注册表已经组装好的 spec 建立文档：名称、描述、schema 属性名。
	// 排序交给 toolrank 的 BM25（IDF + 字段加权），不再手写逐 token 启发式。
	specs := t.registry.Specs()
	docs := make([]toolrank.Doc, 0, len(specs))
	for _, spec := range specs {
		docs = append(docs, toolrank.Doc{
			Name:  spec.Function.Name,
			Desc:  spec.Function.Description,
			Props: toolrank.IndexSchema(spec.Function.Parameters),
		})
	}
	ranked := toolrank.Rank(query, docs)
	if len(ranked) == 0 {
		return fmt.Sprintf("no tools match %q", args.Query), nil
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	lines := make([]string, 0, len(ranked))
	for _, match := range ranked {
		name := match.Doc.Name
		oneLine := strings.TrimSpace(strings.SplitN(match.Doc.Desc, "\n", 2)[0])
		if oneLine == "" {
			oneLine = "(no description)"
		}
		lines = append(lines, name+": "+oneLine)
		// 命中 deferred 工具后记入 reveal 集：下一轮 FilterSpecs 会把它并入
		// 公布目录，模型便无需再次搜索即可看到其 schema。
		if t.registry.Exposure(name) == pluginapi.ExposureDeferred {
			if reveal := revealFor(t.registry); reveal != nil {
				reveal.add(name)
			}
		}
	}
	return clip(ctx, strings.Join(lines, "\n")), nil
}

// deferredReveal 记录一次会话中被 tool_search 命中的 deferred 工具名。
// 它只在确有 deferred 工具被命中时分配，默认路径没有任何同步开销或状态。
type deferredReveal struct {
	mu    sync.Mutex
	names map[string]bool
}

func (d *deferredReveal) add(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.names == nil {
		d.names = make(map[string]bool)
	}
	d.names[name] = true
}

func (d *deferredReveal) snapshot() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]bool, len(d.names))
	for name := range d.names {
		out[name] = true
	}
	return out
}

// deferredReveals 以注册表指针为键。注册表每运行一份，默认没有 deferred 工具
// 时根本不会写入；调用方在会话结束时用 ClearRevealed 显式清理。
var deferredReveals sync.Map

func revealFor(r *Registry) *deferredReveal {
	if r == nil {
		return nil
	}
	entry, _ := deferredReveals.LoadOrStore(r, &deferredReveal{})
	return entry.(*deferredReveal)
}

// RevealedNames 返回已被 tool_search 命中的 deferred 工具名副本。
func (r *Registry) RevealedNames() map[string]bool {
	if r == nil {
		return nil
	}
	if entry, ok := deferredReveals.Load(r); ok {
		return entry.(*deferredReveal).snapshot()
	}
	return nil
}

// ClearRevealed 丢弃某注册表积累的 deferred 命中集，供会话结束时调用。
func ClearRevealed(r *Registry) {
	if r != nil {
		deferredReveals.Delete(r)
	}
}

// ToolExposure 返回工具的暴露级别，并解开 intentSchemaTool 等透明包装。
func ToolExposure(tool Tool) pluginapi.Exposure {
	for {
		wrapped, ok := tool.(intentSchemaTool)
		if !ok {
			break
		}
		tool = wrapped.Tool
	}
	return pluginapi.ToolExposure(tool)
}

// Exposure 按名字返回注册工具的暴露级别；未知工具视为 direct。
func (r *Registry) Exposure(name string) pluginapi.Exposure {
	if r == nil {
		return pluginapi.ExposureDirect
	}
	if tool, ok := r.tools[name]; ok {
		return ToolExposure(tool)
	}
	return pluginapi.ExposureDirect
}

// FilterByExposure 收窄已公布的目录：hidden 工具永不公布；visible 中未命中
// 的 deferred 工具被剔除；而被 tool_search 命中（revealed）的 deferred 工具
// 若已被精简目录（LazyFilterSpecs）剔除，则从 full 中补回，实现「命中 deferred
// 后并入本轮」。full 应传精简前的目录，且已经过安全过滤。
// 它是纯函数：不修改入参，不改变原有 spec 顺序，补回项追加在末尾。
func FilterByExposure(visible, full []llm.ToolSpec, exposure func(string) pluginapi.Exposure, revealed map[string]bool) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(visible))
	present := make(map[string]bool, len(visible))
	for _, spec := range visible {
		if exposure(spec.Function.Name) == pluginapi.ExposureHidden {
			continue
		}
		present[spec.Function.Name] = true
		out = append(out, spec)
	}
	if len(revealed) == 0 {
		return out
	}
	for _, spec := range full {
		name := spec.Function.Name
		if present[name] || !revealed[name] || exposure(name) != pluginapi.ExposureDeferred {
			continue
		}
		out = append(out, spec)
		present[name] = true
	}
	return out
}
