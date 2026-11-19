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
	sidebarW     = 240
	topbarH      = 64
	composerH    = 72
	padding      = 32
	columnGutter = 64
	maxColumnW   = 720
	minColumnW   = 160
	toolRowH     = 28
	approvalH    = 84
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
	fontBold    uintptr
	fontGlyph   uintptr
	fontKey     uintptr
	brushBG     uintptr
	brushSide   uintptr
	brushCard   uintptr
	brushAccent uintptr
	brushError  uintptr

	clrBG, clrSide, clrCard, clrText, clrDim, clrFaint uint32
	clrAccent, clrAccentHover, clrBorder, clrStroke    uint32
	clrError, clrErrorBg, clrWarn, clrOk, clrShadow    uint32
	sendBtnRect                                        rect
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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
		clrText, clrDim, clrFaint = rgb(232, 230, 223), rgb(155, 153, 145), rgb(109, 107, 100) // #6d6b64
		clrAccent, clrAccentHover = rgb(217, 119, 87), rgb(232, 139, 109)
		clrBorder, clrStroke = rgb(43, 43, 41), rgb(58, 57, 53) // separators vs. card outlines
		clrError, clrErrorBg = rgb(242, 119, 122), rgb(58, 36, 34)
		clrWarn, clrOk, clrShadow = rgb(229, 181, 103), rgb(127, 183, 126), rgb(10, 10, 9)
	} else {
		clrBG, clrSide, clrCard = rgb(245, 244, 239), rgb(239, 238, 232), rgb(255, 255, 255)
		clrText, clrDim, clrFaint = rgb(47, 46, 41), rgb(125, 123, 114), rgb(160, 157, 148)
		clrAccent, clrAccentHover = rgb(184, 82, 44), rgb(204, 99, 64)
		clrBorder, clrStroke = rgb(220, 217, 207), rgb(201, 197, 185)
		clrError, clrErrorBg = rgb(190, 56, 48), rgb(251, 233, 231)
		clrWarn, clrOk, clrShadow = rgb(168, 118, 10), rgb(46, 125, 50), rgb(210, 207, 198)
	}
	for _, b := range []uintptr{brushBG, brushSide, brushCard, brushAccent, brushError} {
		if b != 0 {
			pDeleteObject.Call(b)
		}
	}
	brushBG, _ = createBrush(clrBG)
	brushSide, _ = createBrush(clrSide)
	brushCard, _ = createBrush(clrCard)
	brushAccent, _ = createBrush(clrAccent)
	brushError, _ = createBrush(clrErrorBg)
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
		0, 0, 0, 5, 0, uintptr(unsafe.Pointer(utf16Ptr(face))), // CLEARTYPE_QUALITY
	)
	return r
}

// messageColumn returns the x/width of the centered transcript column.
func messageColumn(clientW int) (int, int) {
	w := clientW - sidebarW - columnGutter
	if w > maxColumnW {
		w = maxColumnW
	}
	if w < minColumnW {
		w = minColumnW
	}
	return sidebarW + (clientW-sidebarW-w)/2, w
}

func spinnerFrame(tick int) string {
	n := len(spinnerFrames)
	i := tick % n
	if i < 0 {
		i += n
	}
	return spinnerFrames[i]
}

