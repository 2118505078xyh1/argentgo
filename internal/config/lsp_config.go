package config

import (
	"encoding/json"
	"sort"
	"strings"
)

// 本文件解析分层配置树里的 `lsp` 块。配置层保持零依赖：这里只产出中性的数据结构，
// 由 app 装配层转换成 lsp.Server 注入 Registry。字段与 opencode 的 Config.LSP.Server 对齐：
// command / extensions / disabled / env / initialization。
//
// 解析刻意宽松（fail-closed）：类型不符的字段按缺省处理，绝不让一份拼写错误的
// 项目配置使整个 agent 启动失败。七层优先级由 config.Load 的 mergeTree 负责，
// 本文件只消费最终合并出来的 Raw 树。

// LSPServer 是一个语言服务器的配置形态。Command 为非空字符串数组，
// 首元素是可执行程序，其余是参数；Extensions 是文件扩展名（不含点，大小写不敏感）。
type LSPServer struct {
	Command        []string
	Extensions     []string
	Disabled       bool
	Env            map[string]string
	Initialization map[string]any
}

// LSPConfig 是 `lsp` 块的解析结果。Disabled 为 true 时整体关闭 LSP 配置注入，
// 内置默认服务器仍按原样工作（除非按名称显式 disabled）。
type LSPConfig struct {
	Disabled bool
	Servers  map[string]LSPServer
}

// ParseLSPConfig 从合并后的配置树里解析 `lsp` 块。raw 为 nil 或缺少该键时返回零值，
// 因此未配置 LSP 的运行逐字节不变。返回值的顺序稳定（Servers 为 map，调用方自行排序）。
func ParseLSPConfig(raw map[string]any) LSPConfig {
	var out LSPConfig
	if raw == nil {
		return out
	}
	block, ok := raw["lsp"]
	if !ok || block == nil {
		return out
	}
	// 整体布尔开关：lsp: false 表示整块关闭；lsp: true 等价于未配置。
	if flag, ok := block.(bool); ok {
		out.Disabled = !flag
		return out
	}
	tree, ok := block.(map[string]any)
	if !ok {
		return out
	}
	if v, ok := tree["disabled"]; ok {
		out.Disabled = truthy(v)
	}
	out.Servers = make(map[string]LSPServer)
	for name, value := range tree {
		name = strings.TrimSpace(name)
		if name == "" || strings.EqualFold(name, "disabled") {
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		out.Servers[name] = parseLSPServer(entry)
	}
	return out
}

// parseLSPServer 解析单个服务器条目。布尔整体关闭也支持：server: false。
func parseLSPServer(entry map[string]any) LSPServer {
	var s LSPServer
	if flag, ok := entry["disabled"]; ok {
		s.Disabled = truthy(flag)
	}
	s.Command = stringSlice(entry, "command", "args")
	s.Extensions = stringSlice(entry, "extensions", "languageIds", "language_ids")
	if env, ok := stringMap(entry, "env"); ok {
		s.Env = env
	}
	if init, ok := entry["initialization"].(map[string]any); ok {
		s.Initialization = cloneTree(init)
	}
	return s
}

// truthy 判断是否开启：只认布尔 true、真值字符串（1/true/yes/on）。
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// stringSlice 读取一个字符串数组；兼容字符串（按空白切分）与 []any。
func stringSlice(tree map[string]any, keys ...string) []string {
	for _, key := range keys {
		v, ok := tree[key]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			return strings.Fields(x)
		case []string:
			return append([]string(nil), x...)
		case []any:
			out := make([]string, 0, len(x))
			for _, item := range x {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// stringMap 读取一个字符串到字符串的映射。
func stringMap(tree map[string]any, key string) (map[string]string, bool) {
	v, ok := tree[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(m))
	for k, value := range m {
		if s, ok := value.(string); ok {
			out[k] = s
		}
	}
	return out, true
}

// lspSnapshot 返回 lsp 块的规范化 JSON（键有序），用于来源追踪时比较值是否变化。
func lspSnapshot(tree map[string]any) string {
	v, ok := tree["lsp"]
	if !ok || v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// map 的 JSON 编码本身按键有序；这里再排序顶层以兼容非 map 形态的差异。
	keys := make([]string, 0, 1)
	keys = append(keys, string(b))
	sort.Strings(keys)
	return strings.Join(keys, "")
}
