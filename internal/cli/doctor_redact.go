package cli

import (
	"strings"

	"example.com/argentgo/internal/security"
)

// doctor 的输出经常被复制到公开 issue 中。一条 detail 可能内嵌配置路径、
// 带有内联 `user:pass@` 凭证的 base URL，或引用其中任一者的错误字符串，
// 因此每条 detail 在打印前都会被清洗。
const (
	// doctorDetailMaxRunes 限制单条 detail 行的长度。它故意设得很宽松：
	// 普通的 doctor 输出（路径、简短错误、MCP 问题列表）逐字节保持不变，
	// 只有病态的负载才会被截断。
	doctorDetailMaxRunes = 4096
	// doctorDetailTruncated 代替被丢弃的尾部追加在末尾。
	doctorDetailTruncated = " ...[truncated]"
)

// redactDoctorDetail 使一条 doctor detail 可以安全地粘贴到 bug 报告中。
// 它是一个没有 I/O 和进程状态的纯函数。各步骤的顺序保证脱敏占位符本身
// 绝不会被切断：先去掉 URL 的 userinfo，再脱敏剩余的凭证形状，最后按 rune 截断。
func redactDoctorDetail(s string) string {
	if s == "" {
		return s
	}
	s = stripDoctorURLUserInfo(s)
	s = security.Redact(s)
	return truncateDoctorRunes(s, doctorDetailMaxRunes)
}

// stripDoctorURLUserInfo 丢弃 s 中每个 URL 的 `user:pass@` 部分，但保留
// scheme、主机、端口、路径和查询，使脱敏后的 base URL 仍能告诉用户哪个
// 端点失败了。只有当 userinfo 包含密码（最后一个 `@` 之前有 `:`）时才去掉，
// 这样裸的 `user@host` 地址和 URL 路径中的 `@` 都不会被改动。
func stripDoctorURLUserInfo(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	rest := s
	for {
		sep := strings.Index(rest, "://")
		if sep < 0 {
			out.WriteString(rest)
			return out.String()
		}

		// 原样复制 scheme 和 "://"。scheme 必须以 ASCII 字母开头，且只包含
		// RFC 3986 规定的 scheme 字节；否则这不是 URL，在继续扫描前
		// 原样复制它。
		schemeStart := sep
		for schemeStart > 0 && isDoctorURLSchemeByte(rest[schemeStart-1]) {
			schemeStart--
		}
		validScheme := schemeStart < sep && isDoctorASCIIAlpha(rest[schemeStart])
		out.WriteString(rest[:sep+3])
		rest = rest[sep+3:]
		if !validScheme {
			continue
		}

		// authority 在第一个路径/查询/片段或引用字节处结束。
		// 索引都是 ASCII 字节偏移，因此切片是 rune 安全的。
		end := 0
		for end < len(rest) && !isDoctorAuthorityDelimiter(rest[end]) {
			end++
		}
		authority := rest[:end]
		// 最后一个 @ 分隔 userinfo 和主机：密码本身可能包含未转义的 @。
		if at := strings.LastIndexByte(authority, '@'); at > 0 && strings.Contains(authority[:at], ":") {
			out.WriteString(authority[at+1:])
		} else {
			out.WriteString(authority)
		}
		rest = rest[end:]
	}
}

// isDoctorURLSchemeByte 报告 ch 是否可以出现在 URL scheme 的首字母之后
// （RFC 3986：ALPHA / DIGIT / "+" / "-" / "."）。
func isDoctorURLSchemeByte(ch byte) bool {
	return isDoctorASCIIAlpha(ch) || ch >= '0' && ch <= '9' || ch == '+' || ch == '-' || ch == '.'
}

func isDoctorASCIIAlpha(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// isDoctorAuthorityDelimiter 标记 URL authority 的结束。错误字符串常常
// 引用 URL，因此引用字符也属于分隔符。
func isDoctorAuthorityDelimiter(ch byte) bool {
	switch ch {
	case '/', '?', '#', ' ', '\t', '\n', '\r', '"', '\'', '`':
		return true
	}
	return ch < 0x20 || ch == 0x7f
}

// truncateDoctorRunes 最多保留 max 个 rune，并在不得不丢弃内容时追加标记。
// 遍历字符串会得到位于 rune 边界的字节偏移，因此多字节的中文 rune 绝不会被切开。
func truncateDoctorRunes(s string, max int) string {
	count := 0
	for i := range s {
		if count == max {
			return s[:i] + doctorDetailTruncated
		}
		count++
	}
	return s
}