// cursorVisible halves the 80ms tick into 320ms on/off phases.
func cursorVisible(tick int) bool { return (tick/4)%2 == 0 }

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func wndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	s := current
	switch msg {
	case 0x0001: // WM_CREATE
		hwndMain = hwnd
		fontUI = createFont(-14, "Segoe UI", 400, false)
		fontSmall = createFont(-13, "Segoe UI", 500, false)
		fontMono = createFont(-13, "Consolas", 400, false)
		fontTitle = createFont(-20, "Segoe UI", 600, false)
		fontBold = createFont(-13, "Segoe UI", 600, false)
		fontGlyph = createFont(-16, "Segoe UI Symbol", 500, false)
		fontKey = createFont(-11, "Consolas", 600, false)
		applyTheme(true)
		// Reduced-motion users get a static spinner and a solid cursor.
		if clientAnimationEnabled() {
			pSetTimer.Call(hwnd, animationTimerID, animationMS, 0)
		}
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
		// Double buffer: never paint animation frames directly on screen.
		var rc rect
		pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		w, h := int(rc.Right), int(rc.Bottom)
		memDC, _, _ := pCreateCompDC.Call(hdc)
		bmp, _, _ := pCreateCompBitmap.Call(hdc, uintptr(w), uintptr(h))
		oldBmp, _, _ := pSelectObject.Call(memDC, bmp)
		paint(memDC, hwnd)
		pBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), memDC, 0, 0, srcCopy)
		pSelectObject.Call(memDC, oldBmp)
		pDeleteObject.Call(bmp)
		pDeleteDC.Call(memDC)
		pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmSize:
		layout(hwnd)
		return 0

	case wmAppEvent:
		invalidate(hwnd)
		return 0

	case wmTimer:
		if wparam == animationTimerID && s != nil {
			s.mu.Lock()
			s.tick++
			s.mu.Unlock()
			invalidate(hwnd)
		}
		return 0

	case wmMouseMove:
		if s != nil && hComposer != 0 {
			x := int32(int16(lparam & 0xffff))
			y := int32(int16(lparam >> 16))
			hover := x >= sendBtnRect.Left && x <= sendBtnRect.Right && y >= sendBtnRect.Top && y <= sendBtnRect.Bottom
			s.mu.Lock()
			changed := s.hoverSend != hover
			s.hoverSend = hover
			s.mu.Unlock()
			if changed {
				invalidate(hwnd)
			}
		}
		return 0

	case wmCtlColorEdit:
		pSetTextColor.Call(wparam, uintptr(clrText))
		pSetBkColor.Call(wparam, uintptr(clrCard))
		return brushCard

	case wmLBUTTONDOWN:
		x := int32(int16(lparam & 0xffff))
		y := int32(int16(lparam >> 16))
		if x >= sendBtnRect.Left && x <= sendBtnRect.Right && y >= sendBtnRect.Top && y <= sendBtnRect.Bottom {
			if s != nil {
				s.mu.Lock()
				s.hoverSend = false
				s.mu.Unlock()
			}
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
		pKillTimer.Call(hwnd, animationTimerID)
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
	busy, hover, tick := s.busy, s.hoverSend, s.tick
	s.mu.Unlock()

	colX, colW := messageColumn(w)

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

	// Transcript, clipped above the composer and centered in a reading column.
	bottom := h - composerH - 16
	y := topbarH + 24 - scroll
	lastAssistant := ""
	for _, item := range rows {
		switch item.kind {
		case "user":
			y = drawUser(hdc, item.text, colX, y, colW, bottom)
		case "assistant":
			y = drawAssistant(hdc, item.role, item.text, colX, y, colW, bottom)
		case "thinking":
			pSelectObject.Call(hdc, fontSmall)
			pSetTextColor.Call(hdc, uintptr(clrDim))
			hgt := measure(hdc, item.text, colW, fontSmall)
			textBox(hdc, item.text, colX, y, colW, hgt, fontSmall, clrDim, 0)
			y += hgt + 14
		case "tool":
			y = drawTool(hdc, item, colX, y, colW, tick)
		case "notice":
			color := clrDim
			if item.role == "retried" || item.role == "failed" || item.role == "stream_error" {
				color = clrWarn
			}
			pSelectObject.Call(hdc, fontSmall)
			hgt := measure(hdc, item.text, colW-12, fontSmall)
			textBox(hdc, "· "+item.text, colX+12, y, colW-12, hgt, fontSmall, color, 0)
			y += hgt + 10
		case "error":
			y = drawError(hdc, item.text, colX, y, colW)
		case "welcome":
			drawWelcome(hdc, item.text, colX, colW, topbarH, bottom)
			y = bottom
		}
		lastAssistant = ""
		if item.kind == "assistant" {
			lastAssistant = item.text
		}
		if y > bottom+400 {
			break
		}
	}
	// Blinking caret at the end of the live assistant reply.
	if busy && lastAssistant != "" && y-18 <= bottom {
		caretLine := lastLine(lastAssistant)
		lineW := textWidth(hdc, caretLine, fontUI)
		if lineW > colW-6 {
			lineW = colW - 6
		}
		lineH := measure(hdc, caretLine, colW, fontUI)
		cy := y - 18 + lineH - 16
		if cy < topbarH {
			cy = topbarH
		}
		if cursorVisible(tick) {
			fillRect(hdc, colX+lineW+2, cy, colX+lineW+4, cy+16, brushAccent)
		}
	}
	pSelectObject.Call(hdc, fontSmall)
	if busy {
		pSetTextColor.Call(hdc, uintptr(clrAccent))
		textAt(hdc, spinnerFrame(tick)+" "+status, colX, bottom+2, uint32(colW))
	} else if status != "" {
		pSetTextColor.Call(hdc, uintptr(clrDim))
		textAt(hdc, status, colX, bottom+2, uint32(colW))
	}

	// Composer surface.
	fillRect(hdc, sidebarW, h-composerH-12, w, h, brushBG)
	line(hdc, sidebarW, h-composerH-12, w, h-composerH-11, clrBorder)

	// Send button — drawn by us so it follows the theme exactly.
	sendBtnRect = rect{int32(w - 96), int32(h - composerH - 12), int32(w - 18), int32(h - 20)}
	buttonFill := clrAccent
	if hover {
		buttonFill = clrAccentHover
	}
	rounded(hdc, int(sendBtnRect.Left), int(sendBtnRect.Top), int(sendBtnRect.Right), int(sendBtnRect.Bottom), buttonFill, buttonFill)
	pSetTextColor.Call(hdc, uintptr(clrOnAccent()))
	pSetBkMode.Call(hdc, transparent)
	rBtn := sendBtnRect
	pSelectObject.Call(hdc, fontUI)
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(&sendTextBuffer[0])), 2, uintptr(unsafe.Pointer(&rBtn)), dtCenter|dtVCenter|dtNoPrefix)

	// Approval card: rounded, hard shadow, keyboard keycaps.
	if approval != nil {
		aw := colW
		if aw > 560 {
			aw = 560
		}
		ax := colX + (colW-aw)/2
		ay := h - composerH - approvalH - 28
		bottomY := ay + approvalH
		roundRect(hdc, ax+2, ay+2, ax+aw+2, bottomY+2, clrShadow, clrShadow, 24, 24)
		roundRect(hdc, ax, ay, ax+aw, bottomY, clrCard, clrStroke, 24, 24)
		line(hdc, ax, ay+12, ax+2, bottomY-12, clrAccent)
		pSelectObject.Call(hdc, fontUI)
		pSetTextColor.Call(hdc, uintptr(clrText))
		textAt(hdc, "需要批准: "+approval.Name, ax+16, ay+8, uint32(aw-16))
		pSelectObject.Call(hdc, fontMono)
		pSetTextColor.Call(hdc, uintptr(clrDim))
		textAt(hdc, truncate(approval.Arguments, 100), ax+16, ay+30, uint32(aw-16))
		kx := drawKeycap(hdc, "Y", ax+16, ay+56)
		mutedLabel(hdc, "允许", kx, ay+56, uint32(ax+aw-kx))
		kx = drawKeycap(hdc, "N", kx+64, ay+56)
		mutedLabel(hdc, "拒绝", kx, ay+56, uint32(ax+aw-kx))
		kx = drawKeycap(hdc, "Esc", kx+64, ay+56)
		mutedLabel(hdc, "取消", kx, ay+56, uint32(ax+aw-kx))
	}
}

