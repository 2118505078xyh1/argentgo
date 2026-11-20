package mcp

import (
	"strings"
	"sync/atomic"
)

// toolListChangedMethod 是 MCP 服务器在工具清单发生变化时发送的通知方法名。
const toolListChangedMethod = "notifications/tools/list_changed"

// ToolCatalogRevision 是工具目录的逻辑版本号：工具清单可能发生变化时调用
// Bump 加一，调用方通过比较前后版本号判断是否需要重新执行 tools/list。
// 它本身不持有工具数据，只是并发安全的纯计数器，尚未接入任何通知分发路径。
//
// 零值即可使用；首次使用后不要复制该值（内部含 atomic.Uint64）。
type ToolCatalogRevision struct {
	rev atomic.Uint64
}

// Bump 把版本号加一并返回加一后的值。并发调用时每个调用者都会拿到唯一的
// 递增值，适用于「通知到达 → 标记目录已变化」这类只关心次数变化的场景。
func (r *ToolCatalogRevision) Bump() uint64 {
	return r.rev.Add(1)
}

// Revision 返回当前版本号。正常递增下它单调递增；Reset 之后归零。
func (r *ToolCatalogRevision) Revision() uint64 {
	return r.rev.Load()
}

// Reset 把版本号清零。仅在重新初始化目录（例如切换连接）时调用；若同时有
// 并发的 Bump，清零之后的版本号不再能反映清零前的递增次数。
func (r *ToolCatalogRevision) Reset() {
	r.rev.Store(0)
}

// isToolListChanged 判断一个通知方法名是否为 MCP 的
// notifications/tools/list_changed。JSON-RPC 方法名区分大小写，这里做精确
// 比较，仅额外容忍首尾空白（读取帧时常见的附带空格）；中间的空白、大小写
// 变化以及非 ASCII 文本都不匹配，避免误触发目录刷新。
func isToolListChanged(method string) bool {
	return strings.TrimSpace(method) == toolListChangedMethod
}
