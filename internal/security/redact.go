package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Redact 把具有凭据形状的子串替换为占位符。它刻意保守：误报的代价只是一段
// 字符串被脱敏，而漏报则会泄露密钥。
func Redact(text string) string {
	return string(redactBytes(text))
}

// RedactRow 对一行输出中具有凭据形状的片段脱敏，其余每个字节都保持不变。
// 这包括 ANSI 转义序列：只替换匹配的片段，使颜色和光标序列相对于周围文本的
// 位置完全不变，因此终端布局不受影响。它使用与 Redact 相同的模式表。
func RedactRow(line []byte) []byte {
	if len(line) == 0 {
		return line
	}
	return redactBytes(string(line))
}

// redactBytes 把每个秘密片段替换为其占位符，并逐字复制其余所有字节。
// 它由 Redact 和 RedactRow 共享，因此只有一份模式表和一套替换策略。
func redactBytes(text string) []byte {
	out := make([]byte, 0, len(text))
	rest := text
	for rest != "" {
		match := findSecret(rest)
		if match == nil || match.end <= match.start || match.end > len(rest) {
			out = append(out, rest...)
			break
		}
		out = append(out, rest[:match.start]...)
		out = append(out, match.placeholder...)
		rest = rest[match.end:]
	}
	return out
}

type secretMatch struct {
	start       int
	end         int
	placeholder string
}

var tokenPrefixes = []struct {
	prefix string
	kind   string
}{
	{"sk-proj-", "api key"},
	{"sk-ant-", "api key"},
	{"sk-or-", "api key"},
	{"sk-", "api key"},
	{"ghp_", "token"},
	{"gho_", "token"},
	{"ghu_", "token"},
	{"ghs_", "token"},
	{"github_pat_", "token"},
	{"AKIA", "aws key"},
	{"ASIA", "aws key"},
	{"AIza", "google key"},
	{"xoxa-", "slack token"},
	{"xoxb-", "slack token"},
	{"xoxp-", "slack token"},
	{"xoxs-", "slack token"},
	{"r8_", "token"},
	{"hf_", "token"},
	{"glpat-", "token"},
	{"npm_", "token"},
	{"pypi-", "token"},
	{"eyJ", "jwt"},
	// Stripe 密钥在 sk/rk 后使用下划线；上面的带连字符 `sk-` 无法匹配它们。
	// Google OAuth2 访问令牌以 ya29 开头。
	{"sk_live_", "api key"},
	{"sk_test_", "api key"},
	{"rk_live_", "api key"},
	{"rk_test_", "api key"},
	{"ya29.", "oauth token"},
}

var keyValueNames = []struct {
	needle string
	kind   string
}{
	{"api_key", "api key"},
	{"apikey", "api key"},
	{"api-key", "api key"},
	{"secret", "secret"},
	{"password", "password"},
	{"passwd", "password"},
	{"token", "token"},
	{"access_key", "access key"},
	{"private_key", "private key"},
}

// 搜索串是全小写的，并与 asciiLower(text) 匹配：HTTP 头名和认证方案名不区分
// 大小写（RFC 7230/7235），因此 `AUTHORIZATION: Basic ...` 和 `BEARER ...`
// 与其规范写法一样有效。过去只列出两种精确写法会让整个凭据泄露。asciiLower
// 保持字节偏移不变，因此在小写副本中找到的偏移仍然可以索引原始文本。
var headerPrefixes = []struct {
	needle string
	kind   string
}{
	{"bearer ", "bearer token"},
	{"authorization: ", "auth header"},
	// HTTP 允许头冒号后有可选空白（OWS），因此 `Authorization:Basic <b64>`
	// 也是合法的头。上面带空格的搜索串完全匹配不到这种写法；如果没有这一条，
	// 整个凭据就会泄露，因为裸 base64 token 本身不具有凭据形状。
	// authValueEnd/skipHorizontalSpace 对两种写法一视同仁，因此带空格的头
	// 仍然只产生一次相同的脱敏。
	{"authorization:", "auth header"},
}

