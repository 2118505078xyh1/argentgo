package tui

import (
	"fmt"
	"strings"
)

// 隐藏行的标记文本沿用既有英文模板，前缀与后缀保持不变：
// 形如 "… (14 lines hidden)"。标记自身占据输出中的一个行槽位。
const (
	hiddenMarkerPrefix = "… ("
	hiddenMarkerSuffix = " lines hidden)"
)

// HeadTailToolPreview 让工具结果预览在行超限时保留头尾窗口，
// 中间用 "… (N lines hidden)" 标记代替，使长输出末尾的错误或结果
// 不会被裁掉。它默认关闭，使默认的逐字节预览路径与现有布局和
// 基准测试保持一致；打开此开关即可在预览中看到尾部窗口。
// 把它保留为普通包变量，方便以后接到 CLI 标志或配置而无需触碰渲染器。
var HeadTailToolPreview = false

// previewHeadTail 返回 lines 的头尾窗口：前 max/2 行与后 max-max/2 行，
// 中间插入一行 "… (N lines hidden)"，其中 N 为被隐藏的行数。
//
// 这是纯函数：它按行切片，从不按字节切割 UTF-8 文本，因此汉字、
// emoji 与其他多字节 rune 始终完整（CJK 安全）。行数不超过 max 时
// 内容原样返回，但返回的是副本，调用方改动不会污染输入；
// max <= 0 时返回 nil。标记行使用何种样式由调用方决定，这里只产出文本。
func previewHeadTail(lines []string, max int) []string {
	if max <= 0 {
		return nil
	}
	if len(lines) <= max {
		return append([]string(nil), lines...)
	}
	head := max / 2
	tail := max - head
	out := make([]string, 0, head+1+tail)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("%s%d%s", hiddenMarkerPrefix, len(lines)-head-tail, hiddenMarkerSuffix))
	out = append(out, lines[len(lines)-tail:]...)
	return out
}

// isHiddenMarker 报告一行文本是否为 previewHeadTail 产出的隐藏标记，
// 供渲染器把标记行套用较暗的样式。它拒绝空计数与非数字内容，
// 避免把普通输出误判为标记。
func isHiddenMarker(line string) bool {
	if !strings.HasPrefix(line, hiddenMarkerPrefix) || !strings.HasSuffix(line, hiddenMarkerSuffix) {
		return false
	}
	mid := line[len(hiddenMarkerPrefix) : len(line)-len(hiddenMarkerSuffix)]
	if mid == "" {
		return false
	}
	for _, r := range mid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// headTailOutputLines 在 HeadTailToolPreview 打开时渲染工具输出：
// 先把原始输出切成逻辑行，经 previewHeadTail 选出头尾窗口，再逐行
// 折叠到 bodyWidth。标记行使用较暗的样式以匹配旧预览的溢出行；
// 当输出未超限时，渲染结果与旧的逐行预览逐字一致。
func headTailOutputLines(raw string, limit, bodyWidth int, p Palette) []Line {
	lines := splitResultLines(strings.TrimRight(raw, "\n"))
	preview := previewHeadTail(lines, limit)
	out := make([]Line, 0, len(preview))
	for _, text := range preview {
		line := NewLine()
		line.Push("  ", Style{})
		if isHiddenMarker(text) {
			line.Push(Clip(text, bodyWidth), p.dim())
			out = append(out, line)
			continue
		}
		for _, segment := range wrapLimited(text, bodyWidth, 1) {
			line.Push(Clip(segment, bodyWidth), p.muted())
		}
		out = append(out, line)
	}
	return out
}
