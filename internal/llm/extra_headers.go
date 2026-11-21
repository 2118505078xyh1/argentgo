package llm

import (
	"net/http"
	"sync"
)

// ExtraHeaders 是要注入到每个 HTTP 请求上的额外头集合。
// key 是头名（大小写不敏感，由 net/http 规范化），value 是头值。
// 典型用途是 Azure OpenAI 的 api-key 头以及各类自定义路由/追踪头。
type ExtraHeaders map[string]string

// extraHeadersMu 保护 extraHeaders 的并发读写。
var extraHeadersMu sync.RWMutex

// extraHeaders 是当前注入的头集合；nil 表示未注入（默认）。
// 零值即关闭注入，因此默认请求与新增该原语之前逐字节一致。
var extraHeaders map[string]string

// SetExtraHeaders 安装一组要注入到后续每个 HTTP 请求上的额外头。
//
// 该能力是 opt-in 的：不调用本函数时没有任何额外头被发送。
// 传入 nil 或空 map 等价于 ClearExtraHeaders（回到默认行为）。
// 入参 map 会被复制，调用方在返回后修改它不会影响已安装的头。
// 本函数并发安全。
func SetExtraHeaders(h map[string]string) {
	extraHeadersMu.Lock()
	defer extraHeadersMu.Unlock()
	if len(h) == 0 {
		extraHeaders = nil
		return
	}
	clone := make(map[string]string, len(h))
	for k, v := range h {
		clone[k] = v
	}
	extraHeaders = clone
}

// ClearExtraHeaders 清除所有已注入的额外头，恢复默认（无额外头）行为。
// 它等价于 SetExtraHeaders(nil)，并发安全。
func ClearExtraHeaders() {
	SetExtraHeaders(nil)
}

// currentExtraHeaders 返回当前注入头的一份副本；未注入时返回 nil。
// 返回副本使调用方无法改动包内状态。并发安全。
func currentExtraHeaders() map[string]string {
	extraHeadersMu.RLock()
	defer extraHeadersMu.RUnlock()
	if len(extraHeaders) == 0 {
		return nil
	}
	clone := make(map[string]string, len(extraHeaders))
	for k, v := range extraHeaders {
		clone[k] = v
	}
	return clone
}

// ApplyExtraHeaders 把当前注入的额外头写入 req.Header。
//
// 采用覆盖语义：若注入头与请求既有头同名，注入值通过 Header.Set 覆盖既有值，
// 使调用方可以用注入值替换客户端默认头（例如用 api-key 替换 Authorization）。
// 未注入任何头时是完全的 no-op，请求头保持不变（默认逐字节不变）。
// 调用方应在构造完请求、设置完自身默认头之后、发送之前调用本函数。
// req 为 nil 时安全返回。本函数并发安全：每次调用读取一份头快照后写入该请求。
func ApplyExtraHeaders(req *http.Request) {
	if req == nil {
		return
	}
	h := currentExtraHeaders()
	if len(h) == 0 {
		return
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
}
