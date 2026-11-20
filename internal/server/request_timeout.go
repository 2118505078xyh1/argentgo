package server

import (
	"errors"
	"strconv"
	"strings"
)

// 本文件把“请求头里的毫秒超时”收敛成一个纯函数。
//
// 背景：审计指出超时应可配置，并按错误类型分类。这里先把解析能力
// 独立落地——它不读取 *http.Request、不修改 handler、不触碰全局状态，
// 因此可被 HTTP/SSE 与其他传输复用，也能在不启动服务器的情况下验证。
// 接线是可选的且默认关闭（见 Config.EnableRequestTimeout），
// 不开启时现有线协议与超时行为完全不变。

const (
	// requestTimeoutHeaderName 是携带单轮超时毫秒数的请求头名。
	requestTimeoutHeaderName = "X-Request-Timeout"
	// maxRequestTimeoutMS 是单轮超时的上限（10 分钟）。超过上限的正数
	// 会被钳制而不是回退默认值：调用方明显想要“尽可能长”，
	// 钳制比拒绝更贴近意图，同时避免把截止时间推到遥远未来。
	maxRequestTimeoutMS = 10 * 60 * 1000
	// defaultRequestTimeoutMS 是启用超时但调用方未给出合法默认值时的兜底。
	defaultRequestTimeoutMS = 30 * 1000
)

// requestTimeoutFromHeader 从请求头值中解析毫秒超时。
//
// 规则：
//   - 空值（含仅空白）回退 defaultMS；
//   - 只接受 ASCII 十进制整数（可带正负号），CJK、全角数字、小数、
//     单位后缀等非法形态一律回退 defaultMS，且绝不会在 UTF-8
//     字符中间截断；
//   - 非正数（含零与负值）回退 defaultMS；
//   - 正数超过 maxRequestTimeoutMS 时钳制到上限；正整数的范围溢出
//     同样视为“极大值”而钳制，负整数溢出则回退 defaultMS；
//   - defaultMS 按原样回退：它是调用方提供的兜底值，
//     本函数不擅自改写它（接线处负责给出合法默认值）。
//
// 该函数是纯函数：不读取任何全局状态，不执行 IO，对任意字符串安全。
func requestTimeoutFromHeader(header string, defaultMS int) int {
	value := strings.TrimSpace(header)
	if value == "" {
		return defaultMS
	}
	// ParseInt 只接受 ASCII 十进制数字与可选符号。多字节字符
	// （例如 CJK）会被当作非法语法直接拒绝，不会拆开 UTF-8 序列。
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		// 正整数超出 int64 范围属于范围溢出，语义上仍是“极大正数”，
		// 钳制到上限；带负号（含溢出）则按负值回退默认值。
		if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(value, "-") {
			return maxRequestTimeoutMS
		}
		return defaultMS
	}
	if n <= 0 {
		return defaultMS
	}
	if n > int64(maxRequestTimeoutMS) {
		return maxRequestTimeoutMS
	}
	return int(n)
}