func drawUser(hdc uintptr, text string, colX, y, colW, bottom int) int {
	font := fontUI
	hgt := measure(hdc, text, colW-24, font)
	y += 12
	if y > bottom {
		return y
	}
	boxW := textWidth(hdc, text, font) + 24 // 12px inner padding on each side
	if boxW > colW {
		boxW = colW
	}
	x := colX + colW - boxW
	// Radius 10 (ellipse 20x20), fill only: pen matches the fill.
	roundRect(hdc, x, y-8, x+boxW, y+hgt+8, clrCard, clrCard, 20, 20)
	textBox(hdc, text, x+12, y, boxW-24, hgt, font, clrText, 0)
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrFaint))
	textAt(hdc, "你", x, y+hgt+10, uint32(colW))
	return y + hgt + 36
}

func drawAssistant(hdc uintptr, role, text string, colX, y, colW, bottom int) int {
	font := fontUI
	hgt := measure(hdc, text, colW, font)
	y += 14
	if y > bottom {
		return y
	}
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrAccent))
	textAt(hdc, role, colX, y-20, uint32(colW))
	textBox(hdc, text, colX, y, colW, hgt, font, clrText, 0)
	return y + hgt + 18
}

func drawTool(hdc uintptr, item row, colX, y, colW, tick int) int {
	y += 6
	roundRect(hdc, colX, y, colX+colW, y+toolRowH, clrCard, clrStroke, 12, 12)
	glyph, color := spinnerFrame(tick), clrAccent
	switch item.state {
	case "ok":
		glyph, color = "✓", clrOk
	case "error":
		glyph, color = "✗", clrError
	}
	textBox(hdc, glyph, colX+8, y, 16, toolRowH, fontGlyph, color, dtVCenter)
	name := truncate(item.role, 24)
	pSelectObject.Call(hdc, fontBold)
	pSetTextColor.Call(hdc, uintptr(clrText))
	nameW := textWidth(hdc, name, fontBold)
	textAt(hdc, name, colX+32, y, uint32(nameW+2))
	argX := colX + 40 + nameW
	if argX < colX+colW {
		pSelectObject.Call(hdc, fontSmall)
		textAt(hdc, truncate(item.text, 90), argX, y, uint32(colX+colW-argX))
	}
	return y + toolRowH + 8
}

