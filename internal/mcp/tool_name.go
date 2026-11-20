package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxToolNameBytes 是面向模型的工具名允许占用的最大字节数。
// 工具名会随每次请求一起发给模型，过长会浪费上下文，也可能触发提供方的名称长度上限。
const MaxToolNameBytes = 128

// toolNameHashHexLen 是重名时附加的 sha256 摘要长度（十六进制字符数）。
// 8 个十六进制字符提供 32 位区分度，足以覆盖现实中的工具数量。
const toolNameHashHexLen = 8

// normalizeToolName 把 server/tool 归一化为可安全展示给模型的工具名。
//
// 处理顺序固定：先用 Namespaced 加命名空间，再把非安全字符替换为下划线，
// 然后按 rune 边界截断到 MaxToolNameBytes，最后在发生重名时附加由原始
// server/tool 计算出的稳定哈希后缀。used 记录已经被占用的名字：相同输入必然
// 得到相同结果（确定性），后到者通过哈希后缀消解冲突，而不再被直接丢弃。
// used 为 nil 时不记录占用，仍返回归一化后的名字。
func normalizeToolName(server, tool string, used map[string]bool) string {
	base := sanitizeToolName(Namespaced(server, tool))
	base = truncateToolName(base, MaxToolNameBytes)
	if base == "" {
		base = "tool" // 极端输入下的兜底，保证名字永不为空
	}

	name := base
	if used != nil && used[name] {
		suffix := "_" + stableToolNameHash(server, tool)
		// 截断基名给后缀腾出空间，确保加上后缀后仍不越界。
		room := MaxToolNameBytes - len(suffix)
		if room < 0 {
			room = 0
		}
		name = truncateToolName(base, room) + suffix
		// sha256 前缀理论上仍可能碰撞；用确定性计数器彻底兜底，且保证结果可复现。
		for nonce := 1; used[name]; nonce++ {
			longer := "_" + stableToolNameHash(server, tool) + "_" + strconv.Itoa(nonce)
			room = MaxToolNameBytes - len(longer)
			if room < 0 {
				room = 0
			}
			name = truncateToolName(base, room) + longer
		}
	}
	if used != nil {
		used[name] = true
	}
	return name
}

// NormalizeToolName 是 normalizeToolName 的导出入口，供 internal/app 等包在注册 MCP
// 工具时消解面向模型的重名。原始协议名保留在 Tool.RawName 中，调用服务器时仍用它，
// 归一化只影响模型看到的名字。
func NormalizeToolName(server, tool string, used map[string]bool) string {
	return normalizeToolName(server, tool, used)
}

// sanitizeToolName 把工具名里不安全的字符替换为下划线。
// 保留 ASCII 字母数字、下划线和连字符；Unicode 字母与数字（含 CJK）也保留，
// 其余字符（空格、标点、控制符等）一律替换为下划线。
func sanitizeToolName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// truncateToolName 按 UTF-8 rune 边界把 s 截断到不超过 maxBytes 字节，
// 绝不会把一个多字节字符切成两半。
func truncateToolName(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	used := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		if used+size > maxBytes {
			return s[:i]
		}
		used += size
		i += size
	}
	return s
}

// stableToolNameHash 根据原始 server/tool 计算稳定的十六进制摘要前缀。
// 用 NUL 分隔两个字段，避免 ("ab","c") 与 ("a","bc") 产生同样的哈希；
// 哈希基于原始输入而非归一化后的名字，因此不同原文即使归一化后相同也能区分。
func stableToolNameHash(server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return hex.EncodeToString(sum[:])[:toolNameHashHexLen]
}