// PEM 私钥通过其标志明确的装甲头携带其形状。块形式还会清洗 base64 正文；
// 当工具逐行打印文件时，头形式让 RedactRow 仍然有效。只匹配 PRIVATE KEY 装甲，
// 因此公钥和证书保持不变。
var (
	pemPrivateKeyBlockRe  = regexp.MustCompile(`(?is)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----.*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	pemPrivateKeyHeaderRe = regexp.MustCompile(`(?i)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)
)

func isAuthorizationNeedle(needle string) bool {
	return strings.HasPrefix(asciiLower(needle), "authorization")
}

func skipHorizontalSpace(text string, at int) int {
	for at < len(text) && (text[at] == ' ' || text[at] == '\t') {
		at++
	}
	return at
}

// authValueEnd 返回完整 Authorization 值的结束位置：方案词加上其后的凭据词。
// 引号和其他分隔符会结束该值，因此周围的 shell/JSON 语法保持原样。
func authValueEnd(text string, start int) int {
	end := start
	for end < len(text) && !isHeaderValueDelimiter(text[end]) {
		end++
	}
	gap := skipHorizontalSpace(text, end)
	if gap > end && gap < len(text) {
		credEnd := gap
		if ch := text[credEnd]; ch == '"' || ch == '\'' {
			if close := strings.IndexByte(text[credEnd+1:], ch); close >= 0 {
				return credEnd + close + 2
			}
		}
		for credEnd < len(text) && !isHeaderValueDelimiter(text[credEnd]) {
			credEnd++
		}
		if credEnd > gap {
			end = credEnd
		}
	}
	return end
}

func isHeaderValueDelimiter(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', ',', ';', '<', '>':
		return true
	}
	return false
}

