package config

import "strings"

// replyLanguageDefault 是在显式 locale 和任何探测到的环境变量
// 都未指明受支持语言时所使用的语言。
const replyLanguageDefault = "en"

// replyLanguageEnvOrder 是 aider 风格自动探测环境变量的顺序。
// 第一个指明已知语言的值胜出；未知值则继续
// 回退到下一个变量。
var replyLanguageEnvOrder = [...]string{"LANG", "LANGUAGE", "LC_ALL", "LC_MESSAGES"}

// replyLanguageAliases 是一个有意保持精简的本地表，把
// 小写的 locale、标签或语言名映射为简短的规范代码。config
// 不得依赖 internal/i18n，因此这里在不导入它的前提下
// 复刻了 i18n.Canonical 的思路。短代码（"zh"、"en" 等）被应用
// 转换为易读的 "Reply in X" 短语。
var replyLanguageAliases = map[string]string{
	// 英语，包括 C/POSIX 伪区域设置。
	"c": "en", "posix": "en", "en": "en", "eng": "en", "english": "en",
	// 中文。
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh",
	"中文": "zh", "简体中文": "zh", "繁體中文": "zh", "zh-hans": "zh", "zh-hant": "zh",
	// 日语。
	"ja": "ja", "jpn": "ja", "japanese": "ja", "日本語": "ja",
	// 韩语。
	"ko": "ko", "kor": "ko", "korean": "ko", "한국어": "ko",
	// 几种常见的西方语言，使该表不限于 CJK。
	"ru": "ru", "rus": "ru", "russian": "ru",
	"de": "de", "deu": "de", "ger": "de", "german": "de",
	"fr": "fr", "fra": "fr", "fre": "fr", "french": "fr",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"es": "es", "spa": "es", "spanish": "es",
	"ca": "ca", "cat": "ca", "catalan": "ca",
}

// canonicalReplyLanguage 把一个原始 locale 值规范化为短代码。
// 在查询别名表之前，会剥离 POSIX 编码/修饰符后缀（".UTF-8"、"@euro"）
// 以及 BCP-47 的地区/脚本子标签，因此
// "zh_Hant_TW.UTF-8@euro" 这样的值仍能映射到 "zh"。"auto" 和空
// 值不是语言，会报告 ok == false。
func canonicalReplyLanguage(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, "auto") {
		return "", false
	}
	lower := strings.ToLower(value)
	if i := strings.IndexAny(lower, ".@"); i >= 0 {
		lower = lower[:i]
	}
	if code, ok := replyLanguageAliases[lower]; ok {
		return code, true
	}
	if i := strings.IndexAny(lower, "-_"); i >= 0 {
		if code, ok := replyLanguageAliases[lower[:i]]; ok {
			return code, true
		}
	}
	return "", false
}

// DetectReplyLanguage 为应用的 "Reply in X" 系统提示注入
// 挑选语言代码。它是纯函数：不读取配置和任何
// 全局状态，只触碰传入的两个参数。
//
// 显式 locale 优先。"auto"、空或未知的显式 locale 会
// 回退到环境，按 LANG、LANGUAGE、LC_ALL、LC_MESSAGES
// 的顺序探测。返回第一个指明受支持语言的值；若都不
// 匹配，则返回 replyLanguageDefault（"en"）。nil env map 是安全的。
//
// 该辅助函数有意保持 config 局部性（不依赖 internal/i18n），且
// 不改变 Config.Locale 的解析方式。把它接入应用的
// 提示词，留给 internal/app 的负责人处理。
func DetectReplyLanguage(locale string, env map[string]string) string {
	if code, ok := canonicalReplyLanguage(locale); ok {
		return code
	}
	for _, name := range replyLanguageEnvOrder {
		if code, ok := canonicalReplyLanguage(env[name]); ok {
			return code
		}
	}
	return replyLanguageDefault
}

// replyLanguageDisplayNames 把规范的回复语言代码映射到该
// 语言的本地显示名。config 不得依赖 internal/i18n，因此
// 这些本地名称在本地复刻了 i18n 的输出语言表；代码正是
// DetectReplyLanguage / canonicalReplyLanguage 产出的那些，
// 从而支持 DetectReplyLanguage -> LocaleDisplayName 的往返。
var replyLanguageDisplayNames = map[string]string{
	"en": "English",
	"zh": "中文",
	"ja": "日本語",
	"ko": "한국어",
	"ru": "Русский",
	"de": "Deutsch",
	"fr": "Français",
	"pt": "Português",
	"es": "Español",
	"ca": "Català",
}

// LocaleDisplayName 返回回复语言代码或 locale 别名的本地显示名
// （例如 "zh" -> "中文"、"eng" -> "English"、"中文" -> "中文"）。
// 应用可以把结果作为模型应使用的回复语言注入系统提示。
// 它是纯函数：不读取配置或全局状态。
//
// 输入先经 canonicalReplyLanguage 规范化，因此别名、
// 编码/脚本/地区子标签和本地名称都会解析为同一个
// 显示名。空、"auto"、未知或不支持的值返回 ""，以便
// 调用方省略该指令，而不是指定用户并未
// 要求的语言。
func LocaleDisplayName(code string) string {
	canonical, ok := canonicalReplyLanguage(code)
	if !ok {
		return ""
	}
	return replyLanguageDisplayNames[canonical]
}
