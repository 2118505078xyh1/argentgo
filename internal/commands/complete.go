package commands

// 命令名补全。
//
// CompleteCommands 是针对斜杠命令的纯前缀匹配器：它从不读取
// 文件、启动进程或修改其参数。它让 UI 能够提供
// 实时建议列表，而无需在每种前端中重新实现本包的命名规则
// （命名空间、frontmatter 别名、CJK 输入）。

import (
	"sort"
	"strings"
)

// CompleteCommands 返回所有 Name 或 Aliases 以 prefix 开头的
// 命令。匹配规则：
//
//   - 忽略前导 '/' 和首尾空白，因此像 "/rev" 这样输入了一半的
//     斜杠命令可直接使用。
//   - 只考虑 prefix 的第一个以空白分隔的标记；名称后
//     部分键入的参数不会抑制匹配。
//   - 比较不区分大小写且按 rune 安全处理（多字节 CJK 名称会
//     as 审查 match a 审 prefix). Full-width ASCII and the ideographic space
//     折叠为半角形式，覆盖中文输入法的输入）。
//   - frontmatter 中声明的别名同样匹配。对插件命令，别名还可
//     带命名空间匹配，例如 "acme:review" 的别名 "re"
//     可通过 "acme:re" 访问。
//   - 空 prefix 匹配所有内容，因此一个函数既能用于
//     空查询列举，也能用于增量补全。
//
// 结果是按 Name（并列时再按 Path 保证稳定）排序的新切片，
// 即使某条命令通过多个名称匹配，也绝不包含重复项。
func CompleteCommands(cmds []Command, prefix string) []Command {
	key := completionKey(prefix)
	out := make([]Command, 0, len(cmds))
	for _, cmd := range cmds {
		if completionMatches(cmd, key) {
			out = append(out, cmd)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Complete 是 Set 对 CompleteCommands 的便捷封装。
func (s *Set) Complete(prefix string) []Command {
	return CompleteCommands(s.List(), prefix)
}

func completionMatches(cmd Command, key string) bool {
	if key == "" {
		return true
	}
	if strings.HasPrefix(completionKey(cmd.Name), key) {
		return true
	}
	for _, alias := range cmd.Aliases {
		if strings.HasPrefix(completionKey(alias), key) {
			return true
		}
		if cmd.Plugin != "" && strings.HasPrefix(completionKey(cmd.Plugin+":"+alias), key) {
			return true
		}
	}
	return false
}

// completionKey 将用户键入的前缀或命令名/别名规范化为
// 单一的可比较形式。宽度折叠在大小写折叠之前逐 rune 进行，
// 折叠，使全角拉丁字母的行为与其 ASCII 形式完全一致。
func completionKey(s string) string {
	s = strings.TrimSpace(foldWidth(s))
	s = strings.TrimPrefix(s, "/")
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// foldWidth 将全角 ASCII 和表意空格折叠为
// 半角形式。它与参数补全共用，因此两种输入都
// 能接受通过中文输入法键入的文本。
func foldWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000': // ideographic space
			b.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E': // full-width ASCII block
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
