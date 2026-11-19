//go:build windows

package nativegui

import (
	"context"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/2118505078xyh1/argentgo/internal/gui"
)

const (
	sidebarW  = 240
	topbarH   = 64
	composerH = 72
	padding   = 32
)

var gwlpWndProc = ^uintptr(3) // GWL_WNDPROC == -4

var (
	wndProcCallback  = syscall.NewCallback(wndProc)
	editProcCallback = syscall.NewCallback(editProc)

	hComposer uintptr
	oldEdit   uintptr
	hwndMain  uintptr

	fontUI      uintptr
	fontSmall   uintptr
	fontMono    uintptr
	fontTitle   uintptr
	brushBG     uintptr
	brushSide   uintptr
	brushCard   uintptr
	brushAccent uintptr

	clrBG, clrSide, clrText, clrDim, clrAccent, clrBorder, clrCard uint32
	sendBtnRect                                                    rect
)

func rgb(r, g, b byte) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

// clrOnAccent is dark text on the coral fill in both themes.
func clrOnAccent() uint32 {
	if current != nil && !current.dark {
		return rgb(255, 255, 255)
	}
	return rgb(27, 19, 16)
}

func applyTheme(dark bool) {
	if dark {
		clrBG, clrSide, clrCard = rgb(23, 23, 22), rgb(19, 19, 18), rgb(32, 32, 30)
		clrText, clrDim, clrAccent, clrBorder = rgb(232, 230, 223), rgb(155, 153, 145), rgb(217, 119, 87), rgb(59, 58, 53)
	} else {
		clrBG, clrSide, clrCard = rgb(245, 244, 239), rgb(239, 238, 232), rgb(255, 255, 255)
		clrText, clrDim, clrAccent, clrBorder = rgb(47, 46, 41), rgb(125, 123, 114), rgb(184, 82, 44), rgb(220, 217, 207)
	}
	if brushBG != 0 {
		pDeleteObject.Call(brushBG)
		pDeleteObject.Call(brushSide)
		pDeleteObject.Call(brushCard)
		pDeleteObject.Call(brushAccent)
	}
	brushBG, _ = createBrush(clrBG)
	brushSide, _ = createBrush(clrSide)
	brushCard, _ = createBrush(clrCard)
	brushAccent, _ = createBrush(clrAccent)
}

func createBrush(c uint32) (uintptr, error) {
	r, _, err := pCreateSolidBrush.Call(uintptr(c))
	if r == 0 {
		return 0, err
	}
	return r, nil
}

func createFont(height int32, face string, weight uint32, italic bool) uintptr {
	i := uintptr(0)
	if italic {
		i = 1
	}
	r, _, _ := pCreateFont.Call(
		uintptr(height), 0, 0, 0, uintptr(weight), i, 0, 0,
		0, 0, 0, 0, 0, uintptr(unsafe.Pointer(utf16Ptr(face))),
	)
	return r
}

func wndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	s := current
	switch msg {
	case 0x0001: // WM_CREATE
		hwndMain = hwnd
		fontUI = createFont(-15, "Segoe UI", 400, false)
		fontSmall = createFont(-12, "Segoe UI", 500, false)
		fontMono = createFont(-13, "Consolas", 400, false)
		fontTitle = createFont(-26, "Segoe UI", 600, false)
		applyTheme(true)
		hComposer, _, _ = pCreateWindowEx.Call(
			0, uintptr(unsafe.Pointer(&editClassBuffer[0])), 0,
			wsChild|wsVisible|wsTabStop|wsBorder|esMultiline|esAutoVScroll,
			0, 0, 0, 0, hwnd, 101, 0, 0,
		)
		dark := int32(1)
		pDwmSetAttribute.Call(hwnd, dwmDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
		oldEdit, _, _ = pGetWindowLongPtr.Call(hComposer, gwlpWndProc)
		pSetWindowLongPtr.Call(hComposer, gwlpWndProc, editProcCallback)
		return 0

	case wmPaint:
		var ps paintStruct
		hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		paint(hdc, hwnd)
		pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmSize:
		layout(hwnd)
		return 0

	case wmAppEvent:
		invalidate(hwnd)
		return 0

	case wmCtlColorEdit:
		pSetTextColor.Call(wparam, uintptr(clrText))
		pSetBkColor.Call(wparam, uintptr(clrCard))
		return brushCard

	case wmLBUTTONDOWN:
		x := int32(int16(lparam & 0xffff))
		y := int32(int16(lparam >> 16))
		if x >= sendBtnRect.Left && x <= sendBtnRect.Right && y >= sendBtnRect.Top && y <= sendBtnRect.Bottom {
			sendComposer(s)
			return 0
		}
		if s != nil && x < sidebarW {
			// Session rows start below the brand.
			idx := (y - 92) / 42
			s.mu.Lock()
			if idx >= 0 && int(idx) < len(s.sessions) {
				row := s.sessions[idx]
				s.mu.Unlock()
				if row.Path != "" && s != nil {
					sendCommand(s, gui.Command{Kind: gui.CommandOpenSession, Path: row.Path})
				}
				return 0
			}
			s.mu.Unlock()
		}
		if s != nil && x > sidebarW && y < topbarH && int(x) > widthOf(hwnd)-96 {
			s.mu.Lock()
			s.dark = !s.dark
			applyTheme(s.dark)
			s.mu.Unlock()
			invalidate(hwnd)
		}
		return 0

	case wmMouseWheel:
		if s != nil {
			delta := int32(int16(wparam >> 16))
			s.mu.Lock()
			s.scroll -= int(delta / 3)
			if s.scroll < 0 {
				s.scroll = 0
			}
			s.mu.Unlock()
			invalidate(hwnd)
		}
		return 0

	case wmKeyDown:
		control := lparam&0x08 != 0
		shift := lparam&0x40 != 0
		if control && (wparam == 0x5A || wparam == 0x59) && s != nil {
			retry := wparam == 0x59 || shift
			s.mu.Lock()
			h, sid, ctx := s.history, s.sessionID, s.ctx
			s.mu.Unlock()
			if h != nil && sid != "" {
				if ctx == nil {
					ctx = context.Background()
				}
				var err error
				if retry {
					err = h.Retry(ctx, sid, 1)
				} else {
					err = h.Undo(ctx, sid, 1)
				}
				s.mu.Lock()
				if err != nil {
					s.status = "失败: " + firstLine(err.Error())
				} else if retry {
					s.status = "已从检查点分叉"
				} else {
					s.status = "已恢复检查点文件"
				}
				s.mu.Unlock()
				invalidate(hwnd)
				return 0
			}
		}
		if wparam == 0x1B { // Esc closes an approval, otherwise the window.
			if s != nil {
				s.mu.Lock()
				if s.approval != nil {
					ap := *s.approval
					s.approval = nil
					s.mu.Unlock()
					if ap.Reply != nil {
						ap.Reply <- false
					}
					return 0
				}
				s.mu.Unlock()
			}
			pPostMessage.Call(hwnd, wmClose, 0, 0)
			return 0
		}
		if wparam == 0x59 || wparam == 0x4E { // Y / N approval
			if s != nil {
				s.mu.Lock()
				ap := s.approval
				s.approval = nil
				s.mu.Unlock()
				if ap != nil && ap.Reply != nil {
					ap.Reply <- wparam == 0x59
				}
				return 0
			}
		}
		return 0

	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0

	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, uintptr(msg), wparam, lparam)
	return r
}

// editProc keeps Enter as "send" while allowing Shift+Enter for a newline.
func editProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	if msg == wmKeyDown && wparam == 0x0D {
		shift := (lparam>>16)&0x2000 != 0
		if !shift {
			sendComposer(current)
			return 0
		}
	}
	r, _, _ := pCallWindowProc.Call(oldEdit, hwnd, uintptr(msg), wparam, lparam)
	return r
}

func sendCommand(s *state, command gui.Command) {
	if s == nil {
		return
	}
	// Commands are delivered by the caller's channel stored on the state via a
	// package-level hook set in Run.
	if send != nil {
		send <- command
		if command.Kind == gui.CommandPrompt {
			s.mu.Lock()
			s.busy = true
			s.status = "运行中"
			s.mu.Unlock()
		}
	}
}

var send chan<- gui.Command

