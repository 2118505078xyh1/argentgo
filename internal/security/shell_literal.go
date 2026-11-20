package security

import (
	"regexp"
	"strings"
)

// maxShellLiteralDepth 限制对嵌套 shell -c 载荷的递归分类。每次递归至少消耗
// 包装器文本，因此终止已有保证；上限还使恶意的深层嵌套输入也能被低成本分类。
const maxShellLiteralDepth = 8

// shellExecRe 在*命令位置*匹配 shell 可执行文件（可选带相对或绝对路径）。
// 它针对 maskCommandPositions 产生的掩码运行，其中引号字符串、反引号替换、
// 注释和 heredoc 正文已被置为空白。这样，只在无害字符串中提到的 shell 名字
// （例如 `echo "try bash -c 'rm'"`）就不会成为候选。
var shellExecRe = regexp.MustCompile(`(?i)(?:^|[\s;|&(])((?:/|[A-Za-z0-9_.-]+/)*(?:bash|dash|zsh|ksh|ash|sh))\b`)

// maskCommandPositions 与 sanitizeCommand 有一处刻意的区别：heredoc 正文和
// 分隔符行会变成等长的空格串，而不是空字符串。因此掩码中的字节偏移始终与原始
// 命令对齐，这使 shellLiteralPayloads 可以从原始命令中逐字复制带引号的载荷。
func maskCommandPositions(command string) string {
	lines := strings.Split(command, "\n")
	heredoc := ""
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if heredoc != "" {
			if strings.TrimSpace(line) == heredoc {
				heredoc = ""
			}
			out = append(out, strings.Repeat(" ", len(line)))
			continue
		}
		clean, delim := sanitizeLine(line)
		heredoc = delim
		out = append(out, clean)
	}
	return strings.Join(out, "\n")
}

// shellLiteralPayloads 返回在命令位置发现的每个 `bash -c`、`sh -c`、
// `bash -lc` 等调用的带引号字面量命令载荷。它是一个纯*提取器*：从不判定什么
// 是危险的，只把恢复出的字面量交还给调用方，由调用方在其上重新运行现有分类器。
// 这弥补了 sanitizeCommand 在 FindFileEditingCommand/DetectThreat 看到之前就把
// 带引号载荷清空的漏洞。
//
// 只返回真正带引号的载荷。不带引号的 -c 参数在 shell 语法中不是命令字符串
// （它成为 $0，后面的词是位置参数），因此跳过它可以避免对
// `git status bash -c rm` 这类命令误报；而未闭合的引号会尽力恢复
// （失败即安全：仍然检查现存的任何文本）。
func shellLiteralPayloads(command string) []string {
	mask := maskCommandPositions(command)
	var out []string
	for _, loc := range shellExecRe.FindAllStringSubmatchIndex(mask, -1) {
		// loc[2]:loc[3] 是 shell 可执行文件 token；扫描 -c 标志从其后立即开始。
		flagEnd, ok := findShellCFlagEnd(mask, loc[3])
		if !ok {
			continue
		}
		payload, ok := extractQuotedPayload(command, flagEnd)
		if ok && strings.TrimSpace(payload) != "" {
			out = append(out, payload)
		}
	}
	return out
}

// findShellCFlagEnd 扫描 shell 可执行文件之后的选项 token，返回刚越过包含
// 'c' 的短选项簇（-c、-lc、-cl、-xc 等）的字节偏移。长选项（--noprofile）
// 也会被跳过，但第一个非选项 token 会结束扫描：只有选项文本才可能携带 -c。
func findShellCFlagEnd(mask string, p int) (int, bool) {
	for p < len(mask) {
		for p < len(mask) && isShellBlank(mask[p]) {
			p++
		}
		if p >= len(mask) || mask[p] != '-' {
			return 0, false
		}
		start := p
		for p < len(mask) && !isShellBlank(mask[p]) && !isShellSeparator(mask[p]) {
			p++
		}
		token := mask[start:p]
		if len(token) >= 2 && token[1] != '-' && strings.Contains(strings.ToLower(token), "c") {
			return p, true
		}
	}
	return 0, false
}

// extractQuotedPayload 读取从 p 处或之后开始的单个 shell 词，它必须以引号
// 开头。未闭合的引号会一直恢复到换行或输入结束，而不是丢弃，因此被截断的
// 恶意载荷仍会被分类（失败即安全）。
func extractQuotedPayload(command string, p int) (string, bool) {
	for p < len(command) && isShellBlank(command[p]) {
		p++
	}
	if p >= len(command) || (command[p] != '\'' && command[p] != '"') {
		return "", false
	}
	return readShellWord(command, p)
}

// readShellWord 遵循 POSIX 的引号拼接和反斜杠转义，因此恶意载荷无法把第二个
// 带引号片段拆到相邻段中，从而对分类器隐藏它。词在第一个未加引号的空白或
// shell 分隔符处结束。
func readShellWord(s string, start int) (string, bool) {
	var b strings.Builder
	i := start
	for i < len(s) {
		switch s[i] {
		case '\'', '"':
			quote := s[i]
			i++
			for i < len(s) {
				c := s[i]
				if c == '\\' && quote == '"' && i+1 < len(s) {
					b.WriteByte(c)
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == quote {
					i++
					break
				}
				if c == '\n' || c == '\r' {
					return b.String(), true // 未闭合：失败即安全
				}
				b.WriteByte(c)
				i++
			}
			if i >= len(s) {
				return b.String(), true
			}
			// 只有直接相邻的片段才继续同一个词；
			// 空白和分隔符会结束 -c 参数。
			switch {
			case s[i] == '\'' || s[i] == '"':
				continue
			case s[i] == '\\' && i+1 < len(s) && s[i+1] == '\'':
				b.WriteByte('\'')
				i += 2
				continue
			default:
				return b.String(), true
			}
		case '\\':
			if i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			return b.String(), true
		}
	}
	return b.String(), true
}

func isShellBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

func isShellSeparator(c byte) bool {
	switch c {
	case ';', '&', '|', '(', ')', '\n':
		return true
	}
	return false
}
