package security

import (
	"strings"
	"unicode/utf8"
)

// StripUnicodeTags 剥离字符串中的 Unicode Tag Block 字符（U+E0000..U+E007F）。
// 这些码位在任何字体和终端中都不渲染为可见内容，却能镜像可打印 ASCII，因此
// 攻击者可以把一整段提示注入指令藏进 AGENTS.md、技能说明或 MCP 工具描述，而
// 人类审阅者误以为自己看到的就是全部内容。
//
// 判断在 rune 层进行，并与 invisible.go 中的检测器共用 tagBlockStart/tagBlockEnd
// 常量，因此不会产生检测与剥离不一致的边界。非 tag 的字节（包括格式错误的
// UTF-8）按原样逐字节复制，CJK、emoji、bidi 控制符和换行等都完全不变。
// 该函数是纯函数：不修改输入，对同一输入始终返回相同结果。
func StripUnicodeTags(s string) string {
	// 常见路径上没有任何 tag，直接返回原串：既零分配，也天然保证逐字节不变。
	if !ContainsInvisibleTags(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= tagBlockStart && r <= tagBlockEnd {
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