func sendComposer(s *state) {
	if hComposer == 0 {
		return
	}
	n, _, _ := pGetWindowText.Call(hComposer, 0, 0)
	_ = n // length is the return value; fetch below with a fixed buffer.
	buf := make([]uint16, 8192)
	got, _, _ := pGetWindowText.Call(hComposer, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if got == 0 {
		return
	}
	text := strings.TrimSpace(string(utf16.Decode(buf[:got])))
	if text == "" {
		return
	}
	pSetWindowText.Call(hComposer, uintptr(unsafe.Pointer(utf16Ptr(""))))
	if s != nil {
		s.mu.Lock()
		s.rows = append(s.rows, row{kind: "user", text: text})
		s.mu.Unlock()
	}
	sendCommand(s, gui.Command{Kind: gui.CommandPrompt, Text: text})
	invalidate(hwndMain)
}

func layout(hwnd uintptr) {
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.Right), int(rc.Bottom)
	cy := h - composerH - 12
	pMoveWindow.Call(hComposer, uintptr(sidebarW+padding), uintptr(cy), uintptr(w-sidebarW-padding-110), uintptr(composerH-8), 1)
	invalidate(hwnd)
}

func invalidate(hwnd uintptr) {
	pInvalidateRect.Call(hwnd, 0, 1)
}

func widthOf(hwnd uintptr) int {
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	return int(rc.Right)
}

