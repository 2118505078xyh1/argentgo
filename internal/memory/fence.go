package memory

import (
	"strconv"
	"strings"
)

// fenceCandidates 是动态围栏的候选标记对，按优先级排列。
// 每一对都是 (open, close)；选中的条件是 close 不出现在指令文件内容里，
// 这样恶意文件即便写入闭合标签，也无法逃逸围栏去注入系统提示词。
// 各候选使用互不相同的标签名，为内容中可能出现的任意闭合标签留出替代项。
var fenceCandidates = [][2]string{
	{"<file>", "</file>"},
	{"<source>", "</source>"},
	{"<document>", "</document>"},
	{"<instructions>", "</instructions>"},
	{"<user-input>", "</user-input>"},
}

// safeFence 为 content 选择一对安全的围栏标记。
//
// 它扫描 content，挑选第一个闭合标记未出现在内容中的候选对，
// 从而保证内容里的文本绝不可能伪造出闭合标记来截断围栏。
// 检查基于字节子串：ASCII 标签、反引号与换行符都不会出现在
// UTF-8 多字节序列内部，因此本函数对 CJK 及其他多字节文本同样安全。
// 当所有标签候选都被内容占用时，退化为递增长度的反引号围栏：
// 找到第一个在内容中完全不出现的连续反引号串，确保闭合仍不可伪造。
func safeFence(content string) (open, close string) {
	for _, candidate := range fenceCandidates {
		if !strings.Contains(content, candidate[1]) {
			return candidate[0], candidate[1]
		}
	}

	n := 3
	for strings.Contains(content, strings.Repeat("`", n)) {
		n++
	}
	tick := strings.Repeat("`", n)
	return tick, tick
}

// wrapWithFence 用 open/close 围栏包裹 path 与 content。
//
// path 作为元数据行放在内容之前，便于模型识别来源；
// 使用 strconv.Quote 转义路径里的引号与反斜杠，避免其破坏围栏结构。
// 闭合标记 close 由调用方（safeFence）保证不会出现在 content 中。
func wrapWithFence(path, content string, open, close string) string {
	var b strings.Builder
	b.WriteString(open)
	b.WriteByte('\n')
	b.WriteString("path: ")
	b.WriteString(strconv.Quote(path))
	b.WriteByte('\n')
	b.WriteString(content)
	b.WriteByte('\n')
	b.WriteString(close)
	b.WriteByte('\n')
	return b.String()
}

// BlockSafe 与 Block 类似，渲染注入系统提示词的项目指令块，
// 但为每个文档使用 safeFence 选出的动态安全围栏，使任何指令文件
// 都无法通过写入闭合标签逃逸围栏、注入额外指令。
//
// 这是 opt-in 接口：默认路径仍使用 Block 以保持历史输出字节不变；
// 调用方显式改用本函数即启用动态围栏防护。
func BlockSafe(docs []Doc) string {
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(blockHeader)
	for _, doc := range docs {
		open, close := safeFence(doc.Text)
		b.WriteString(wrapWithFence(doc.Path, doc.Text, open, close))
	}
	b.WriteString("</project-memory>\n")
	return b.String()
}
