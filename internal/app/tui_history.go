package app

import (
	"strings"

	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/tui"
)

// entriesToTUI 把会话历史（session.Entry，活动分支的线性对话）投影成 TUI
// 的展示条目。tui 不 import session，映射由 app 这一 composition root 完成，
// 与 native 的 messageFromSessionEvent 是同一职责的本地版本。
//
// 映射规则：
//   - user / assistant → EntryUser / EntryAssistant（正文）
//   - assistant.Reasoning 非空 → 正文前插一个推理条目（与实时 reasoning_delta 同形）
//   - tool → EntryTool（Name/输出，静态、非运行中）
//   - 其它角色（system/压缩摘要等）→ EntryNotice 或跳过
//
// 有界：正文按 rune 安全裁剪到 maxHistoryEntryRunes，避免超大历史把首帧渲染
// 拖慢或撑爆内存；裁剪标记让模型/用户知道原文更长。
const maxHistoryEntryRunes = 20000

func entriesToTUI(entries []session.Entry) []tui.Entry {
	out := make([]tui.Entry, 0, len(entries))
	for _, e := range entries {
		switch e.Role {
		case "user":
			if text := clipHistoryText(e.Content); text != "" {
				out = append(out, tui.Entry{Kind: tui.EntryUser, Text: text})
			}
		case "assistant":
			if reasoning := strings.TrimSpace(e.Reasoning); reasoning != "" {
				out = append(out, tui.Entry{Kind: tui.EntryReasoning, Text: clipHistoryText(reasoning)})
			}
			if text := clipHistoryText(e.Content); text != "" {
				out = append(out, tui.Entry{Kind: tui.EntryAssistant, Text: text})
			}
		case "tool":
			// 历史工具结果都是已完成的静态卡片：Running/live 必须为 false，
			// 否则渲染层会为它们启动 spinner 并进入逐帧 live 路径。
			out = append(out, tui.Entry{
				Kind:   tui.EntryTool,
				Name:   e.Name,
				Args:   toolArgsFromData(e.Data),
				Output: clipHistoryText(e.Content),
			})
		}
		// system 与未知角色不进 transcript（与 native 的 default:skip 一致）。
	}
	return out
}

// clipHistoryText 按显示宽度安全裁剪历史正文（tui.Truncate 以字素为单位，
// CJK/emoji 不被切坏且输出合法 UTF-8，并自带省略号标记）。
func clipHistoryText(s string) string {
	return tui.Truncate(s, maxHistoryEntryRunes)
}

// toolArgsFromData 尽力从工具记录的 Data 里取出可读的参数摘要。Data 可能是
// 调用参数 JSON；解析失败时返回空串（工具卡片仍能显示名称与输出）。
func toolArgsFromData(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(data))
	// 只保留单行、有界的可读片段，避免原始 JSON 撑宽工具卡片。
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return tui.Truncate(s, 200)
}
