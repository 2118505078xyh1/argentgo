//go:build windows

// Package nativegui is a small, dependency-free Win32 chat window. It uses only
// the standard library's syscall package: no WebView, no browser, no cgo, no
// third-party module. The visual language follows Claude/Codex: warm neutral
// surfaces, one coral accent, generous spacing and monospace tool rows.
package nativegui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/2118505078xyh1/argentgo/internal/gui"
	"syscall"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassEx  = user32.NewProc("RegisterClassExW")
	pCreateWindowEx   = user32.NewProc("CreateWindowExW")
	pShowWindow       = user32.NewProc("ShowWindow")
	pUpdateWindow     = user32.NewProc("UpdateWindow")
	pGetMessage       = user32.NewProc("GetMessageW")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessage  = user32.NewProc("DispatchMessageW")
	pDefWindowProc    = user32.NewProc("DefWindowProcW")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")
	pBeginPaint       = user32.NewProc("BeginPaint")
	pEndPaint         = user32.NewProc("EndPaint")
	pInvalidateRect   = user32.NewProc("InvalidateRect")
	pGetClientRect    = user32.NewProc("GetClientRect")
	pGetWindowText    = user32.NewProc("GetWindowTextW")
	pSetWindowText    = user32.NewProc("SetWindowTextW")
	pSetFocus         = user32.NewProc("SetFocus")
	pPostMessage      = user32.NewProc("PostMessageW")
	pSendMessage      = user32.NewProc("SendMessageW")
	pMoveWindow       = user32.NewProc("MoveWindow")
	pDestroyWindow    = user32.NewProc("DestroyWindow")
	pSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	pCallWindowProc   = user32.NewProc("CallWindowProcW")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
	pCreateFont       = gdi32.NewProc("CreateFontW")
	pSelectObject     = gdi32.NewProc("SelectObject")
	pSetTextColor     = gdi32.NewProc("SetTextColor")
	pSetBkColor       = gdi32.NewProc("SetBkColor")
	pSetBkMode        = gdi32.NewProc("SetBkMode")
	pFillRect         = user32.NewProc("FillRect")
	pDrawText         = user32.NewProc("DrawTextW")
	pRoundRect        = gdi32.NewProc("RoundRect")
	pGetTextExtent    = gdi32.NewProc("GetTextExtentPoint32W")
	pCreatePen        = gdi32.NewProc("CreatePen")
	pGetModuleHandle  = kernel32.NewProc("GetModuleHandleW")
	pDwmSetAttribute  = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

const (
	wmDestroy     = 0x0002
	wmPaint       = 0x000F
	wmClose       = 0x0010
	wmKeyDown     = 0x0100
	wmMouseWheel  = 0x020A
	wmLBUTTONDOWN = 0x0201
	wmAppEvent    = 0x8001
	wmSize        = 0x0005

	swShow      = 5
	transparent = 1
	dtWordBreak = 0x0004
	dtCalcRect  = 0x0400
	dtLeft      = 0x0000
	dtCenter    = 0x0001
	dtRight     = 0x0002
	dtVCenter   = 0x0100
	dtNoPrefix  = 0x0800

	esMultiline        = 0x0004
	esAutoVScroll      = 0x0080
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00000100
	wsBorder           = 0x00800000
	wsOverlappedWindow = 0x00CF0000
	wmCtlColorEdit     = 0x0133
	dwmDarkMode        = 20
)

type rect struct{ Left, Top, Right, Bottom int32 }
type size struct{ CX, CY int32 }
type point struct{ X, Y int32 }