func drawError(hdc uintptr, text string, colX, y, colW int) int {
	hgt := measure(hdc, text, colW-20, fontSmall)
	fillRect(hdc, colX, y, colX+colW, y+hgt+16, brushError)
	line(hdc, colX, y, colX+2, y+hgt+16, clrError) // 2px left rail
	textBox(hdc, text, colX+12, y+8, colW-20, hgt, fontSmall, clrError, 0)
	return y + hgt + 26
}

func drawWelcome(hdc uintptr, hint string, colX, colW, top, bottom int) {
	midY := top + (bottom-top)/2
	cx := colX + colW/2
	centeredText(hdc, "✦", cx, midY-72, fontSmall, clrAccent)
	centeredText(hdc, "有什么可以帮你？", cx, midY-32, fontTitle, clrText)
	centeredText(hdc, hint, cx, midY+16, fontSmall, clrDim)
}

func centeredText(hdc uintptr, text string, cx, y int, font uintptr, color uint32) {
	w := textWidth(hdc, text, font)
	x := cx - w/2
	pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, uintptr(color))
	textAt(hdc, text, x, y, uint32(w+12))
}

func mutedLabel(hdc uintptr, text string, x, y int, maxW uint32) {
	pSelectObject.Call(hdc, fontSmall)
	pSetTextColor.Call(hdc, uintptr(clrDim))
	textAt(hdc, text, x, y, maxW)
}

func drawKeycap(hdc uintptr, label string, x, y int) int {
	w := 16
	if len([]rune(label)) > 1 {
		w = 30
	}
	roundRect(hdc, x, y, x+w, y+16, clrSide, clrStroke, 8, 8)
	pSelectObject.Call(hdc, fontKey)
	pSetTextColor.Call(hdc, uintptr(clrText))
	r := rect{int32(x), int32(y), int32(x + w), int32(y + 16)}
	u := utf16.Encode([]rune(label))
	pDrawText.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&r)), dtCenter|dtVCenter|dtNoPrefix)
	return x + w + 8
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
	roundRect(hdc, x1, y1, x2, y2, fill, border, 12, 12)
}

// roundRect draws a filled rounded rectangle. A fill matching the border
// suppresses the outline altogether (PS_NULL pen).
func roundRect(hdc uintptr, x1, y1, x2, y2 int, fill, border uint32, ellipseW, ellipseH int) {
	fb, _ := createBrush(fill)
	style := uintptr(0) // PS_SOLID
	if fill == border {
		style = 5 // PS_NULL
	}
	pen, _, _ := pCreatePen.Call(style, 1, uintptr(border))
	oldB, _, _ := pSelectObject.Call(hdc, fb)
	oldP, _, _ := pSelectObject.Call(hdc, pen)
	pRoundRect.Call(hdc, uintptr(x1), uintptr(y1), uintptr(x2), uintptr(y2), uintptr(ellipseW), uintptr(ellipseH))
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
