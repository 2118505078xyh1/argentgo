package server

import (
	"net/http"
	"strings"
)

// 本文件把浏览器跨域（CORS）能力收敛在 --serve 的 HTTP 传输层：
// 纯函数 corsAllowed 负责判定来源是否被允许，corsMiddleware 负责拦截
// 预检（OPTIONS）请求并补齐响应头。
//
// 背景：浏览器在跨源请求（尤其是带 Authorization 头或非简单 Content-Type
// 的 POST）之前会先发一个 OPTIONS 预检请求；只有当服务端回以明确的
// Access-Control-Allow-* 头时才会继续真正的请求。--serve 是 bearer token
// 保护的 HTTP/SSE 服务，缺少这些头时浏览器端根本无法调用。
//
// 判定规则都集中在 corsAllowed 中，它不读取 *http.Request、不触碰全局状态，
// 因此可在不启动服务器的情况下验证，并天然对 CJK 与其它区域设置保持稳定。

const (
	// corsAllowMethods 是预检响应中宣告允许的方法。health/version 用 GET，
	// prompt/abort 用 POST，预检本身用 OPTIONS。
	corsAllowMethods = "GET, POST, OPTIONS"
	// corsMaxAge 是预检结果的缓存秒数，减少重复预检的往返。
	corsMaxAge = "600"
)

// corsAllowed 报告请求来源 origin 是否在允许列表 allowed 中。规则如下：
//   - origin 为空（请求未携带 Origin 头，例如同源请求或非浏览器客户端）时
//     返回 false：没有需要放行的跨源来源，不能回显任何
//     Access-Control-Allow-Origin。此规则优先于下面的通配符规则；
//   - allowed 中包含 "*" 时返回 true（通配符放行任意来源）；
//   - 否则对 allowed 中每一项做大小写不敏感的精确匹配，命中即返回 true；
//   - 均未命中返回 false。
//
// 该函数是纯函数：strings.EqualFold 按 Unicode 码点折叠，CJK 等多字节字符
// 不会被切断或误判；不读时钟、不做 IO、不依赖区域设置或全局状态，对同样的
// 输入永远给出同样的结果（稳定）。匹配是「精确 + 大小写不敏感」：不做前后
// 空白裁剪、不做子串匹配、不解析 URL——这些都属于调用方的职责。
func corsAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	for _, item := range allowed {
		if item == "*" {
			return true
		}
		if strings.EqualFold(origin, item) {
			return true
		}
	}
	return false
}

// corsMiddleware 用 h.cfg.AllowedOrigins 包裹 next：
//   - 只要请求携带 Origin，就追加 Vary: Origin，使缓存按来源区分，避免把
//     放行 A 的响应错发给 B；
//   - 对允许的来源回显 Access-Control-Allow-Origin（回显具体来源而非 "*"，
//     对将来可能携带凭据的请求也更安全）；
//   - 对「带 Access-Control-Request-Method 的 OPTIONS」这一浏览器预检形态，
//     允许的来源直接以 204 结束并补齐 Allow-Methods / Allow-Headers /
//     Max-Age；未允许的来源以 403 拒绝；
//   - 其它请求（包括没有预检特征的 OPTIONS）原样交给 next。
func (h *handler) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		allowed := corsAllowed(origin, h.cfg.AllowedOrigins)
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		// 只有同时带 Origin 与 Access-Control-Request-Method 的 OPTIONS
		// 才是浏览器预检；缺其一的 OPTIONS 只是普通的方法探测，交给路由
		// 按原有的 405 逻辑处理，避免改变既有行为。
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
				w.Header().Set("Access-Control-Allow-Headers", requested)
			}
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
