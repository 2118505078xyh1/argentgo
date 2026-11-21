package mcp

import (
	"errors"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 在请求到达传输之前返回的哨兵错误。调用方可以用 errors.Is
// 匹配它们，并把包装后的消息展示给模型。
var (
	// ErrDisabled 报告 Enabled 字段显式为 false 的 spec。
	ErrDisabled = errors.New("mcp: server is disabled")
	// ErrReadOnly 报告被只读服务器阻止的修改类调用。
	ErrReadOnly = errors.New("mcp: server is read-only")
	// ErrToolNotAllowed 报告被 allowedTools/deniedTools 排除的调用。
	ErrToolNotAllowed = errors.New("mcp: tool not allowed")
)

// IsEnabled 报告该 spec 是否可以被连接。Enabled 为 nil 表示
// 零值默认值：启用。
func (s Spec) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// splitQualified 把 "server__tool" 拆成两部分。普通名称的
// server 部分为空。
func splitQualified(name string) (server, tool string) {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+2:])
	}
	return "", strings.TrimSpace(name)
}

// matchesTool 把允许/拒绝条目与工具比较。普通条目在任何服务器上
// 匹配工具简称；限定条目（或限定式的宣告名称）还要求服务器一致。
// "*" 可作为通配符部分。
func matchesTool(pattern, server, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	pServer, pTool := splitQualified(pattern)
	tServer, tTool := splitQualified(name)
	if pTool == "*" || strings.EqualFold(pTool, tTool) {
		if pServer == "" || pServer == "*" {
			return true
		}
		if tServer != "" && strings.EqualFold(pServer, tServer) {
			return true
		}
		return strings.EqualFold(pServer, strings.TrimSpace(server))
	}
	return false
}

// toolPermitted 先应用拒绝，使拒绝始终胜过允许。
func toolPermitted(spec Spec, tool Tool) bool {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return false
	}
	server := strings.TrimSpace(tool.Server)
	if server == "" {
		server = spec.Name
	}
	for _, pattern := range spec.DeniedTools {
		if matchesTool(pattern, server, name) {
			return false
		}
	}
	if len(spec.AllowedTools) == 0 {
		return true
	}
	for _, pattern := range spec.AllowedTools {
		if matchesTool(pattern, server, name) {
			return true
		}
	}
	return false
}

// FilterTools 对工具应用 spec 的允许/拒绝列表，同时保持其
// 顺序。禁用的 spec 不暴露任何工具。
func FilterTools(spec Spec, tools []Tool) []Tool {
	if !spec.IsEnabled() {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if toolPermitted(spec, tool) {
			out = append(out, tool)
		}
	}
	return out
}

// EnabledSpecs 只返回启用的 spec，保持原始顺序。
func EnabledSpecs(specs []Spec) []Spec {
	out := make([]Spec, 0, len(specs))
	for _, spec := range specs {
		if spec.IsEnabled() {
			out = append(out, spec)
		}
	}
	return out
}

// Tools 返回最后一次成功 ListTools 调用缓存的工具，经过
// 权限过滤。在调用 ListTools 之前它为空。
func (c *client) Tools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.spec.IsEnabled() {
		return nil
	}
	return append([]Tool(nil), c.tools...)
}

// Spec 返回构造此客户端时使用的配置。
func (c *client) Spec() Spec { return c.spec }

// Specs 在启用时返回本服务器的 spec，禁用时什么也不返回。它
// 使调用方能从一组客户端构造统一的视图。
func (c *client) Specs() []Spec {
	if !c.spec.IsEnabled() {
		return nil
	}
	return []Spec{c.spec}
}

// Clients 是一组有序的已连接 MCP 服务器，例如应用层中的
// t.clients 切片。Tools 和 Specs 只暴露启用的服务器并应用每台
// 服务器的工具过滤器；外来的 Client 实现不贡献任何内容，因为它们
// 没有缓存的、已过滤的工具视图。
type Clients []Client

// Specs 按连接顺序为每个启用的服务器返回一个 spec。
func (cs Clients) Specs() []Spec {
	out := make([]Spec, 0, len(cs))
	for _, c := range cs {
		if impl, ok := c.(*client); ok {
			out = append(out, impl.Specs()...)
		}
	}
	return out
}

// Tools 按连接顺序返回每台启用服务器的过滤后工具。
func (cs Clients) Tools() []Tool {
	out := make([]Tool, 0, len(cs))
	for _, c := range cs {
		if impl, ok := c.(*client); ok {
			out = append(out, impl.Tools()...)
		}
	}
	return out
}

// 注册表是进程级的，使调用方能在任何服务器连接之前，让启发式规则
// 获知自定义工具名。
var (
	mutatingMu    sync.RWMutex
	mutatingExtra = make(map[string]bool)
)

// MarkMutating 把额外的工具名注册为修改类。匹配使用
// 短（非限定）名称且不区分大小写，因此 "deploy" 和
// "prod__deploy" 都能覆盖。
func MarkMutating(name string) {
	key := strings.ToLower(shortName(name))
	if key == "" {
		return
	}
	mutatingMu.Lock()
	mutatingExtra[key] = true
	mutatingMu.Unlock()
}

// IsMutating 报告 name 看起来是否是会改变状态的工具。它
// 结合保守的内置启发式（已下沉到零依赖的 security 包，与目录过滤和
// 执行门控共用）与 MarkMutating 添加的名称；
// 未知名称被视为只读，使新工具不会被悄悄
// 允许写入。
func IsMutating(name string) bool {
	key := shortName(name)
	if key == "" {
		return false
	}
	mutatingMu.RLock()
	extra := mutatingExtra[strings.ToLower(key)]
	mutatingMu.RUnlock()
	return extra || security.MutatingTool(name)
}

func shortName(name string) string {
	_, tool := splitQualified(strings.TrimSpace(name))
	return tool
}
