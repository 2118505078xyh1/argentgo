package tools

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 本文件提供一个纯原语：把"超长工具输出落盘后的 artifact 路径"封装成
// 一段单行、可读的 metadata 文本，供上层（TUI / session）自行决定是否展示。
//
// 设计边界：
//   - FormatArtifactRef 是纯函数，不读写文件、不改入参、输出完全由入参决定；
//   - ArtifactRefFromSpill 是只读 I/O 小助手，仅做存在性/可读性检查与 stat；
//   - 两者都不做路径逃逸校验（不去 Abs/Clean/Rel，也不绑定工作区根目录）：
//     相对路径与 base 边界由调用方负责，本原语只把 path 当作不透明数据读取；
//   - 不注册到 Registry.Specs()，不接入 clip/CollapseOutput/TUI，零调用方，
//     因此不会改变任何现有工具行为或工具清单 golden。接线由未来调用方完成。

const (
	// artifactRefPreviewMaxBytes 是 Preview 参与格式化后的最大字节数（含省略号）。
	// 用字节做硬上界、用 rune 迭代保证不切断 UTF-8 字形，从而兼顾"有界"与 CJK 安全。
	artifactRefPreviewMaxBytes = 96
	// artifactRefEllipsis 在 Preview 被截断时追加。
	artifactRefEllipsis = "..."
)

// OutputArtifactRef 是一条 artifact 引用的可读表示。
//
// Path 是落盘文件的路径（由调用方保证其相对/base 合法性）；Bytes 是文件字节数；
// Preview 是可选的内容片段，格式化时按 rune 截断到有界长度。
// 字段为值类型字符串/整数，传入本包的任何函数都不会被修改。
type OutputArtifactRef struct {
	Path    string
	Bytes   int64
	Preview string
}

// FormatArtifactRef 把 ref 格式化成单行可读文本：
//
//	[完整输出已保存: <path> (<n> bytes)] <preview>
//
// Preview 为空时省略后面的片段部分，只输出方括号内的引用。
//
// 校验与不变式：
//   - Path 去空白后为空、或 Bytes 为负时视为非法引用，返回空串（调用方据此判断）；
//   - Path 与 Preview 中的换行/制表/C0/C1 控制字符以及 U+2028/U+2029 会被转义成
//     可见的可见序列，保证结果严格单行；反斜杠本身不转义（保留 Windows 路径可读性）；
//   - Preview 先转义再按 rune 截断到 artifactRefPreviewMaxBytes 字节以内，
//     超长时以 artifactRefEllipsis 结尾，绝不切断一个 UTF-8 rune；
//   - 纯函数：相同入参永远得到相同输出，且不产生任何副作用。
func FormatArtifactRef(ref OutputArtifactRef) string {
	if strings.TrimSpace(ref.Path) == "" || ref.Bytes < 0 {
		return ""
	}
	base := "[完整输出已保存: " + escapeArtifactControl(ref.Path) +
		" (" + strconv.FormatInt(ref.Bytes, 10) + " bytes)]"
	if ref.Preview == "" {
		return base
	}
	return base + " " + clipArtifactPreview(ref.Preview)
}

// ArtifactRefFromSpill 在 path 指向一个存在且可读的普通文件时构造引用（字节数取 stat）。
// 文件不存在、路径为空、不是普通文件、stat 或打开失败均返回 (零值, false)。
//
// 这是只读辅助：不创建/修改/删除文件，也不做路径逃逸校验（相对/base 由调用方负责）；
// Preview 留空，由调用方按需要填入。对同一份文件系统状态，结果是一致的。
func ArtifactRefFromSpill(path string) (OutputArtifactRef, bool) {
	if strings.TrimSpace(path) == "" {
		return OutputArtifactRef{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return OutputArtifactRef{}, false
	}
	// os.Stat 成功不代表内容可读；实际打开一次做可读性检查后立刻关闭。
	f, err := os.Open(path)
	if err != nil {
		return OutputArtifactRef{}, false
	}
	if err := f.Close(); err != nil {
		return OutputArtifactRef{}, false
	}
	return OutputArtifactRef{Path: path, Bytes: info.Size()}, true
}

// clipArtifactPreview 对 Preview 先做单行转义，再按 rune 截断到有界字节数。
// 迭代 rune 而非字节，确保中文等多字节字符不会被切在中间。
func clipArtifactPreview(preview string) string {
	escaped := escapeArtifactControl(preview)
	if len(escaped) <= artifactRefPreviewMaxBytes {
		return escaped
	}
	budget := artifactRefPreviewMaxBytes - len(artifactRefEllipsis)
	if budget < 0 {
		budget = 0
	}
	var b strings.Builder
	b.Grow(budget + len(artifactRefEllipsis))
	for _, r := range escaped {
		n := utf8.RuneLen(r)
		if b.Len()+n > budget {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + artifactRefEllipsis
}

// escapeArtifactControl 把会破坏"单行可读"的字符转成可见转义：
// 换行/回车/制表用 \n \r \t，其它 C0/C1 控制字符与行/段分隔符用 \xNN 或 \uNNNN。
// 反斜杠特意不转义，避免 Windows 路径被写成双反斜杠而降低可读性。
func escapeArtifactControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	const hex = "0123456789abcdef"
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\u2028':
			b.WriteString(`\u2028`)
		case '\u2029':
			b.WriteString(`\u2029`)
		default:
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				b.WriteString(`\x`)
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
