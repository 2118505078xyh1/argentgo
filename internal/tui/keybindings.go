package tui

import "strings"

// 本文件补上键位系统缺失的"解码 Key ↔ 规范键串"双向桥。
// 现有系统里 handleKeyLocked 用 Kind 枚举分派，而配置/别名/帮助用字符串
// （"ctrl+t" / "alt+b" / "enter"）；Canonical 把前者转成后者，使声明式
// keybinding 注册表成为可能。纯函数、零依赖、不改任何解码语义。

// Key.Canonical 返回该键的规范键串，与配置/别名表使用的写法一致：
//   - 组合键小写："ctrl+t"、"alt+b"、"ctrl+home"
//   - 命名键："enter"、"tab"、"esc"、"pgup"、"home"、"up" 等
//   - 可打印字符：其 rune 字符串（"a" / "中"）
//   - 鼠标："mouse_press" / "mouse_wheel_up" 等
//   - 未知/忽略：""
//
// 注意：KindCtrlB 的规范名是 "ctrl+b"（左移光标），工作区面板是 "alt+b"；
// 与旧注释（已过时）无关，这里严格按实际键位语义。
func (k Key) Canonical() string {
	switch k.Kind {
	case KindRune:
		if k.Rune == 0 {
			return ""
		}
		return string(k.Rune)
	case KindEnter:
		return "enter"
	case KindNewline:
		return "ctrl+j"
	case KindBackspace:
		return "backspace"
	case KindDelete:
		return "delete"
	case KindLeft:
		return "left"
	case KindRight:
		return "right"
	case KindUp:
		return "up"
	case KindDown:
		return "down"
	case KindHome:
		return "home"
	case KindEnd:
		return "end"
	case KindPageUp:
		return "pgup"
	case KindPageDown:
		return "pgdn"
	case KindScrollUp:
		return "scroll_up"
	case KindScrollDown:
		return "scroll_down"
	case KindTab:
		return "tab"
	case KindBackTab:
		return "shift+tab"
	case KindEsc:
		return "esc"
	case KindCtrlC:
		return "ctrl+c"
	case KindCtrlA:
		return "ctrl+a"
	case KindCtrlE:
		return "ctrl+e"
	case KindCtrlK:
		return "ctrl+k"
	case KindCtrlU:
		return "ctrl+u"
	case KindCtrlW:
		return "ctrl+w"
	case KindCtrlL:
		return "ctrl+l"
	case KindAltEnter:
		return "alt+enter"
	case KindPaste:
		return "paste"
	case KindIgnore:
		return ""
	case KindCtrlT:
		return "ctrl+t"
	case KindCtrlB:
		return "ctrl+b"
	case KindCtrlP:
		return "ctrl+p"
	case KindCtrlS:
		return "ctrl+s"
	case KindAltU:
		return "alt+u"
	case KindAltR:
		return "alt+r"
	case KindMousePress:
		return "mouse_press"
	case KindMouseDrag:
		return "mouse_drag"
	case KindMouseRelease:
		return "mouse_release"
	case KindMouseWheelUp:
		return "mouse_wheel_up"
	case KindMouseWheelDown:
		return "mouse_wheel_down"
	case KindCtrlG:
		return "ctrl+g"
	case KindCtrlO:
		return "ctrl+o"
	case KindCtrlX:
		return "ctrl+x"
	case KindCtrlY:
		return "ctrl+y"
	case KindAltY:
		return "alt+y"
	case KindCtrlF:
		return "ctrl+f"
	case KindCtrlD:
		return "ctrl+d"
	case KindCtrlN:
		return "ctrl+n"
	case KindCtrlR:
		return "ctrl+r"
	case KindCtrlV:
		return "ctrl+v"
	case KindAltT:
		return "alt+t"
	case KindAltB:
		return "alt+b"
	case KindAltP:
		return "alt+p"
	case KindAltS:
		return "alt+s"
	case KindAltC:
		return "alt+c"
	case KindCtrlHome:
		return "ctrl+home"
	case KindCtrlEnd:
		return "ctrl+end"
	case KindAltL:
		return "alt+l"
	}
	return ""
}

// resolveCustomBindingLocked 在用户自定义键位表里查当前键：命中且映射到
// 不同默认键时，把动作翻译回默认键并重新进入主 switch 执行。
// 调用方须持有 m.mu。返回 handled=false 表示未命中（走常规分派）。
func (m *Model) resolveCustomBindingLocked(key Key) (KeyAction, bool) {
	if m.reentry || len(m.bindings) == 0 {
		return ActionNone, false
	}
	canonical := key.Canonical()
	if canonical == "" {
		return ActionNone, false
	}
	action, ok := m.bindings.Resolve(canonical)
	if !ok || action == ActNone {
		return ActionNone, false
	}
	defaultKey, ok := actionDefaultKey(action)
	if !ok {
		return ActionNone, false
	}
	// 键串已是默认键 → 交给常规主 switch（避免翻译回自己形成递归）。
	if strings.EqualFold(defaultKey, canonical) {
		return ActionNone, false
	}
	translated, ok := specToKey(defaultKey)
	if !ok {
		return ActionNone, false
	}
	m.reentry = true
	defer func() { m.reentry = false }()
	return m.handleKeyLocked(translated), true
}

// specToKey 把规范键串转成 Key（覆盖有专属 Kind 的键与单字符 rune）。
func specToKey(spec string) (Key, bool) {
	if spec == "" {
		return Key{}, false
	}
	if kind, ok := ParseKeySpec(spec); ok {
		return Key{Kind: kind}, true
	}
	runes := []rune(spec)
	if len(runes) == 1 {
		return Key{Kind: KindRune, Rune: runes[0]}, true
	}
	return Key{}, false
}

// ParseKeySpec 把规范键串解析回 Kind（仅覆盖有专属 Kind 的键）。
// 它是 Canonical 的逆，但有意不处理 KindRune（单字符由调用方按 rune 处理）
// 与鼠标。未识别返回 (KindIgnore, false)。
func ParseKeySpec(spec string) (Kind, bool) {
	s := strings.ToLower(strings.TrimSpace(spec))
	for kind := KindRune; kind <= KindAltL; kind++ {
		key := Key{Kind: kind}
		if kind == KindRune {
			continue // rune 由调用方处理
		}
		if key.Canonical() == s {
			return kind, true
		}
	}
	return KindIgnore, false
}