type paintStruct struct {
	hdc       uintptr
	erase     int32
	rc        rect
	restore   int32
	incUpdate int32
	reserved  [32]byte
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// Options carries the already-resolved engine seams. The window never imports
// app/agent: it only speaks gui.Command / gui.Event, exactly like the web page.
type Options struct {
	Cwd       string
	Model     string
	Provider  string
	Context   context.Context
	History   gui.TurnHistory
	Events    <-chan gui.Event
	Approvals <-chan gui.Approval
	Commands  chan<- gui.Command
}

type row struct {
	kind string
	role string
	text string
}

type state struct {
	mu        sync.Mutex
	rows      []row
	model     string
	cwd       string
	status    string
	sessionID string
	sessions  []gui.SessionRow
	approval  *gui.Approval
	history   gui.TurnHistory
	ctx       context.Context
	dark      bool
	busy      bool
	scroll    int
}

var current *state

// Run opens the window and blocks until it is closed.
func Run(opts Options) error {
	s := &state{
		rows:    []row{{kind: "welcome", text: "问点什么，或者输入 / 看命令。"}},
		model:   opts.Model,
		cwd:     opts.Cwd,
		status:  "就绪",
		dark:    true,
		history: opts.History,
		ctx:     opts.Context,
	}
	current = s

	hInstance, _, _ := pGetModuleHandle.Call(0)
	className := &nativeClassName[0]
	send = opts.Commands
	wc := &wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		Style:      0x0002 | 0x0001, // CS_HREDRAW | CS_VREDRAW
		WndProc:    wndProcCallback,
		Instance:   hInstance,
		ClassName:  className,
		Background: 0,
	}
	if r, _, _ := pRegisterClassEx.Call(uintptr(unsafe.Pointer(wc))); r == 0 {
		return fmt.Errorf("nativegui: RegisterClassEx failed")
	}

	title := &nativeTitle[0]
	hwnd, _, _ := pCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		0x80000000-320, 0x80000000-200, 1180, 760,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("nativegui: CreateWindowEx failed")
	}
	pShowWindow.Call(hwnd, swShow)
	pUpdateWindow.Call(hwnd)

	// Pump engine frames into the UI thread. Posting a private message keeps all
	// state mutation and GDI work on the window thread.
	go func() {
		for {
			select {
			case ev, ok := <-opts.Events:
				if !ok {
					return
				}
				applyEvent(s, ev)
				pPostMessage.Call(hwnd, wmAppEvent, 0, 0)
			case ap, ok := <-opts.Approvals:
				if !ok {
					return
				}
				s.mu.Lock()
				s.approval = &ap
				s.mu.Unlock()
				pPostMessage.Call(hwnd, wmAppEvent, 0, 0)
			}
		}
	}()

	var msg struct {
		Hwnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      point
		Unused  uint32
	}
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

func applyEvent(s *state, ev gui.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case gui.EventReady:
		if ev.Ready != nil {
			if ev.Ready.Model != "" {
				s.model = ev.Ready.Model
			}
			s.cwd = ev.Ready.Cwd
		}
	case gui.EventAgent:
		if ev.Agent == nil {
			return
		}
		a := ev.Agent
		switch a.Type {
		case gui.AgentText:
			if n := len(s.rows); n > 0 && s.rows[n-1].kind == "assistant" {
				s.rows[n-1].text += a.Text
			} else {
				s.rows = append(s.rows, row{kind: "assistant", role: "argentgo", text: a.Text})
			}
		case gui.AgentThinking:
			if n := len(s.rows); n > 0 && s.rows[n-1].kind == "thinking" {
				s.rows[n-1].text += a.Text
			} else {
				s.rows = append(s.rows, row{kind: "thinking", role: "思考", text: a.Text})
			}
		case gui.AgentToolRequested:
			s.rows = append(s.rows, row{kind: "tool", role: a.Name, text: a.Arguments})
		case gui.AgentToolFinished:
			s.rows = append(s.rows, row{kind: "notice", role: a.Name, text: firstLine(a.Output)})
		case gui.AgentNotice:
			s.rows = append(s.rows, row{kind: "notice", text: a.Text})
		case gui.AgentAborted:
			s.status = "已中止"
			s.busy = false
		case gui.AgentAttempt:
			s.rows = append(s.rows, row{kind: "notice", role: a.Attempt, text: a.Text})
		}
	case gui.EventTurnDone:
		s.busy = false
		if ev.TurnDone != nil {
			s.status = fmt.Sprintf("完成 · %d 请求 · %d tokens", ev.TurnDone.Requests, ev.TurnDone.TotalTokens)
		}
	case gui.EventError:
		s.rows = append(s.rows, row{kind: "error", text: ev.Message})
		s.busy = false
	case gui.EventSession:
		base := filepath.Base(ev.Path)
		s.sessionID = strings.TrimSuffix(base, filepath.Ext(base))
	case gui.EventSessionCleared:
		s.rows = nil
		s.sessionID = ""
	case gui.EventSessions:
		s.sessions = ev.Sessions
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var (
	emptyUTF16      = [1]uint16{0}
	lastUTF16       []uint16 // keeps the most recent conversion alive across a syscall
	nativeClassName = utf16Buffer("ArgentGoNative")
	nativeTitle     = utf16Buffer("argentgo")
	editClassBuffer = utf16Buffer("EDIT")
	sendTextBuffer  = utf16Buffer("发送")
)

// utf16Buffer makes a NUL-terminated buffer: Win32 W functions expect C strings
// even when a length is also supplied.
func utf16Buffer(s string) []uint16 {
	return append(utf16.Encode([]rune(s)), 0)
}

func utf16Ptr(s string) *uint16 {
	if s == "" {
		return &emptyUTF16[0]
	}
	lastUTF16 = utf16Buffer(s)
	return &lastUTF16[0]
}
