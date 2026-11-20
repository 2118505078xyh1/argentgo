package i18n

// Plural 挑选与语言环境 locale 的计数 n 匹配的语法形态。
//
// 形态按顺序 [单数, 复数] 传入。英语 —— 以及任何
// Normalize 回退到的语言环境 —— 对恰好一项使用 forms[0]，对
// 其他所有计数（零、负数和复数 alike）使用 forms[1]。中文
// 没有复数区分，因此所有计数都返回 forms[0]；调用方
// 通常为该语言环境传入单个中性形态。
//
// 没有形态时 Plural 返回 ""。缺失的复数形态回退到
// forms[0]，因此只提供一个形态的调用方总能得到该形态。
// 前两个之外的形态被忽略。
//
// 选择是纯的：它不做目录查找、不在 Missing 中记录且
// 不格式化形态本身。想要嵌入计数的调用方
// 通常随后用 fmt.Sprintf，例如
//
//	form := Plural(locale, n,
//	    T(locale, "activity.command_one"),
//	    T(locale, "activity.command_many"))
func Plural(locale string, n int, forms ...string) string {
	if len(forms) == 0 {
		return ""
	}
	if Normalize(locale) == ZhCN {
		return forms[0] // CJK has no plural: one neutral form covers every count.
	}
	if n == 1 || len(forms) < 2 {
		return forms[0]
	}
	return forms[1]
}
