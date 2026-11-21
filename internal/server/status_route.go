package server

import "net/http"

// NewStatusMux 返回一个空的状态注册表。StatusMux 的零值本就可用，
// 这里提供显式构造器，使 routes() 中的可选接线意图更清晰。
func NewStatusMux() *StatusMux {
	return &StatusMux{}
}

// status 返回 GET /v1/status 的端点处理函数。调用方应保证只在配置了
// StatusProvider 时才注册该路由，并用 h.auth 包裹：状态含内部信息，
// 与无需 token 的 health/version 不同。
//
// 处理逻辑完全委托给 WriteStatus，确保响应体字节不做任何再编码：
// 成功时为提供者原始字节；WriteStatus 返回 false 只可能是方法非 GET
// （本路由的 mux 必然已注册提供者），此时复用既有的 405 响应。
func (h *handler) status(statusMux *StatusMux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if WriteStatus(w, r, statusMux) {
			return
		}
		methodNotAllowed(w, http.MethodGet)
	}
}
