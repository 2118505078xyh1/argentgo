package server

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// 本文件提供 W3C Trace Context（traceparent）的纯解析与生成函数。
// 它们不触碰请求头、不修改 handler，也不依赖任何 transport 状态：
// 目的是先把可独立验证的能力落地，接线留到下游（提供方调用）具备
// 传播能力时再做，避免在没准备好时改变现有线协议与测试基线。

// parseTraceparent 解析 W3C traceparent 请求头，提取 traceID 与 spanID。
//
// 合法格式为 version-traceID-spanID-flags：
//   - version 为两位十六进制，保留值 ff 视为非法（大小写不敏感）；
//   - traceID 为 32 位十六进制且不能全零；
//   - spanID 为 16 位十六进制且不能全零；
//   - flags 为两位十六进制。
//
// 版本 00 必须恰好四段；更高版本允许尾部附加字段（W3C 规定接收方
// 应忽略其不认识的附加段）。任何非十六进制字符（包括 CJK 等 Unicode
// 字符）都会被拒绝。返回的 ID 统一为小写十六进制的规范形式。
func parseTraceparent(header string) (traceID, spanID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) < 4 {
		return "", "", false
	}
	version, rawTrace, rawSpan, rawFlags := parts[0], parts[1], parts[2], parts[3]

	// 版本 00 的 traceparent 不允许附加字段。
	if version == "00" && len(parts) != 4 {
		return "", "", false
	}
	// ff 是 W3C 保留的非法版本；EqualFold 使 "FF" 同样被拒绝。
	if !validHex(version, 2) || strings.EqualFold(version, "ff") {
		return "", "", false
	}
	if !validHex(rawTrace, 32) || allZero(rawTrace) {
		return "", "", false
	}
	if !validHex(rawSpan, 16) || allZero(rawSpan) {
		return "", "", false
	}
	if !validHex(rawFlags, 2) {
		return "", "", false
	}
	return strings.ToLower(rawTrace), strings.ToLower(rawSpan), true
}

// formatTraceparent 生成合法的 W3C traceparent 值。
//
// traceID 必须是 32 位十六进制、spanID 必须是 16 位十六进制，二者
// 都不能全零；sampled 决定 trace-flags 的最低有效位（采样标志）。
// 生成值固定使用版本 00，输出为小写十六进制。任一输入非法时返回
// 空字符串，保证调用方永远不会发出无效的头。
func formatTraceparent(traceID, spanID string, sampled bool) string {
	if !validHex(traceID, 32) || allZero(traceID) {
		return ""
	}
	if !validHex(spanID, 16) || allZero(spanID) {
		return ""
	}
	var flags byte
	if sampled {
		flags = 0x01
	}
	return fmt.Sprintf("00-%s-%s-%02x", strings.ToLower(traceID), strings.ToLower(spanID), flags)
}

// validHex 报告 s 是否恰好是 n 个十六进制字符。
// hex.DecodeString 只接受 [0-9a-fA-F]，因此长度不符、非 ASCII 字符
// （例如 CJK）以及任何非法字符都会在这里被直接拒绝。
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// allZero 报告十六进制字符串是否全为零字符。W3C 规定 traceID 与
// spanID 都不能是全零值。调用前应先经 validHex 校验。
func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
