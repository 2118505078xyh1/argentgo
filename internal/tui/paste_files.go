package tui

// 把终端粘贴的多文件路径识别为 @引用。
//
// 从资源管理器或终端粘贴多个文件路径时，Windows Terminal 用双引号包裹含空格
// 的路径，Unix 终端用反斜杠转义空格，Rio 还会在 Windows 上插入 NUL 字节。
// 若整段文本按其中一路规则全部解析为已存在的文件，就改写为 @path 引用；
// 否则原样提交，绝不猜测或破坏用户输入。纯标准库实现。

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// PathStat 探测路径是否存在。生产环境传 os.Stat；测试注入假实现，不碰真实磁盘。
type PathStat func(path string) (os.FileInfo, error)

// ParsePastedFiles 把 text 解析为已存在的文件路径列表。
// ok 为 true 表示整段文本按「逐行 / 引号 / 反斜杠转义」三路之一全部解析成功。
// 仅当文本带路径特征（多个条目、路径分隔符、引号、转义或换行）时才可能成功，
// 避免把恰好与某个文件同名的普通单词误判为附件。
func ParsePastedFiles(text string, stat PathStat) ([]string, bool) {
	if stat == nil {
		stat = os.Stat
	}
	// Rio 在 Windows 上会用 NUL 分隔粘贴的多个路径。把它换成空白而不是直接删除：
	// 直接删除会让相邻两个带引号的路径粘成一个 token（"a"\x00"b" → "a""b"）。
	cleaned := strings.ReplaceAll(text, "\x00", " ")
	trimmed := strings.TrimSpace(cleaned)
	if trimmed == "" {
		return nil, false
	}

	accept := func(tokens []string) ([]string, bool) {
		if len(tokens) == 0 || !hasPathSignal(trimmed, tokens) {
			return nil, false
		}
		for _, token := range tokens {
			info, err := stat(token)
			if err != nil || info == nil {
				return nil, false
			}
		}
		return tokens, true
	}

	if tokens, ok := tokenizePastedLines(trimmed); ok {
		if paths, ok := accept(tokens); ok {
			return paths, true
		}
	}
	if strings.Contains(trimmed, `"`) {
		if tokens, ok := tokenizePastedQuoted(trimmed); ok {
			if paths, ok := accept(tokens); ok {
				return paths, true
			}
		}
	}
	if strings.Contains(trimmed, `\`) {
		if tokens, ok := tokenizePastedEscaped(trimmed); ok {
			if paths, ok := accept(tokens); ok {
				return paths, true
			}
		}
	}
	return nil, false
}

// hasPathSignal 报告文本是否「看起来像路径列表」，防止把普通单词误判为附件。
func hasPathSignal(text string, tokens []string) bool {
	if len(tokens) > 1 {
		return true
	}
	return strings.ContainsAny(text, "/\\\"\r\n")
}

// tokenizePastedLines 按行切分：每行去空白后都必须非空。
func tokenizePastedLines(text string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			return nil, false
		}
		out = append(out, line)
	}
	return out, len(out) > 0
}

// tokenizePastedQuoted 解析 Windows Terminal 风格的引号包裹路径：引号外空白分隔，
// 引号内空白是路径的一部分，\" 表示字面双引号。引号不成对时判失败。
func tokenizePastedQuoted(text string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inQuote := false
	hasToken := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '\\' && i+1 < len(text) && text[i+1] == '"':
			cur.WriteByte('"')
			i++
			hasToken = true
		case ch == '"':
			inQuote = !inQuote
			hasToken = true
		case (ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n') && !inQuote:
			if hasToken {
				out = append(out, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteByte(ch)
			hasToken = true
		}
	}
	if inQuote {
		return nil, false
	}
	if hasToken {
		out = append(out, cur.String())
	}
	return out, len(out) > 0
}

// tokenizePastedEscaped 解析 Unix 风格反斜杠转义：\ 后的字符按字面处理（含空格），
// 未转义的空白分隔 token。结尾悬空反斜杠判失败。
func tokenizePastedEscaped(text string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	escaped := false
	hasToken := false
	for _, r := range text {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			hasToken = true
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			hasToken = true
		case unicode.IsSpace(r):
			if hasToken {
				out = append(out, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	if escaped {
		return nil, false
	}
	if hasToken {
		out = append(out, cur.String())
	}
	return out, len(out) > 0
}

// PastedFilesToMentions 把已解析的文件路径转成输入行内的 @引用。
// 绝对路径尽量转成相对 root 的路径（附件扫描只接受相对路径）；需要保护的字符
// 交给 quoteIfNeeded 加引号，形成 `@"..."` 这种可被附件扫描识别的形状。
func PastedFilesToMentions(root string, paths []string) string {
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		ref := p
		if abs, err := filepath.Abs(p); err == nil && root != "" {
			if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." &&
				!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				ref = rel
			}
		}
		parts = append(parts, "@"+quoteIfNeeded(filepath.ToSlash(ref)))
	}
	return strings.Join(parts, " ")
}
