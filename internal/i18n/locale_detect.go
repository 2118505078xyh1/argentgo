package i18n

import "strings"

// localeTagMaxLength 在解析前限制不可信环境变量值的长度。
// 比这更长的文本不可能是语言环境标签。
const localeTagMaxLength = 64

// localeEnvPriority 是检测语言环境时环境变量的优先级顺序：
// LANGUAGE > LC_ALL > LANG > LC_MESSAGES。
// LANGUAGE 按 POSIX 约定是冒号分隔的优先级列表，其余变量是单个值。
var localeEnvPriority = [...]string{"LANGUAGE", "LC_ALL", "LANG", "LC_MESSAGES"}

// detectLocale 从环境中选择语言环境，是一个不读写任何全局状态的纯函数。
//
// 它按 localeEnvPriority 的顺序检查各来源，返回第一个能解析为受支持
// 语言环境（ZhCN 或 EnUS）的值。单个值的解析规则见 parseLocaleTag：
// 支持 POSIX 编码/修饰符后缀、下划线或连字符分隔的区域码以及大小写
// 归一（主子标签小写、区域大写）。任何无法解析的值都会被跳过，继续
// 检查下一个来源；直到所有来源都没有有效值时才回退到 DefaultLocale。
// 这样高优先级来源里的垃圾值不会掩盖低优先级来源中的有效设置。
//
// env 不得为 nil；nil 时返回 DefaultLocale。
func detectLocale(env func(string) string) string {
	if env == nil {
		return DefaultLocale
	}
	for _, name := range localeEnvPriority {
		raw := strings.TrimSpace(env(name))
		if raw == "" {
			continue
		}
		candidates := []string{raw}
		if name == "LANGUAGE" {
			// LANGUAGE 是优先级列表，逐项尝试。
			candidates = strings.Split(raw, ":")
		}
		for _, candidate := range candidates {
			if code, ok := parseLocaleTag(candidate); ok {
				return code
			}
		}
	}
	return DefaultLocale
}

// parseLocaleTag 把单个语言环境值解析为受支持的代码。
//
// 解析步骤：去掉编码后缀（".UTF-8"、".GBK" 等）和修饰符后缀
// （"@euro"）；先尝试 Canonical 的闭集别名（含 CJK 别名"中文"）；
// 再解析 "language[_region]" 结构并把主子标签归一为小写、区域
// 归一为大写，例如 "zh_CN" -> "zh-CN"、"EN_us" -> "en-US"。
// POSIX 的 "C"/"POSIX" 中性区域视为显式英语。
//
// 解析只按 ASCII 分隔并只比较 ASCII 结构，因此多字节 CJK 文本会被
// 原样跳过而不会在字节中间被切断，也不会被误判为受支持的语言环境；
// 超长值直接拒绝。返回值 ok 为 false 表示无法识别。
func parseLocaleTag(raw string) (string, bool) {
	token := strings.TrimSpace(raw)
	if token == "" || len(token) > localeTagMaxLength {
		return "", false
	}
	// 编码和修饰符后缀跟在语言标签后，不改变请求的语言。
	if i := strings.IndexAny(token, ".@"); i >= 0 {
		token = strings.TrimSpace(token[:i])
	}
	if token == "" {
		return "", false
	}
	// 闭集别名优先，覆盖 Canonical 识别的 CJK 名称等写法。
	if code, ok := Canonical(token); ok {
		return code, true
	}
	// POSIX 中性区域等价于显式英语。
	switch asciiLower(token) {
	case "c", "posix":
		return DefaultLocale, true
	}
	// 解析 "language[_region]"：分隔符同时接受 "_" 和 "-"。
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '_' || r == '-' })
	if len(parts) == 0 || len(parts) > 2 {
		return "", false
	}
	lang := asciiLower(parts[0])
	if len(parts) == 2 && !validLocaleRegion(asciiUpper(parts[1])) {
		return "", false
	}
	switch lang {
	case "zh":
		return ZhCN, true
	case "en":
		return EnUS, true
	}
	return "", false
}

// validLocaleRegion 报告 s 是否为合法的区域码：两个 ASCII 字母
// （如 "US"、"cn"）或三个数字（如 "001"）。其他写法均不合法。
func validLocaleRegion(s string) bool {
	switch len(s) {
	case 2:
		return isASCIILetter(s[0]) && isASCIILetter(s[1])
	case 3:
		return s[0] >= '0' && s[0] <= '9' &&
			s[1] >= '0' && s[1] <= '9' &&
			s[2] >= '0' && s[2] <= '9'
	}
	return false
}

// asciiUpper 只折叠 ASCII 小写字母，与 asciiLower 对称。
// 它不依赖 Unicode 大小写折叠，避免易混淆字符混入闭集。
func asciiUpper(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			if out == nil {
				out = []byte(s)
			}
			out[i] = c - ('a' - 'A')
		}
	}
	if out == nil {
		return s
	}
	return string(out)
}