func isAlpha(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func isURLSchemeByte(ch byte) bool {
	return isAlnum(ch) || ch == '+' || ch == '-' || ch == '.'
}

// findSecret 返回最早的具有凭据形状的子串（如果有）。
func findSecret(text string) *secretMatch {
	lowered := asciiLower(text)
	best := (*secretMatch)(nil)
	consider := func(start, end int, kind string) {
		if end <= start || end > len(text) {
			return
		}
		if best == nil || start < best.start || (start == best.start && end > best.end) {
			best = &secretMatch{start: start, end: end, placeholder: placeholder(kind)}
		}
	}
	for _, entry := range tokenPrefixes {
		for from := 0; from < len(lowered); {
			offset := strings.Index(lowered[from:], strings.ToLower(entry.prefix))
			if offset < 0 {
				break
			}
			start := from + offset
			consider(start, tokenEnd(text, start), entry.kind)
			from = start + len(entry.prefix)
		}
	}
	// 同一位置起始时，完整的 PEM 块优先于其自身的头（保留更长的结束位置）；
	// 而没有尾部 END 标记的头仍会被清洗，这样被截断的密钥也不会泄露出去。
	for _, loc := range pemPrivateKeyBlockRe.FindAllStringIndex(text, -1) {
		consider(loc[0], loc[1], "private key")
	}
	for _, loc := range pemPrivateKeyHeaderRe.FindAllStringIndex(text, -1) {
		consider(loc[0], loc[1], "private key")
	}
	for _, entry := range headerPrefixes {
		// 在小写副本中匹配：头名和方案名不区分大小写。由于 asciiLower 是逐字节的，
		// 偏移仍然有效。
		offset := strings.Index(lowered, entry.needle)
		if offset < 0 {
			continue
		}
		start := offset + len(entry.needle)
		if isAuthorizationNeedle(entry.needle) {
			// 每种 Authorization 方案（Bearer、Basic、Mac、未知……）都把凭据放在冒号后的
			// 值里。把方案连同其 token 一起脱敏，这样跟在 `Basic ` 后面的 base64
			// 凭据就不会因为 tokenEnd 遇空格停止而残留。
			start = skipHorizontalSpace(text, start)
			consider(start, authValueEnd(text, start), entry.kind)
			continue
		}
		consider(start, tokenEnd(text, start), entry.kind)
	}
	// `scheme://user:pass@host` 把凭据放在 URL 的 userinfo 中。冒号和 @ 是
	// tokenEnd 的分隔符，因此需要专门的规则：清洗 `user:pass@`，保留方案和主机
	// 不变。
	for from := 0; from < len(text); {
		sep := strings.Index(text[from:], "://")
		if sep < 0 {
			break
		}
		schemeEnd := from + sep
		schemeStart := schemeEnd
		for schemeStart > 0 && isURLSchemeByte(text[schemeStart-1]) {
			schemeStart--
		}
		if schemeStart < schemeEnd && isAlpha(text[schemeStart]) {
			infoStart := schemeEnd + 3
			limit := infoStart
			for limit < len(text) {
				ch := text[limit]
				if ch == '/' || ch == '?' || ch == '#' || isHeaderValueDelimiter(ch) {
					break
				}
				limit++
			}
			authority := text[infoStart:limit]
			// 授权部分中最后一个 @ 是 userinfo/host 的分隔符；
			// 密码本身可能包含未转义的 @。
			if at := strings.LastIndexByte(authority, '@'); at > 0 && strings.Contains(authority[:at], ":") {
				consider(infoStart, infoStart+at+1, "url credentials")
			}
		}
		from = schemeEnd + 3
	}
	for _, entry := range keyValueNames {
		for from := 0; from < len(lowered); {
			offset := strings.Index(lowered[from:], entry.needle)
			if offset < 0 {
				break
			}
			start := from + offset
			after := text[start+len(entry.needle):]
			trimmed := strings.TrimLeft(after, " \t")
			gap := len(after) - len(trimmed)
			if eq := strings.IndexAny(trimmed, "=:"); eq >= 0 && eq <= 3 {
				valueStart := start + len(entry.needle) + gap + eq + 1
				value := text[valueStart:]
				valueTrimmed := strings.TrimLeft(value, " \t\"'")
				valueStart += len(value) - len(valueTrimmed)
				consider(valueStart, tokenEnd(text, valueStart), entry.kind)
			}
			from = start + len(entry.needle)
		}
	}
	return best
}

// tokenEnd 找到具有凭据形状的连续串的结束位置。Base64 凭据带有 `+`、`/` 和
// `=` 填充，后面可能跟着 `:` 或 `@`（例如在 URL 中），因此整个连续串会作为
// 一个片段被消耗。空白、ANSI 转义等控制字节以及结构性标点仍会将其分隔。
func tokenEnd(text string, start int) int {
	end := start
	for end < len(text) {
		ch := text[end]
		if isAlnum(ch) || ch == '.' || ch == '-' || ch == '_' || ch == '/' || ch == '+' || ch == '=' ||
			ch == ':' || ch == '@' || ch == '%' || ch == '~' {
			end++
			continue
		}
		break
	}
	return end
}

func isAlnum(ch byte) bool {
	return ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// asciiLower 只把 ASCII 字母转为小写，因此字节偏移保持有效。
func asciiLower(text string) string {
	out := make([]byte, len(text))
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out[i] = ch
	}
	return string(out)
}

func placeholder(kind string) string {
	return "[redacted:" + kind + "]"
}

// 键名闸门必须覆盖 secretKeyKind 分支里出现的每一种写法。此前这里漏了 passwd，
// 而下面的 switch 又显式处理了它，结果 {"passwd":"..."} 这类参数以明文写入审计
// 日志：RedactJSON 只在键名被识别后才把整个子树标记为机密。
var secretKeyPattern = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|authorization)`)

// secretKeyKind 报告对象键是否命名了凭据。
func secretKeyKind(key string) string {
	if !secretKeyPattern.MatchString(key) {
		return ""
	}
	normalized := asciiLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(normalized, "authorization"):
		return "auth header"
	case strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd"):
		return "password"
	case strings.Contains(normalized, "token"):
		return "token"
	case strings.Contains(normalized, "secret"):
		return "secret"
	case strings.Contains(normalized, "key"):
		return "api key"
	}
	return "secret"
}

// pathCredentialSegment 匹配名称中带有凭据的路径组件，例如
// `/repo/token-sk-abcdef/.env` 或 `\repo\api_key=abc123\`。
var pathCredentialSegment = regexp.MustCompile(`(?i)(^|[\\/])((?:api[_-]?key|token|secret|password|passwd)[-_=])[A-Za-z0-9._~+/=-]{4,}`)

// RedactPath 在路径进入审计记录之前对其进行清洗：凭据形状会被替换，
// 用户的主目录会被折叠为 `~`。
func RedactPath(path string) string {
	path = Redact(path)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, prefix := range []string{home, filepath.ToSlash(home)} {
			if strings.HasPrefix(path, prefix) {
				path = "~" + strings.TrimPrefix(path, prefix)
				break
			}
		}
	}
	return pathCredentialSegment.ReplaceAllString(path, "${1}${2}[redacted:path credential]")
}

// RedactManifestPaths 为恢复或 checkpoint 动作的路径字段返回脱敏后的 JSON。
// 其他字段保持不变；审计日志会在其上再执行通常的 RedactJSON。
func RedactManifestPaths(raw json.RawMessage) json.RawMessage {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	object, ok := value.(map[string]any)
	if !ok {
		return raw
	}
	if list, ok := object["paths"].([]any); ok {
		for i, item := range list {
			if text, ok := item.(string); ok {
				list[i] = RedactPath(text)
			}
		}
	}
	for _, key := range []string{"path", "file", "dir", "directory", "target"} {
		if text, ok := object[key].(string); ok {
			object[key] = RedactPath(text)
		}
	}
	out, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return out
}

// RedactJSON 在不扁平化的情况下对 JSON 文档脱敏：以凭据命名的键下的值会变成
// 占位符，其他每个字符串都用 Redact 清洗。无效 JSON 回退为文本脱敏。
func RedactJSON(raw []byte) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Redact(string(raw))
	}
	redacted, err := json.Marshal(redactValue(value, ""))
	if err != nil {
		return Redact(string(raw))
	}
	return string(redacted)
}

func redactValue(value any, secretKind string) any {
	switch typed := value.(type) {
	case string:
		if secretKind != "" {
			return placeholder(secretKind)
		}
		return Redact(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			// 以秘密命名的键会把其整个子树标记为秘密。把这个上下文向下传递，
			// 这样嵌套在其中的字符串不会因为自己的键看起来无害就漏过去
			// （例如 {"secret":{"note":...}}）。直接命名秘密的键优先，
			// 保留更具体的种类。
			kind := secretKeyKind(key)
			if kind == "" {
				kind = secretKind
			}
			out[key] = redactValue(inner, kind)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			// 数组元素没有自己的键，因此它们继承外层的秘密上下文。没有这一点，
			// {"tokens":["hunter2"]} 会泄露每个元素：Redact 只捕获具有凭据形状的
			// 字符串，而任意秘密并不一定具有凭据形状。
			out[i] = redactValue(inner, secretKind)
		}
		return out
	default:
		// 数字、布尔和 null 也可能承载凭据（PIN、数字令牌、OTP 等）。若只替换
		// 字符串而原样保留标量，审计日志就会在凭据名下泄露这些值，违背“写入
		// 审计日志前先脱敏”的约束。容器已在上面递归处理并继承 secretKind，
		// 这里的默认分支只可能是标量，因此一律替换为占位符。
		if secretKind != "" {
			return placeholder(secretKind)
		}
		return value
	}
}
