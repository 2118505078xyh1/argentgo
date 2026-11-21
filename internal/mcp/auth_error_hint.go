package mcp

import (
	"net/url"
	"strings"
	"unicode"
)

// 本文件把 HTTP 401/403 响应映射为面向模型与用户的中文认证提示
// （needs-auth）。codex 审计发现：MCP 服务器返回 401/403 时，旧实现
// 只把裸错误文本交给上层，既不说明这是认证问题，也不给出更新凭据的
// 指引。这里提供一个纯函数来完成映射，便于在任意传输层复用。

const (
	authHintStatusUnauthorized = 401 // HTTP 401：未授权，凭据缺失或无效
	authHintStatusForbidden    = 403 // HTTP 403：禁止访问，凭据有效但权限不足

	// authHintMaxBodyScan 限制只扫描 body 的前若干字节。响应体可能由
	// 不可信服务器提供且体积巨大，限定窗口防止超长输入拖慢调用。
	authHintMaxBodyScan = 4096
	// authHintMaxURLLen 限制单条脱敏后 URL 的最大 rune 数。
	authHintMaxURLLen = 256
	// authHintMaxTotalLen 限制整条提示的最大 rune 数，为最终产物兜底。
	authHintMaxTotalLen = 512
)

// authErrorHint 把 HTTP 状态码映射为认证提示。
//
// 仅 401 / 403 返回非空提示（均带 "needs-auth" 标记），其它状态码一律
// 返回空串，调用方据此决定是否为认证失败走单独的提示通道。
//
// body 若含 URL，会被提取、脱敏后附在提示末尾，帮助用户定位需要更新的
// 认证配置；脱敏会移除 userinfo（可能含密码）、片段（可能含 token）以及
// 名称敏感的查询参数。若 body 中没有合法 URL，则只返回基础提示，绝不
// 回显任意响应体文本，避免把服务器回显的敏感信息带入模型上下文。
//
// 该函数是纯函数：无 I/O、无全局状态，相同输入恒定返回相同输出；且对
// 超长、畸形 body 有界，截断均发生在 rune 边界，不会切坏 CJK 字符。
func authErrorHint(statusCode int, body string) string {
	var base string
	switch statusCode {
	case authHintStatusUnauthorized:
		base = "认证失败（needs-auth）：HTTP 401，凭据缺失或无效；请更新认证配置（API Key / Token）后重试。"
	case authHintStatusForbidden:
		base = "认证失败（needs-auth）：HTTP 403，凭据已验证但权限不足或访问被拒绝；请检查账号权限与授权范围后重试。"
	default:
		return ""
	}
	if link := firstSafeURL(body); link != "" {
		base += " 参考地址：" + link
	}
	return truncateRunes(base, authHintMaxTotalLen)
}

// firstSafeURL 从 body 中提取第一个合法 URL 并脱敏。
// 只检查 body 的前 authHintMaxBodyScan 字节；找不到合法 URL 时返回空串。
func firstSafeURL(body string) string {
	// 先丢弃非法 UTF-8 字节，保证后续按 rune 扫描与截断不会切坏字符。
	window := strings.ToValidUTF8(body, "")
	if len(window) > authHintMaxBodyScan {
		window = window[:authHintMaxBodyScan]
	}

	// 找出两种前缀中最靠前的一次出现。
	best := -1
	for _, prefix := range []string{"https://", "http://"} {
		if i := strings.Index(window, prefix); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}

	// 从前缀起点延伸到分隔符为止。
	rest := window[best:]
	end := len(rest)
	for i, r := range rest {
		if isURLDelimiter(r) {
			end = i
			break
		}
	}
	raw := strings.TrimRightFunc(rest[:end], isTrailingURLPunct)
	if raw == "" {
		return ""
	}
	return redactURL(raw)
}

// isURLDelimiter 报告 r 是否是 URL 文本的分隔符。
// 只包含几乎不会出现在 URL 中的空白、控制字符与引号类字符；括号等
// 可能合法出现在 URL 中的字符交给 isTrailingURLPunct 做尾部清理。
func isURLDelimiter(r rune) bool {
	if unicode.IsSpace(r) || unicode.IsControl(r) {
		return true
	}
	switch r {
	case '"', '\'', '`', '<', '>', '\\':
		return true
	default:
		return false
	}
}

// isTrailingURLPunct 报告 r 是否是应从 URL 尾部去掉的标点。
// 同时覆盖 ASCII 与常见 CJK 标点，使中文句子中的 URL 不被拖尾标点污染。
func isTrailingURLPunct(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '!', '?', ')', ']', '}', '>', '、':
		return true
	case '。', '，', '；', '：', '！', '？', '）', '】', '》', '」', '』':
		return true
	default:
		return false
	}
}

// redactURL 解析并脱敏一条 URL，失败时返回空串。
//
// 移除内容：userinfo（user:pass@）、片段（#...），以及名称敏感的查询
// 参数（中英文关键字）。主机名、路径与非敏感查询参数会保留，方便定位
// 服务器；最终按 authHintMaxURLLen 截断。
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	// userinfo 常含明文密码，绝不保留。
	u.User = nil
	// 片段常作为隐式凭据载体（如 #access_token=...），直接移除。
	u.Fragment = ""
	u.RawFragment = ""

	// 过滤敏感查询参数。values.Encode() 会重新编码，天然保证输出合法。
	if u.RawQuery != "" {
		values := u.Query()
		for key := range values {
			if isSensitiveQueryKey(key) {
				values.Del(key)
			}
		}
		u.RawQuery = values.Encode()
	}

	return truncateRunes(u.String(), authHintMaxURLLen)
}

// isSensitiveQueryKey 报告查询参数名是否暗示其值含凭据。
// 用大小写不敏感的子串匹配，兼顾常见英文与 CJK 命名。
func isSensitiveQueryKey(key string) bool {
	k := strings.ToLower(key)
	for _, needle := range []string{
		"token", "secret", "password", "passwd", "pwd", "key",
		"code", "auth", "session", "sig", "credential", "apikey",
		"令牌", "密钥", "密码", "口令", "凭证",
	} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// truncateRunes 按 rune 把 s 截断到最多 max 个 rune。
// 切点始终落在 rune 边界，因此 CJK 等多字节字符不会被切在中间。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