func paint(hdc uintptr, hwnd uintptr) {
	applyTheme(current.dark)
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.Right), int(rc.Bottom)

	oldFont, _, _ := pSelectObject.Call(hdc, fontUI)
	defer pSelectObject.Call(hdc, oldFont)
	pSetBkMode.Call(hdc, transparent)
	pSetTextColor.Call(hdc, uintptr(clrText))

	// Surfaces.
	fillRect(hdc, 0, 0, sidebarW, h, brushSide)
	fillRect(hdc, sidebarW, 0, w, h, brushBG)
	line(hdc, sidebarW-1, 0, sidebarW, h, clrBorder)

	// Brand.
	pSelectObject.Call(hdc, fontTitle)
	pSetTextColor.Call(hdc, uintptr(clrText))
	textAt(hdc, "argentgo", 20, 18, uint32(w))
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrAccent))
	textAt(hdc, "AGENT RUNTIME", 21, 50, uint32(w))
	pSelectObject.Call(hdc, fontUI)
	pSetTextColor.Call(hdc, uintptr(clrText))

	// Sessions.
	s := current
	s.mu.Lock()
	sessions := append([]gui.SessionRow(nil), s.sessions...)
	rows := append([]row(nil), s.rows...)
	model, cwd, status := s.model, s.cwd, s.status
	approval, scroll := s.approval, s.scroll
	busy := s.busy
	s.mu.Unlock()

	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrDim))
	textAt(hdc, "会话", 20, 84, uint32(w))
	for i, sess := range sessions {
		y := 112 + i*42
		if y > h-40 {
			break
		}
		pSelectObject.Call(hdc, fontUI)
		pSetTextColor.Call(hdc, uintptr(clrText))
		label := sess.Title
		if label == "" {
			label = "未命名会话"
		}
		textAt(hdc, truncate(label, 28), 20, y, sidebarW-32)
	}

	// Top bar.
	line(hdc, sidebarW, topbarH, w, topbarH+1, clrBorder)
	pSelectObject.Call(hdc, fontUI)
	pSetTextColor.Call(hdc, uintptr(clrText))
	textAt(hdc, model, sidebarW+padding, 22, uint32(w))
	pSelectObject.Call(hdc, fontMono)
	pSetTextColor.Call(hdc, uintptr(clrDim))
	textAt(hdc, truncate(cwd, 72), sidebarW+padding, 42, uint32(w))
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrAccent))
	textAt(hdc, "主题", w-88, 22, uint32(w))

	// Transcript, clipped above the composer.
	bottom := h - composerH - 16
	y := topbarH + 24 - scroll
	for _, item := range rows {
		switch item.kind {
		case "user":
			y = drawUser(hdc, item.text, sidebarW, y, w-sidebarW, bottom)
		case "assistant":
			y = drawAssistant(hdc, item.role, item.text, sidebarW, y, w-sidebarW, bottom)
		case "thinking":
			pSelectObject.Call(hdc, fontSmall)
			pSetTextColor.Call(hdc, uintptr(clrDim))
			hgt := measure(hdc, item.text, w-sidebarW-padding*2, fontSmall)
			textBox(hdc, item.text, sidebarW+padding, y, w-sidebarW-padding*2, hgt, fontSmall, clrDim, 0)
			y += hgt + 14
		case "tool":
			pSelectObject.Call(hdc, fontMono)
			pSetTextColor.Call(hdc, uintptr(clrAccent))
			hgt := measure(hdc, "› "+item.role+"  "+truncate(item.text, 90), w-sidebarW-padding*2, fontMono)
			textBox(hdc, "› "+item.role+"  "+truncate(item.text, 90), sidebarW+padding, y, w-sidebarW-padding*2, hgt, fontMono, clrAccent, 0)
			y += hgt + 10
		case "notice":
			pSelectObject.Call(hdc, fontSmall)
			pSetTextColor.Call(hdc, uintptr(clrDim))
			hgt := measure(hdc, item.text, w-sidebarW-padding*2, fontSmall)
			textBox(hdc, "· "+item.text, sidebarW+padding, y, w-sidebarW-padding*2, hgt, fontSmall, clrDim, 0)
			y += hgt + 10
		case "error":
			pSelectObject.Call(hdc, fontSmall)
			pSetTextColor.Call(hdc, uintptr(0x7b7be0))
			hgt := measure(hdc, item.text, w-sidebarW-padding*2, fontSmall)
			textBox(hdc, item.text, sidebarW+padding, y, w-sidebarW-padding*2, hgt, fontSmall, 0x7b7be0, 0)
			y += hgt + 14
		case "welcome":
			pSelectObject.Call(hdc, fontTitle)
			pSetTextColor.Call(hdc, uintptr(clrText))
			textAt(hdc, "有什么可以帮你？", sidebarW+padding, topbarH+72, uint32(w))
			pSelectObject.Call(hdc, fontUI)
			pSetTextColor.Call(hdc, uintptr(clrDim))
			textAt(hdc, item.text, sidebarW+padding, topbarH+122, uint32(w))
			y += 180
		}
		if y > bottom+400 {
			break
		}
	}
	pSelectObject.Call(hdc, fontSmall)
	if busy {
		pSetTextColor.Call(hdc, uintptr(clrAccent))
		textAt(hdc, "● "+status, sidebarW+padding, bottom+2, uint32(w))
	} else if status != "" {
		pSetTextColor.Call(hdc, uintptr(clrDim))
		textAt(hdc, status, sidebarW+padding, bottom+2, uint32(w))
	}

	// Composer surface.
	fillRect(hdc, sidebarW, h-composerH-12, w, h, brushBG)
	line(hdc, sidebarW, h-composerH-12, w, h-composerH-11, clrBorder)

	// Send button — drawn by us so it follows the theme exactly.
	sendBtnRect = rect{int32(w - 96), int32(h - composerH - 12), int32(w - 18), int32(h - 20)}
	rounded(hdc, int(sendBtnRect.Left), int(sendBtnRect.Top), int(sendBtnRect.Right), int(sendBtnRect.Bottom), clrAccent, clrAccent)
	pSetTextColor.Call(hdc, uintptr(clrOnAccent()))
	pSetBkMode.Call(hdc, transparent)
	rBtn := sendBtnRect
	pSelectObject.Call(hdc, fontUI)
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(&sendTextBuffer[0])), 2, uintptr(unsafe.Pointer(&rBtn)), dtCenter|dtVCenter|dtNoPrefix)

	// Approval bar.
	if approval != nil {
		ay := h - composerH - 104
		fillRect(hdc, sidebarW+padding, ay, w-padding, ay+80, brushCard)
		line(hdc, sidebarW+padding, ay, sidebarW+padding+3, ay+80, clrAccent)
		pSelectObject.Call(hdc, fontUI)
		pSetTextColor.Call(hdc, uintptr(clrText))
		textAt(hdc, "需要批准: "+approval.Name, sidebarW+padding+16, ay+10, uint32(w))
		pSelectObject.Call(hdc, fontMono)
		pSetTextColor.Call(hdc, uintptr(clrDim))
		textAt(hdc, truncate(approval.Arguments, 100), sidebarW+padding+16, ay+34, uint32(w))
		pSetTextColor.Call(hdc, uintptr(clrAccent))
		textAt(hdc, "Y 允许   N 拒绝   Esc 取消", sidebarW+padding+16, ay+56, uint32(w))
	}
}

