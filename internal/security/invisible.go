package security

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Unicode Tag Block（第 14 平面）在 U+E0000..U+E007F 镜像了可打印 ASCII。
// 它的码位在终端和编辑器中不显示任何内容，因此可以在人类以为自己已经读过的
// 文本中隐藏提示注入载荷。
const (
	tagBlockStart = '\U000E0000'
	tagBlockEnd   = '\U000E007F'
)

// ContainsInvisibleTags 报告 s 是否包含任何 Unicode Tag Block 字符。它是一个
// 检测辅助函数：是否警告、拒绝或剥离由调用方决定。bidi 控制符和其他不可见格式
// 字符不属于 tag。
func ContainsInvisibleTags(s string) bool {
	for _, r := range s {
		if r >= tagBlockStart && r <= tagBlockEnd {
			return true
		}
	}
	return false
}

// isBidiOrFormat 报告 r 是否在不渲染为可见内容的情况下改变文本方向或布局。
// 这些字符必须在审批提示中以转义形式显示，使操作员看到其批准的全部内容。
func isBidiOrFormat(r rune) bool {
	switch r {
	case '\u061C', // 阿拉伯字母标记
		'\u200E', // 从左到右标记
		'\u200F', // 从右到左标记
		'\uFEFF': // 零宽不换行空格 / BOM
		return true
	}
	return (r >= '\u202A' && r <= '\u202E') || // 嵌入与覆盖控制符
		(r >= '\u2066' && r <= '\u2069') // 隔离控制符
}

// EscapeBidi 把每个 bidi/格式字符替换为可见的 \uXXXX 转义，其余 rune 保持不变。
// 非特殊字节（包括格式错误的 UTF-8）会被逐字复制，因此返回的字符串绝不会是
// 对输入的有损重写。中日韩字符、emoji、换行和制表符都原样通过。
func EscapeBidi(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isBidiOrFormat(r) {
			b.WriteString(fmt.Sprintf("\\u%04x", r))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// SanitizeInvisible 完全移除 Unicode Tag Block 字符，并转义 bidi/格式字符。
// hadTags 报告是否发现了任何 tag，以便调用方警告操作员隐藏文本已被丢弃。
// 只有提示副本被清洗；执行的参数从不被修改。
func SanitizeInvisible(s string) (cleaned string, hadTags bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= tagBlockStart && r <= tagBlockEnd {
			hadTags = true
			i += size
			continue
		}
		if isBidiOrFormat(r) {
			b.WriteString(fmt.Sprintf("\\u%04x", r))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String(), hadTags
}
