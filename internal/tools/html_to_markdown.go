package tools

import (
	"html"
	"regexp"
	"strings"
)

// 本文件提供纯标准库的轻量 HTML→Markdown 结构化提取（学自 crush
// fetch_helpers.go）。它不引入 x/net/html（第三方依赖），而是手写有界扫描器，
// 保留链接 URL、标题层级、列表与表格，剔除 script/style/nav 等噪声。
//
// 目标不是完整的 HTML5 渲染，而是"让模型能引用来源链接、看懂表格与层级"。
// 输入有界（调用方已限制 5MB），输出行/字节有界，防止恶意页面撑爆上下文。

const (
	mdMaxOutputLines = 4000
	mdMaxOutputBytes = 1 << 20 // 1 MiB
)

var (
	reComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	reTag        = regexp.MustCompile(`(?is)<\s*(/?)\s*([a-z0-9]+)\b([^>]*)>`)
	reAttrHref   = regexp.MustCompile(`(?i)\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	reBlankLines = regexp.MustCompile(`\n{3,}`)
)

// noiseTags 是需要整体丢弃的噪声标签。RE2 无反向引用，故用手动栈扫描而非
// 单个 regexp（见 stripNoiseBlocks）。
var noiseTags = map[string]bool{
	"script": true, "style": true, "nav": true, "header": true, "footer": true,
	"aside": true, "noscript": true, "iframe": true, "svg": true, "form": true,
	"button": true,
}

// stripNoiseBlocks 去掉噪声标签及其内容。用 reTag 扫描，遇到打开的噪声标签
// 时跳过直到对应关闭标签（支持同名嵌套计数）。纯 stdlib、O(n)。
func stripNoiseBlocks(s string) string {
	var b strings.Builder
	cursor := 0
	skipDepth := 0
	skipName := ""
	for _, m := range reTag.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[0], m[1]
		nameStart, nameEnd := m[4], m[5]
		isClose := m[2] != m[3]
		name := strings.ToLower(s[nameStart:nameEnd])
		if skipDepth > 0 {
			// 处于噪声块内：既不写文本也不写标签，遇到同名关闭标签出栈。
			if name == skipName {
				if isClose {
					skipDepth--
				} else {
					skipDepth++
				}
			}
			cursor = end
			continue
		}
		// 正常状态：先写标签前的文本，再决定是否跳过该标签及其内容。
		b.WriteString(s[cursor:start])
		if noiseTags[name] && !isClose {
			skipName = name
			skipDepth = 1
		} else {
			// 非噪声标签原样保留。
			b.WriteString(s[start:end])
		}
		cursor = end
	}
	if skipDepth == 0 && cursor < len(s) {
		b.WriteString(s[cursor:])
	}
	return b.String()
}

// htmlToMarkdown 把 HTML 转成结构化 Markdown。
// 保留：a[href]→[text](url)、h1-h6→#、li→-、table→管道行、p/br→换行。
// 剔除：噪声块与注释。实体解码、UTF-8 安全、输出有界。
func htmlToMarkdown(body string) string {
	// 预处理：去噪声块与注释（一次）。
	clean := stripNoiseBlocks(body)
	clean = reComment.ReplaceAllString(clean, "")

	var b strings.Builder
	cursor := 0
	lines := 0
	linkURL := ""
	var linkText strings.Builder

	write := func(text string) {
		if b.Len() >= mdMaxOutputBytes {
			return
		}
		decoded := html.UnescapeString(text)
		decoded = strings.Join(strings.Fields(decoded), " ")
		if decoded != "" {
			b.WriteString(decoded)
		}
	}
	newline := func() {
		if lines < mdMaxOutputLines {
			b.WriteString("\n")
			lines++
		}
	}

	for _, m := range reTag.FindAllStringSubmatchIndex(clean, -1) {
		fullStart, fullEnd := m[0], m[1]
		closeStart, closeEnd := m[2], m[3]
		nameStart, nameEnd := m[4], m[5]
		attrStart, attrEnd := m[6], m[7]

		text := clean[cursor:fullStart]
		cursor = fullEnd
		if linkURL != "" {
			// 链接内的文本收集到 linkText，不直接输出。
			linkText.WriteString(html.UnescapeString(strings.Join(strings.Fields(text), " ")))
		} else {
			write(text)
		}

		isClose := closeEnd > closeStart
		name := strings.ToLower(clean[nameStart:nameEnd])
		attrs := ""
		if attrStart >= 0 && attrEnd >= 0 {
			attrs = clean[attrStart:attrEnd]
		}

		switch {
		case name == "a" && !isClose:
			linkURL = attrHref(attrs)
			linkText.Reset()
		case name == "a" && isClose:
			if linkURL != "" {
				t := strings.TrimSpace(linkText.String())
				if t == "" {
					t = linkURL
				}
				b.WriteString("[" + t + "](" + linkURL + ")")
			} else {
				b.WriteString(linkText.String())
			}
			linkURL, linkText = "", strings.Builder{}
		case linkURL != "":
			// 链接内标签（如 <code>）：忽略标签本身，文本已在上面收集。
		case name == "br":
			newline()
		case name == "p" || name == "div" || name == "section" || name == "article":
			if !isClose {
				newline()
			}
		case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' && !isClose:
			newline()
			b.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
		case name == "li" && !isClose:
			newline()
			b.WriteString("- ")
		case name == "tr" && !isClose:
			newline()
			b.WriteString("| ")
		case name == "td" || name == "th":
			b.WriteString(" | ")
		case name == "table":
			if !isClose {
				newline()
			}
		}
	}

	if cursor < len(clean) {
		if linkURL != "" {
			linkText.WriteString(html.UnescapeString(clean[cursor:]))
		} else {
			write(clean[cursor:])
		}
	}
	// 未闭合的 <a>：把收集的文本原样输出。
	if linkURL != "" {
		b.WriteString(linkText.String())
	}

	out := reBlankLines.ReplaceAllString(b.String(), "\n\n")
	out = strings.TrimSpace(out)
	if len(out) > mdMaxOutputBytes {
		out = out[:mdMaxOutputBytes]
	}
	return out
}

// attrHref 从标签属性串中提取 href 值（支持双引号/单引号/裸值）。
// 拒绝 javascript:/data: 防注入。
func attrHref(attrs string) string {
	m := reAttrHref.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	for _, v := range []string{m[2], m[3], m[4]} {
		if v != "" {
			u := html.UnescapeString(v)
			lower := strings.ToLower(u)
			if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") {
				return ""
			}
			return u
		}
	}
	return ""
}
