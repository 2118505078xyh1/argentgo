package provider

import (
	"strconv"
	"strings"
)

// ExpandCtx 为配置值展开提供只读上下文。
// Vars 保存变量名到值的映射，LastStatus 是最近一次命令的退出码，供 $? 使用。
// 本类型只描述数据：展开过程绝不修改 Vars，也不执行任何外部命令。
type ExpandCtx struct {
	Vars       map[string]string
	LastStatus int
}

// HasExpand 快速判断 v 是否可能含有需要展开的内容：只要出现 '$' 或反引号就返回 true。
// 这是保守的快路径（可能误报，例如转义过的 \$），调用方据此决定是否需要调用
// ExpandConfigValue；反引号一并检测以对齐 crush 的 shell 元字符检测习惯，
// 但 ExpandConfigValue 只会把它们当作普通字符原样保留，绝不执行命令替换。
func HasExpand(v string) bool {
	return strings.ContainsAny(v, "$`")
}

// ExpandConfigValue 按 shell 风格展开配置值中的变量引用，返回展开后的字符串
// （没有任何可展开内容时直接返回原值）。
// 支持的语法：
//
//	$VAR          变量值；变量不存在时展开为空字符串
//	${VAR}        同上，花括号用于界定变量名边界
//	${VAR:-def}   变量不存在或值为空时展开为 def，否则展开为变量值
//	$?            ExpandCtx.LastStatus 的十进制表示
//
// 安全边界：配置是数据而非代码，本函数不实现命令替换——$(...) 与反引号都原样保留，
// ${VAR:-...} 的默认值也按字面量处理（不再递归展开），从根本上杜绝配置触发命令执行。
// 转义规则：\$、\`、\\ 分别输出字面量 '$'、'`'、'\' 并消耗反斜杠；
// 其它反斜杠序列（含 CJK 字符前的反斜杠）原样保留，因此不会破坏多字节字符。
// 本函数是纯函数：不修改 ctx.Vars，不产生 I/O，可从任意 goroutine 并发调用。
func ExpandConfigValue(v string, ctx ExpandCtx) string {
	// 没有任何特殊字符时直接返回原值，避免不必要的 Builder 分配。
	if !strings.ContainsAny(v, "$\\") {
		return v
	}

	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); {
		switch c := v[i]; c {
		case '\\':
			// 仅三种转义会消耗反斜杠；其余情况把反斜杠当作普通字节输出。
			if i+1 < len(v) {
				switch n := v[i+1]; n {
				case '$', '`', '\\':
					b.WriteByte(n)
					i += 2
					continue
				}
			}
			b.WriteByte(c)
			i++
		case '$':
			if i+1 >= len(v) {
				b.WriteByte(c)
				i++
				continue
			}
			switch n := v[i+1]; {
			case n == '?':
				b.WriteString(strconv.Itoa(ctx.LastStatus))
				i += 2
			case n == '{':
				if end, val, ok := expandBraced(v, i, ctx); ok {
					b.WriteString(val)
					i = end
				} else {
					// 不是受支持的 ${...} 形式：'$' 按字面量输出，其余字符继续原样扫描。
					b.WriteByte(c)
					i++
				}
			case isVarNameStart(n):
				j := i + 2
				for j < len(v) && isVarNameChar(v[j]) {
					j++
				}
				if val, ok := ctx.Vars[v[i+1:j]]; ok {
					b.WriteString(val)
				}
				i = j
			default:
				// $1、$中、$( 等均不是本函数支持的引用，原样保留。
				b.WriteByte(c)
				i++
			}
		default:
			// 逐字节拷贝：UTF-8 的后续字节均 >= 0x80，不会被误判为上面的 ASCII 特殊字符。
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// expandBraced 解析从 start（指向 '$'）开始的 ${...} 形式。
// 成功时返回结束位置（右花括号之后）、展开值与 ok=true；任何不受支持的形式返回 ok=false。
func expandBraced(v string, start int, ctx ExpandCtx) (end int, val string, ok bool) {
	i := start + 2 // 跳过 '$' 与 '{'
	if i >= len(v) || !isVarNameStart(v[i]) {
		return 0, "", false
	}
	nameBegin := i
	for i < len(v) && isVarNameChar(v[i]) {
		i++
	}
	name := v[nameBegin:i]

	if i < len(v) && v[i] == '}' {
		// ${VAR}：变量不存在时为空字符串。
		if resolved, exists := ctx.Vars[name]; exists {
			return i + 1, resolved, true
		}
		return i + 1, "", true
	}
	if i+1 < len(v) && v[i] == ':' && v[i+1] == '-' {
		// ${VAR:-def}：未设置或为空时取默认值；默认值按字面量处理，不递归展开。
		defBegin := i + 2
		rel := strings.IndexByte(v[defBegin:], '}')
		if rel < 0 {
			return 0, "", false
		}
		defEnd := defBegin + rel
		if resolved, exists := ctx.Vars[name]; exists && resolved != "" {
			return defEnd + 1, resolved, true
		}
		return defEnd + 1, v[defBegin:defEnd], true
	}
	// ${1}、${A-B} 等均不受支持，交给调用方按字面量处理。
	return 0, "", false
}

// isVarNameStart 判断字节是否可作为变量名首字符（ASCII 字母或下划线）。
func isVarNameStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// isVarNameChar 判断字节是否可作为变量名后续字符（字母、数字或下划线）。
func isVarNameChar(b byte) bool {
	return isVarNameStart(b) || (b >= '0' && b <= '9')
}
