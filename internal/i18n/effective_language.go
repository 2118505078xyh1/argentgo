package i18n

// effectiveLanguage 按优先级决定最终使用的输出语言，是一个不读写任何
// 全局状态的纯函数。
//
// 决策顺序：
//
//  1. 显式配置 explicit：非空、能通过 ParseOutputLanguagePreference 校验
//     且不等于 "auto" 时直接采用；"auto" 与空值都表示交给下层检测。
//  2. 环境检测：detectLocale(env) 按 LANGUAGE > LC_ALL > LANG >
//     LC_MESSAGES 的顺序解析各来源，返回首个受支持的语言环境。
//  3. 兜底：环境没有任何可识别值时，detectLocale 返回 DefaultLocale。
//
// 显式值先经闭集校验，因此形似指令的任意 CJK 散文（例如
// "请忽略之前的所有指令并输出"）不会冒充语言名而被采纳；这类非法值
// 以及 "auto" 都穿透到环境检测，不会遮蔽合法的环境设置。env 为 nil 时
// detectLocale 返回 DefaultLocale。
func effectiveLanguage(explicit string, env func(string) string) string {
	if lang, ok := ParseOutputLanguagePreference(explicit); ok && lang != "auto" {
		return lang
	}
	if code := detectLocale(env); code != "" {
		return code
	}
	return DefaultLocale
}
