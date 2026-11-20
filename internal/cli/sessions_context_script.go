package cli

import (
	"fmt"
	"sort"
	"strings"
)

// contextScriptHeader 是文件集回放脚本文本的首行说明。它与会话导出的 YAML
// 注释标记一样以 # 开头，便于人工阅读、版本控制以及被逐行回放时忽略。
const contextScriptHeader = "# argent 文件集回放脚本：/drop 清空当前文件集，/add 添加可编辑文件，/read-only 标记只读文件。\n"

// renderContextScript 把一组文件渲染成可回放的 slug 命令脚本文本。
//
// 脚本先输出 /drop 清空当前文件集，再按稳定顺序逐行输出 /add <file>（可编辑
// 文件）和 /read-only <file>（只读文件）。两个列表都会去重、忽略空字符串并
// 稳定排序，因此语义上相同的集合总是生成逐字节相同的脚本文本。路径中含空白
// 或引号、反斜杠时会加双引号并在内部转义，CJK 等字符原样保留为 UTF-8。
func renderContextScript(files []string, readOnly []string) string {
	var b strings.Builder
	b.WriteString(contextScriptHeader)
	b.WriteString("/drop\n")
	for _, file := range sortedScriptPaths(files) {
		fmt.Fprintf(&b, "/add %s\n", quoteScriptPath(file))
	}
	for _, file := range sortedScriptPaths(readOnly) {
		fmt.Fprintf(&b, "/read-only %s\n", quoteScriptPath(file))
	}
	return b.String()
}

// sortedScriptPaths 返回去重、去空并稳定排序后的路径副本，不修改入参。
func sortedScriptPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// quoteScriptPath 把一个路径渲染成脚本中可安全回放的单个参数。
//
// 仅当路径包含空白或引号、反斜杠时才用双引号包裹；内部的双引号和反斜杠用
// 反斜杠转义，与 internal/commands 的参数拆分规则一致。CJK 等字符本身不含
// 空白，原样保留为 UTF-8，不做 \uXXXX 转义。空字符串渲染为 ""。
func quoteScriptPath(path string) string {
	if path == "" {
		return `""`
	}
	if !scriptPathNeedsQuote(path) {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range path {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// scriptPathNeedsQuote 报告路径是否需要加引号才能作为单个参数回放。
func scriptPathNeedsQuote(path string) bool {
	for _, r := range path {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f', '\'', '"', '\\':
			return true
		}
	}
	return false
}
