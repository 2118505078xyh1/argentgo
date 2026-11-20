package config

import "strings"

// StripJSONC 从 JSONC 文档中移除 // 和 /* */ 注释以及尾随逗号，
// 产出 encoding/json 可以解码的严格 JSON。
//
// 它是一个手写的扫描器：字符串内容（包括
// "http://..." 或 "/* not a comment */" 这样的序列）会被原样复制，
// 因此带引号的值里类似注释的片段永远不会被误当作语法。
func StripJSONC(text string) string {
	// UTF-8 BOM（在 PowerShell 5.x 保存的文件中常见）不是 JSON
	// 空白；在公共入口处剥离它，使每个调用方都能把结果
	// 直接交给 encoding/json。
	text = strings.TrimPrefix(text, "\ufeff")
	var out strings.Builder
	out.Grow(len(text))

	inString := false
	for i := 0; i < len(text); {
		char := text[i]
		if inString {
			out.WriteByte(char)
			if char == '\\' && i+1 < len(text) {
				// 被转义的字节不可能闭合字符串或开启注释。
				out.WriteByte(text[i+1])
				i += 2
				continue
			}
			if char == '"' {
				inString = false
			}
			i++
			continue
		}

		switch char {
		case '"':
			inString = true
			out.WriteByte(char)
			i++
		case '/':
			switch {
			case i+1 < len(text) && text[i+1] == '/':
				i += 2
				for i < len(text) && text[i] != '\n' {
					i++
				}
				if i < len(text) {
					out.WriteByte('\n') // 保留行结构以便定位错误
					i++
				}
			case i+1 < len(text) && text[i+1] == '*':
				i += 2
				for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
					if text[i] == '\n' {
						out.WriteByte('\n')
					}
					i++
				}
				if i+1 < len(text) {
					i += 2 // 消费掉结束的 */
				}
			default:
				out.WriteByte(char)
				i++
			}
		case ',':
			next := nextJSONCSignificant(text, i+1)
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				i++ // trailing comma: drop it
			} else {
				out.WriteByte(char)
				i++
			}
		default:
			out.WriteByte(char)
			i++
		}
	}
	return out.String()
}

// nextJSONCSignificant 返回从 from 起（含）第一个既非空白
// 也不属于注释的字节的索引。调用方只在字符串之外使用它，
// 这足以检测尾随逗号。
func nextJSONCSignificant(text string, from int) int {
	for i := from; i < len(text); {
		switch {
		case text[i] == ' ' || text[i] == '\t' || text[i] == '\r' || text[i] == '\n':
			i++
		case i+1 < len(text) && text[i] == '/' && text[i+1] == '/':
			i += 2
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case i+1 < len(text) && text[i] == '/' && text[i+1] == '*':
			i += 2
			for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
				i++
			}
			if i+1 < len(text) {
				i += 2
			}
		default:
			return i
		}
	}
	return len(text)
}
