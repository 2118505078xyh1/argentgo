package update

import "strings"

// normalizeVersionString 把用户简写的版本号规范化为三段式语义化版本字符串。
// 具体规则：去掉可选的 v/V 前缀；去掉 "+" 及其后的构建元数据；把一到三段的
// 数字核心右补零到三段；保留 "-" 及其后的预发布标识。无法识别的输入原样返回。
//
// 本函数为纯函数：只依赖参数，不读写全局状态。所有分隔与切片都按 ASCII 字节
// 定位，不会在 UTF-8 多字节字符中间切分，因此对 CJK 输入安全。同时限制输入
// 总长与核心段数，避免病态输入带来的无界处理。
func normalizeVersionString(s string) string {
	// 有界：超过该长度直接视为非法，避免病态输入触发无界扫描。
	const maxLength = 256
	if len(s) > maxLength {
		return s
	}

	text := strings.TrimSpace(s)
	// 去掉可选的 v/V 前缀。
	text = strings.TrimPrefix(strings.TrimPrefix(text, "v"), "V")
	if text == "" {
		return s
	}
	// 构建元数据位于 "+" 之后，先剥离，避免其中的 "-" 干扰预发布解析。
	if index := strings.IndexByte(text, '+'); index >= 0 {
		text = text[:index]
	}

	// 拆分核心与预发布："-" 及其后内容原样保留。
	core := text
	prerelease := ""
	if index := strings.IndexByte(text, '-'); index >= 0 {
		core = text[:index]
		rest := text[index+1:]
		if rest == "" {
			return s
		}
		for _, identifier := range strings.Split(rest, ".") {
			if identifier == "" {
				return s
			}
		}
		prerelease = text[index:]
	}
	if core == "" {
		return s
	}

	// 核心必须是 1 到 3 段纯 ASCII 数字，右补零到三段。
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return s
	}
	segments := [3]string{}
	for index, part := range parts {
		if !isNumericIdentifier(part) {
			return s
		}
		segments[index] = part
	}
	for index := len(parts); index < len(segments); index++ {
		segments[index] = "0"
	}
	return strings.Join(segments[:], ".") + prerelease
}
