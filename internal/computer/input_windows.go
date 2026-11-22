//go:build windows

package computer

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf16"
	"unsafe"
)

const (
	keyEventKeyUp      = 0x0002
	mouseEventLeftDown = 0x0002
	mouseEventLeftUp   = 0x0004
	mouseEventWheel    = 0x0800
	cfUnicodeText      = 13
	gmemMoveable       = 0x0002
	vkShift            = 0x10
	vkControl          = 0x11
	vkMenu             = 0x12 // ALT
	wheelDeltaPerNotch = 120
)

// Click 把鼠标移到 (x, y) 并单击左键。
func Click(x, y int) error {
	if ok, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		return fmt.Errorf("computer: SetCursorPos(%d,%d) failed", x, y)
	}
	procMouseEvent.Call(mouseEventLeftDown, 0, 0, 0, 0)
	procMouseEvent.Call(mouseEventLeftUp, 0, 0, 0, 0)
	return nil
}

// Scroll 把鼠标移到 (x, y) 并滚动滚轮。amount > 0 向上，< 0 向下。
func Scroll(x, y, amount int) error {
	if ok, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		return fmt.Errorf("computer: SetCursorPos(%d,%d) failed", x, y)
	}
	delta := int32(amount * wheelDeltaPerNotch)
	procMouseEvent.Call(mouseEventWheel, 0, 0, uintptr(uint32(delta)), 0)
	return nil
}

// Type 通过剪贴板 CF_UNICODETEXT + Ctrl+V 输入文本。
// 这绕开 VkKeyScan 不支持中文/emoji 的限制，是 computer-use 的标准做法。
// 副作用：会覆盖系统剪贴板当前内容。
func Type(text string) error {
	if text == "" {
		return nil
	}
	if err := setClipboardUnicode(text); err != nil {
		return err
	}
	// 松开可能残留的修饰键，避免粘滞状态污染 Ctrl+V。
	for _, vk := range []uint16{vkControl, vkMenu, vkShift} {
		procKeybdEvent.Call(uintptr(vk), 0, keyEventKeyUp, 0)
	}
	procKeybdEvent.Call(vkControl, 0, 0, 0)
	procKeybdEvent.Call(0x56 /* V */, 0, 0, 0)
	procKeybdEvent.Call(0x56, 0, keyEventKeyUp, 0)
	procKeybdEvent.Call(vkControl, 0, keyEventKeyUp, 0)
	return nil
}

// Key 发送组合键，例如 "ctrl+c"、"alt+tab"、"enter"、"ctrl+shift+s"。
// 键名不区分大小写；支持单键、ctrl/alt/shift/win 修饰键、a-z、0-9、
// F1-F12 及常见导航键。未知键名返回错误。
func Key(combo string) error {
	combo = strings.TrimSpace(strings.ToLower(combo))
	if combo == "" {
		return errors.New("computer: empty key combination")
	}
	parts := strings.Split(combo, "+")
	vks := make([]uint16, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		vk, ok := virtualKey(p)
		if !ok {
			return fmt.Errorf("computer: unknown key %q in %q", p, combo)
		}
		vks = append(vks, vk)
	}
	// 松开可能残留的修饰键。
	for _, vk := range []uint16{vkControl, vkMenu, vkShift, 0x5B} {
		procKeybdEvent.Call(uintptr(vk), 0, keyEventKeyUp, 0)
	}
	for _, vk := range vks {
		procKeybdEvent.Call(uintptr(vk), 0, 0, 0)
	}
	for i := len(vks) - 1; i >= 0; i-- {
		procKeybdEvent.Call(uintptr(vks[i]), 0, keyEventKeyUp, 0)
	}
	return nil
}

// setClipboardUnicode 把 UTF-8 文本以 CF_UNICODETEXT 写入剪贴板。
func setClipboardUnicode(text string) error {
	if ok, _, _ := procOpenClipboard.Call(0); ok == 0 {
		return errors.New("computer: OpenClipboard failed")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	u := utf16.Encode([]rune(text))
	sizeBytes := uintptr(len(u)+1) * 2 // 结尾 UTF-16 NUL
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, sizeBytes)
	if h == 0 {
		return errors.New("computer: GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("computer: GlobalLock failed")
	}
	// GlobalLock 返回的是 GlobalAlloc 分配的系统内存地址，不是 Go 堆对象：
	// GC 既不会移动它，也不需要追踪它。这里通过 SliceHeader 直接装配切片，
	// 避免把整数地址转成 unsafe.Pointer——vet 会把后者误判为 Go 对象指针运算。
	var dst []uint16
	header := (*reflect.SliceHeader)(unsafe.Pointer(&dst))
	header.Data = p
	header.Len = len(u) + 1
	header.Cap = len(u) + 1
	copy(dst, u)
	dst[len(u)] = 0
	procGlobalUnlock.Call(h)

	ret, _, _ := procSetClipboardData.Call(cfUnicodeText, h)
	if ret == 0 {
		procGlobalFree.Call(h)
		return errors.New("computer: SetClipboardData failed")
	}
	// 成功后 h 归系统所有，不能在此释放。
	return nil
}

// virtualKey 把键名映射到 Win32 virtual-key code。
func virtualKey(name string) (uint16, bool) {
	switch name {
	case "ctrl", "control":
		return vkControl, true
	case "alt", "menu":
		return vkMenu, true
	case "shift":
		return vkShift, true
	case "win", "super", "meta":
		return 0x5B, true
	case "enter", "return":
		return 0x0D, true
	case "tab":
		return 0x09, true
	case "esc", "escape":
		return 0x1B, true
	case "space":
		return 0x20, true
	case "backspace":
		return 0x08, true
	case "delete", "del":
		return 0x2E, true
	case "home":
		return 0x24, true
	case "end":
		return 0x23, true
	case "pageup":
		return 0x21, true
	case "pagedown":
		return 0x22, true
	case "up":
		return 0x26, true
	case "down":
		return 0x28, true
	case "left":
		return 0x25, true
	case "right":
		return 0x27, true
	case "insert":
		return 0x2D, true
	}
	if len(name) == 1 {
		c := name[0]
		switch {
		case c >= 'a' && c <= 'z':
			return uint16(c - 'a' + 0x41), true
		case c >= '0' && c <= '9':
			return uint16(c - '0' + 0x30), true
		}
	}
	if len(name) == 2 && name[0] == 'f' {
		if n := name[1] - '1'; n >= 0 && n <= 11 {
			return uint16(0x70 + n), true // F1..F12
		}
	}
	return 0, false
}
