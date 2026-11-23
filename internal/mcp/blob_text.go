package mcp

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// 本文件补上 goose 多模态审计指出的最后一块：MCP 嵌入资源的 blob
// 在落盘之前应先尝试当文本用。许多服务器会把日志、JSON、源码等
// 以 text/plain（甚至 application/json）的 blob 形式返回；旧的
// CallTool 只读取 resource.text，因此这类文本 blob 完全丢失。
//
// 核心是纯函数 blobAsText：合法的 UTF-8 且不含 NUL 的字节按原样
// 作为文本返回；其余（真正的二进制）输出安全的占位符
// "[Binary content (<mime>) - N bytes]"。mime 来自不可信服务器，
// 进入占位符前必须经过白名单净化，防止换行、方括号或控制字符
// 注入模型上下文。
//
// 接线默认开启：CallTool 把文本型 blob 当文本渲染，仅显式设置
// ARGENT_MCP_BLOB_TEXT 为假值词（0/false/no/off）时关闭，回到历史行为。

const (
	// blobTextEnv 是 blob 文本渲染的门控开关，默认开启、假值词关闭。
	blobTextEnv = "ARGENT_MCP_BLOB_TEXT"

	// maxBlobMimeDisplayLen 限制占位符中 mime 的显示长度，
	// 防止服务器用超长 mime 淹没上下文。
	maxBlobMimeDisplayLen = 64
)

// blobAsText 把资源 blob 解码后的字节渲染为模型可读文本。
// 它是纯函数：无 I/O、无全局状态，同一输入恒定返回同一输出。
//
// 判定规则：
//   - data 是合法 UTF-8 且不含 NUL 字节时，按原样返回其文本
//     （CJK 等多字节字符天然合法，会原样保留，不会被误判为二进制）；
//   - 否则返回二进制占位符，其中 mime 经 sanitizeBlobMime 净化，
//     N 为解码后的字节数。
func blobAsText(data []byte, mime string) string {
	if utf8.Valid(data) && bytes.IndexByte(data, 0) < 0 {
		return string(data)
	}
	return binaryContentPlaceholder(mime, len(data))
}

// binaryContentPlaceholder 渲染统一的二进制占位符。
// mime 总是经过净化，n 为解码后的字节数（解码失败时调用方传 0）。
func binaryContentPlaceholder(mime string, n int) string {
	return fmt.Sprintf("[Binary content (%s) - %d bytes]", sanitizeBlobMime(mime), n)
}

// sanitizeBlobMime 把不可信的 mime 类型净化为可安全嵌入占位符的 ASCII 串。
// 它先去掉 ";" 之后的参数、去首尾空白，再逐字符过滤：仅保留
// ASCII 字母、数字和 "/ + - . _"，其余字符（含换行、NUL、方括号、
// CJK 和控制字符）统一替换为 "_"。空结果回退为 "unknown"
// （与 image 内容块的占位符保持一致）。输出最长 maxBlobMimeDisplayLen
// 字节，杜绝超长 mime 注入。
func sanitizeBlobMime(mime string) string {
	base := mime
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range base {
		if b.Len() >= maxBlobMimeDisplayLen {
			break
		}
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9',
			r == '/', r == '+', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// blobTextEnabled 报告是否启用 blob 文本渲染。默认开启：仅当环境变量为
// 假值词（0/false/no/off，忽略大小写与首尾空白）时关闭，以保留用户显式
// 关掉的意图；未设置、空串及其它值一律启用。
func blobTextEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(blobTextEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// blobTextFromResource 是 CallTool 侧的适配器：解码嵌入资源的
// blob 后交给 blobAsText 渲染。非法 base64 不会让调用失败——降级为
// 安全的二进制占位符（字节数记 0），与既有产物写入的静默失败一致。
func blobTextFromResource(resource ResourceContent) string {
	data, mime, err := DecodeResourceBlob(resource)
	if err != nil {
		return binaryContentPlaceholder(mime, 0)
	}
	return blobAsText(data, mime)
}