func drawUser(hdc uintptr, text string, left, y, width, bottom int) int {
	font := fontUI
	hgt := measure(hdc, text, width-padding*2, font)
	y += 12
	if y > bottom {
		return y
	}
	boxW := width - padding*2
	textW := textWidth(hdc, text, font)
	if textW < boxW {
		boxW = textW + 28
	}
	x := left + width - padding - boxW
	rounded(hdc, x-10, y-8, x+boxW+10, y+hgt+8, clrCard, clrBorder)
	pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, uintptr(clrText))
	textBox(hdc, text, x, y, boxW, hgt, font, clrText, 0)
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrAccent))
	textAt(hdc, "你", x, y-22, uint32(x+boxW))
	return y + hgt + 22
}

func drawAssistant(hdc uintptr, role, text string, left, y, width, bottom int) int {
	font := fontUI
	hgt := measure(hdc, text, width-padding*2, font)
	y += 14
	if y > bottom {
		return y
	}
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrAccent))
	textAt(hdc, role, left+padding, y-20, uint32(left+width))
	pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, uintptr(clrText))
	textBox(hdc, text, left+padding, y, width-padding*2, hgt, font, clrText, 0)
	return y + hgt + 18
}

func fillRect(hdc uintptr, x1, y1, x2, y2 int, brush uintptr) {
	r := rect{int32(x1), int32(y1), int32(x2), int32(y2)}
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
}

func line(hdc uintptr, x1, y1, x2, y2 int, color uint32) {
	// A 1px rectangle is enough for separators and avoids MoveTo/LineTo state.
	b, _ := createBrush(color)
	fillRect(hdc, x1, y1, x2, y2, b)
	pDeleteObject.Call(b)
}

func rounded(hdc uintptr, x1, y1, x2, y2 int, fill, border uint32) {
	fb, _ := createBrush(fill)
	pen, _, _ := pCreatePen.Call(0, 1, uintptr(border))
	oldB, _, _ := pSelectObject.Call(hdc, fb)
	oldP, _, _ := pSelectObject.Call(hdc, pen)
	pRoundRect.Call(hdc, uintptr(x1), uintptr(y1), uintptr(x2), uintptr(y2), 12, 12)
	pSelectObject.Call(hdc, oldB)
	pSelectObject.Call(hdc, oldP)
	pDeleteObject.Call(fb)
	pDeleteObject.Call(pen)
}

func textAt(hdc uintptr, text string, x, y int, maxW uint32) {
	r := rect{int32(x), int32(y), int32(x + int(maxW)), int32(y + 28)}
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(utf16Ptr(text))), uintptr(len(utf16.Encode([]rune(text)))), uintptr(unsafe.Pointer(&r)), dtLeft|dtNoPrefix|dtVCenter)
}

func textBox(hdc uintptr, text string, x, y, w, h int, font uintptr, color uint32, flags uintptr) {
	pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, uintptr(color))
	r := rect{int32(x), int32(y), int32(x + w), int32(y + h)}
	u := utf16.Encode([]rune(text))
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&r)), dtWordBreak|dtNoPrefix|flags)
}

func measure(hdc uintptr, text string, w int, font uintptr) int {
	pSelectObject.Call(hdc, font)
	r := rect{0, 0, int32(w), 0}
	u := utf16.Encode([]rune(text))
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&r)), dtCalcRect|dtWordBreak|dtNoPrefix)
	return int(r.Bottom - r.Top)
}

func textWidth(hdc uintptr, text string, font uintptr) int {
	pSelectObject.Call(hdc, font)
	var sz size
	u := utf16.Encode([]rune(text))
	pGetTextExtent.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&sz)))
	return int(sz.CX)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
