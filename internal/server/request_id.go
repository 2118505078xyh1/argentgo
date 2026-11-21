package server

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// 本文件把 HTTP X-Request-ID 请求头收敛成一个可独立验证的纯函数。
//
// 背景：关联 ID（correlation ID）用于把同一请求在日志、错误与不同传输
// （HTTP/SSE、JSON-RPC、ACP）中的记录串联起来，便于跨请求追踪。这里先
// 把「规范化提取」能力独立落地：它不读取 *http.Request、不修改 handler、
// 不触碰全局状态，因此可在不启动服务器的情况下验证，也能被各传输复用。
// 接线是可选的，本文件只提供能力，不改变现有线协议与测试基线。

const (
	// requestIDHeaderName 是携带关联 ID 的请求头名。
	requestIDHeaderName = "X-Request-ID"
	// maxRequestIDRunes 是关联 ID 的长度上限，按 Unicode 码点计数而非字节，
	// 保证 CJK 等多字节字符不会被从中间截断。
	maxRequestIDRunes = 128
)

// requestIDFromHeader 从 X-Request-ID 请求头值中提取规范化的关联 ID。
// 规范化规则如下：
//   - 先用 strings.TrimSpace 去掉首尾空白；空值或仅空白返回空串；
//   - 过滤所有控制字符（unicode.IsControl，覆盖 C0 0x00-0x1F、DEL 0x7F
//     与 C1 0x80-0x9F），无论出现在首尾还是中间；
//   - 按 Unicode 码点处理：CJK 等多字节字符不会被拆开；若整串包含非法
//     UTF-8 字节序列，则视为非法并返回空串（CJK 安全）；
//   - 过滤后长度超过 maxRequestIDRunes 时按码点截断到上限；
//   - 过滤后若没有任何有效字符（例如全是控制字符）则返回空串。
//
// 该函数是纯函数：不读取任何全局状态，不执行 IO，对任意字符串安全。
func requestIDFromHeader(header string) string {
	value := strings.TrimSpace(header)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}

	var b strings.Builder
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		if count == maxRequestIDRunes {
			// 已达上限：按码点停止追加，绝不切断多字节字符。
			break
		}
		b.WriteRune(r)
		count++
	}

	if b.Len() == 0 {
		return ""
	}
	return b.String()
}
