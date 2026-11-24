package tui

import "strings"

// smartShortenPath 把路径压缩到不超过 maxWidth 个终端单元格。
//
// 策略（按优先级依次尝试，取第一个能放下的候选）：
//   - 若路径以用户主目录开头，先替换为 ~；
//   - 宽度足够时原样返回；
//   - 否则把中间的目录组件缩成首字素（CJK 下即首字），
//     头锚点（~、根目录或盘符）与末两段保持原样；
//   - 仍放不下时从左到右丢弃中间组件，用 … 代替；
//   - 最后仍放不下则保留末尾（文件名与扩展名）并加 …。
//
// 所有宽度都以字素簇为单位计算，CJK 按双单元格计，
// 且返回值宽度绝不超过 maxWidth（maxWidth<=0 返回空串）。
func smartShortenPath(path string, maxWidth int) string {
	home, err := homeDir()
	if err != nil {
		home = ""
	}
	return shortenPathWithHome(path, home, maxWidth)
}

// shortenPathWithHome 是 smartShortenPath 的纯函数内核：
// home 由调用方注入，便于确定性测试。
func shortenPathWithHome(path, home string, maxWidth int) string {
	if maxWidth <= 0 || path == "" {
		return ""
	}
	display := replaceHomePrefix(path, home)
	if TextWidth(display) <= maxWidth {
		return display
	}

	comps, sep := splitPath(display)
	middleStart, middleEnd := 1, len(comps)-2
	if middleEnd < middleStart {
		middleEnd = middleStart
	}
	middleCount := middleEnd - middleStart

	// 先把中间组件缩成首字素，尽量保住全部层级。
	candidate := buildShortPath(comps, sep, true, 0)
	if TextWidth(candidate) <= maxWidth {
		return candidate
	}
	// 依次从左侧丢弃中间组件，越丢越短。
	for drop := 1; drop <= middleCount; drop++ {
		candidate = buildShortPath(comps, sep, true, drop)
		if TextWidth(candidate) <= maxWidth {
			return candidate
		}
	}
	// 末两段自身就超宽：保留末尾（文件名与扩展名）。
	return tailClip(candidate, maxWidth)
}

// replaceHomePrefix 把 path 的主目录前缀替换为 ~。
// 仅在前缀后紧跟分隔符（或恰好是主目录本身）时替换，
// 避免误伤同一前缀的其它目录。
// 比较时把 \ 与 / 视为等价，输出保留 path 原来的分隔符。
func replaceHomePrefix(path, home string) string {
	if home == "" {
		return path
	}
	root := strings.TrimRight(home, `/\`)
	if root == "" {
		return path
	}
	normalizedPath := strings.ReplaceAll(path, `\`, `/`)
	normalizedRoot := strings.ReplaceAll(root, `\`, `/`)
	if !strings.HasPrefix(normalizedPath, normalizedRoot) {
		return path
	}
	rest := path[len(root):]
	if rest == "" {
		return "~"
	}
	if rest[0] == '/' || rest[0] == '\\' {
		return "~" + rest
	}
	return path
}

// splitPath 按 / 与 \ 拆分路径，并返回用于重新拼接的分隔符
// （取第一个出现的分隔符，默认 /）。UTF-8 多字节序列中不会
// 出现 ASCII 分隔符字节，因此逐字节扫描是安全的。
func splitPath(display string) ([]string, string) {
	sep := "/"
	found := false
	var comps []string
	var cur strings.Builder
	for i := 0; i < len(display); i++ {
		c := display[i]
		if c == '/' || c == '\\' {
			if !found {
				sep = string(c)
				found = true
			}
			comps = append(comps, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	comps = append(comps, cur.String())
	return comps, sep
}

// buildShortPath 用 sep 重新拼接组件：头锚点与末两段保持原样，
// 中间组件（下标 [1, len-2)）在 abbrev 时缩为首字素；
// drop>0 时丢弃前 drop 个中间组件，并以单个 … 代替整段。
func buildShortPath(comps []string, sep string, abbrev bool, drop int) string {
	n := len(comps)
	if n == 0 {
		return ""
	}
	parts := make([]string, 0, n)
	parts = append(parts, comps[0]) // 头锚点：~、根目录或盘符
	middleStart, middleEnd := 1, n-2
	if middleEnd < middleStart {
		middleEnd = middleStart
	}
	if drop > 0 && middleEnd > middleStart {
		parts = append(parts, "…")
	}
	for i := middleStart + drop; i < middleEnd; i++ {
		part := comps[i]
		if abbrev {
			part = firstGrapheme(part)
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	for i := middleEnd; i < n; i++ { // 末两段（父目录 + 文件名）
		parts = append(parts, comps[i])
	}
	return strings.Join(parts, sep)
}

// firstGrapheme 返回 s 的第一个字素簇；空串返回空串。
func firstGrapheme(s string) string {
	if s == "" {
		return ""
	}
	end, _ := nextGrapheme(s, 0)
	return s[:end]
}

// tailClip 保留 s 末尾最多 max 个单元格，前面用 … 标记切口。
// 按字素簇裁剪，绝不会把 CJK 字符或 emoji 从中间切开。
func tailClip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if TextWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	type cluster struct {
		start, width int
	}
	var clusters []cluster
	for pos := 0; pos < len(s); {
		next, width := nextGrapheme(s, pos)
		clusters = append(clusters, cluster{pos, width})
		pos = next
	}
	budget := max - 1 // 省略号自身占一个单元格
	used := 0
	start := len(s)
	for i := len(clusters) - 1; i >= 0; i-- {
		if used+clusters[i].width > budget {
			break
		}
		used += clusters[i].width
		start = clusters[i].start
	}
	return "…" + s[start:]
}
