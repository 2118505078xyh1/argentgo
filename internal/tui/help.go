package tui

// 本文件把 locale 里那条超长的 help 字符串变成结构化的多行说明。
// 它复用 keys.go 已经实现的 HintBindings / ShortHelp 原语，并让
// KeyBinding.Enabled 真正参与生产路径（不再只有测试引用）。
// 底部按键栏 hintLine 按用户决定恒为空行，这里不触碰它。

// helpStateEnabled 按当前 UI 状态决定帮助条目的可见性。
// 需要宿主钩子的撤销/重试在钩子未安装时隐藏，避免出现按了没反应的条目；
// 面板开关、滚动、编辑等按键在任何状态下都可用，一律保留。
func helpStateEnabled(m *Model, bindings []KeyBinding) {
	for i := range bindings {
		b := &bindings[i]
		switch b.Key {
		case "alt+u":
			b.Enabled = m.undoFn != nil
		case "alt+r":
			b.Enabled = m.retryFn != nil
		default:
			b.Enabled = true
		}
	}
}

// helpSegmentWidth 返回单个绑定渲染后的宽度。复用 ShortHelp 测量，
// 保证分组用的宽度和最终渲染的口径完全一致（同一套整条丢弃规则）。
func helpSegmentWidth(b KeyBinding) int {
	return TextWidth(ShortHelp([]KeyBinding{b}, 1<<20, TextWidth))
}

// helpNoticeLines 把已经设好 Enabled 的绑定渲染成若干行 notice 文本。
//
// 绑定按原始顺序贪心分组：一组加上一个新条目后仍不超过 width 才收入，
// 否则先把当前组渲染为一行再开新组。因为 ShortHelp 只整条丢弃条目、
// 绝不切开按键，所以一个 "key desc" 要么完整地落在某一行，要么被省略号
// 代替，窄终端不会出现半个按键。每行最后再过一遍 Wrap 作为宽度守门：
// 分组已保证不切断按键，Wrap 只兜底字素边界（CJK/emoji 不被折在中间）。
func helpNoticeLines(bindings []KeyBinding, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	var group []KeyBinding
	groupWidth := 0
	sepWidth := TextWidth(shortHelpSep)
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		segWidth := helpSegmentWidth(b)
		if segWidth == 0 {
			continue
		}
		add := segWidth
		if len(group) > 0 {
			add += sepWidth
		}
		if len(group) > 0 && groupWidth+add > width {
			lines = appendHelpLine(lines, group, width)
			group = nil
			groupWidth = 0
			add = segWidth
		}
		group = append(group, b)
		groupWidth += add
	}
	if len(group) > 0 {
		lines = appendHelpLine(lines, group, width)
	}
	return lines
}

func appendHelpLine(lines []string, group []KeyBinding, width int) []string {
	line := ShortHelp(group, width, TextWidth)
	for _, wrapped := range Wrap(line, width) {
		lines = append(lines, wrapped)
	}
	return lines
}

// helpNoticeLocked 生成 /help 的结构化说明行。notice 以 "! " 为前缀绘制，
// 故正文宽度给前缀留两格；宽度未知时退回一个保守的默认值。
func (m *Model) helpNoticeLocked() []string {
	bindings := HintBindings(text(m.locale, "help"))
	helpStateEnabled(m, bindings)
	width := m.bodyW - 2
	if width < 1 {
		width = m.bodyW
	}
	if width < 1 {
		width = 80
	}
	return helpNoticeLines(bindings, width)
}
