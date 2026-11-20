package i18n

// formatPromptWithLanguage 在提示词模板中按槽位注入语言指令。
//
// 模板中的 {language} 槽位被替换为 ReplyLanguageHint(lang) 产生的
// 语言指令；其余 {name} 槽位从 vars 中按名称替换，vars 中不存在
// 的名称以及非法占位符保持原样。函数复用 Format 的展开规则，因此
// 转义大括号、嵌套大括号、多字节（CJK）文本以及不被重扫的插入值
// 都按同一套确定性规则处理。
//
// 当 lang 为空或不受支持时，ReplyLanguageHint 返回中性值（空字符串），
// {language} 槽位因此被替换为空，不会向模型强加任何语言。
//
// 它是纯函数：不读取配置或全局状态，不修改 vars，也不向 Missing
// 记录任何内容。
func formatPromptWithLanguage(template, lang string, vars map[string]string) string {
	hint := ReplyLanguageHint(lang)
	// 复制 vars 以免改动调用方的 map；language 槽位始终由 lang 决定，
	// 即便 vars 中带有同名键也不覆盖它。
	merged := make(map[string]string, len(vars)+1)
	for name, value := range vars {
		merged[name] = value
	}
	merged["language"] = hint
	return Format(template, merged)
}
