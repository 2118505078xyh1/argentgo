package tui

import (
	"strings"
	"unicode/utf8"
)

// maxToolProgressLabelRunes 限制阶段标签的 rune 数量。按 rune 而非字节裁剪，
// 保证 CJK 标签不会被从 UTF-8 字符中间切断。
const maxToolProgressLabelRunes = 48

// toolProgressStages 列出 tools 包当前会发出的裸阶段行前缀（全部为 ASCII）。
// 历史工具进度通知不带显式标记，因此把它作为解析兜底；新接线只需给通知
// 加上 tool_progress 标记即可获得任意 CJK 阶段标签。
var toolProgressStages = []string{
	"bash job finished",
}

// ParseToolProgress 是 parseToolProgress 的导出入口，供 app 的事件桥接器
// 把 agent.EventNotice 转成 tui.Event 时提取阶段标签。
func ParseToolProgress(text string) (string, bool) { return parseToolProgress(text) }

// parseToolProgress 从工具进度通知文本中提取可读的阶段标签。识别两种形态：
//
//  1. 带显式标记的 "tool_progress<分隔符><label>"（分隔符为冒号、空白或
//     全角冒号，大小写不敏感），其后内容即阶段标签；
//  2. tools 包发出的裸阶段行 "<stage>: <detail>"，取冒号前的阶段名作为标签。
//
// 两种形态都会折叠空白并按 rune 上限裁剪，确保 CJK 文本始终有效。
// 空串、非进度文本返回 ok=false，调用方据此忽略事件。
func parseToolProgress(text string) (label string, ok bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return "", false
	}
	if rest, tagged := stripProgressMarker(s); tagged {
		return clipProgressLabel(rest), rest != ""
	}
	if stage, bare := knownProgressStage(s); bare {
		return clipProgressLabel(stage), true
	}
	return "", false
}

// stripProgressMarker 去掉开头的 tool_progress 标记及其分隔符，返回剩余文本。
// 要求标记后必须有分隔符，避免把 "tool_progressed" 之类的普通单词误判为进度。
func stripProgressMarker(s string) (rest string, ok bool) {
	const marker = "tool_progress"
	if len(s) < len(marker) || !strings.EqualFold(s[:len(marker)], marker) {
		return "", false
	}
	rest = s[len(marker):]
	if rest == "" {
		return "", false
	}
	r, size := utf8.DecodeRuneInString(rest)
	if r != ':' && r != '：' && r != ' ' && r != '\t' && r != '\n' && r != '\r' {
		return "", false
	}
	return strings.TrimSpace(rest[size:]), true
}

// knownProgressStage 识别 tools 包发出的裸阶段行，返回冒号前的阶段名。
func knownProgressStage(s string) (stage string, ok bool) {
	lower := strings.ToLower(s)
	for _, prefix := range toolProgressStages {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		// 明细可能含 CJK 或完整命令行；只取冒号前的阶段名，不进入标签。
		if i := strings.IndexAny(s, ":："); i > 0 {
			if name := strings.TrimSpace(s[:i]); name != "" {
				return name, true
			}
		}
		return s, true
	}
	return "", false
}

// clipProgressLabel 把标签整理成单行并按 rune 上限裁剪，保证 CJK 文本
// 不会被从字符中间切开。
func clipProgressLabel(label string) string {
	label = strings.Join(strings.Fields(label), " ")
	runes := []rune(label)
	if len(runes) > maxToolProgressLabelRunes {
		runes = runes[:maxToolProgressLabelRunes]
	}
	return string(runes)
}
