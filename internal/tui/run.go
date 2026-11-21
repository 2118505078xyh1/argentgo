package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// 终端控制序列。UI 对屏幕所做的一切都是这些序列
// 加上绝对光标移动；没有平台终端库。
const (
	enterAltScreen = "\x1b[?1049h"
	leaveAltScreen = "\x1b[?1049l"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	clearScreen    = "\x1b[2J"
	// 括号粘贴（DECSET/DECRST 2004）：终端把粘贴块包装在
	// ESC[200~ ... ESC[201~ 中，使换行和命令字节作为文本到达
	// 而不是被解码为 Enter / Ctrl 键并提前提交。
	enablePaste  = "\x1b[?2004h"
	disablePaste = "\x1b[?2004l"
	// terminalTitle 是 OSC 0（同时改窗口与图标标题）的 BEL 终止序列。
	// Windows Terminal 直接接受它；不需要先查询原标题——查询需要读取
	// 终端响应，在管道驱动的测试中会阻塞。进入和退出都设为同一个
	// ASCII 品牌名，保持行为确定且帧缓冲区完全不受影响。
	terminalTitle = "\x1b]0;argentgo\x07"
)

const frameInterval = 50 * time.Millisecond

var errNotConsole = errors.New("tui: not a terminal")

// IO 替换终端流。默认是 os.Stdin / os.Stdout；当
// 设置它时不进行原始模式设置，使测试可以用管道驱动 Run。
type IO struct {
	In  io.Reader
	Out io.Writer
}

// Options 配置 Run。
type Options struct {
	Root     string
	Model    string
	Locale   string
	Theme    string
	NoColor  bool
	Env      func(string) string // injected lookup for NO_COLOR / TERM
	FS       FileSystem          // injected read-only workspace for the panel
	Sessions []string
	// ContextWindow 和 Cost 是可选的头部元数据。零/空值使
	// 头部与从未设置它们的构建逐字节一致。
	ContextWindow int
	Cost          string
	IO            *IO
	Prompt        func(ctx context.Context, text string, onText func(string)) error
	Approval      func(ctx context.Context, name, args string) (bool, error)
	// CommandInfo 是宿主提供的斜杠命令元数据，供输入行补全与第 5 面板
	// 使用。nil/空值保持经典行为：没有候选、Tab 仍插入两个空格。
	CommandInfo []CommandInfo
	// PromptHistory 是可选的持久化输入历史（opt-in）。nil 表示关闭：
	// 编辑器不读写任何文件，首帧与历史导航逐字节不变。非 nil 时
	// Run 在 New 之后把它装到编辑器上，启动时恢复最近的输入。
	PromptHistory *PromptStore
	// Events 在设置时把工具事件喂给可选的侧边栏。
	// 已关闭的通道只是禁用后续更新。
	Events <-chan Event
	// Undo、Retry 和 Rename 是检查点/会话接缝。nil 表示
	// 相应的斜杠命令解释宿主尚未接线。
	Undo   TurnAction
	Retry  TurnAction
	Rename TurnAction
	// OnReady 在设置时于第一帧显示后接收活动模型。
	// 它是集成方用来运行 agent 的钩子，并通过 Model.Approve
	// 从另一个 goroutine 提交审批请求。
	OnReady func(*Model)
}

func (o *Options) streams() (io.Reader, io.Writer) {
	if o.IO != nil {
		in, out := o.IO.In, o.IO.Out
		if in == nil {
			in = os.Stdin
		}
		if out == nil {
			out = os.Stdout
		}
		return in, out
	}
	return os.Stdin, os.Stdout
}

// size 是终端几何通知。
type size struct {
	w, h int
}

// frameWriter 记住上一帧写入的光标位置。缓冲区已经按行
// 做差异比较；这为光标序列增加了同样的差异比较，
// 否则即使 UI 空闲，它也会在每个 50 ms tick 发出。
type frameWriter struct {
	lastRow, lastCol int
	haveCursor       bool
}

// draw 绘制模型、刷新行差异，并仅在确实发生变化时发出光标。
// 尺寸变化传入 force，使完整的重绘（以及
// clearScreen 之后的光标序列）仍然发生。
func (fw *frameWriter) draw(m *Model, buffer *Buffer, out io.Writer, force bool) error {
	if force || m.Dirty() {
		m.Paint(buffer)
	}
	written, err := buffer.Flush(out)
	if err != nil {
		return err
	}
	row, col := m.CursorPosition(buffer.Width(), buffer.Height())
	if written == 0 && fw.haveCursor && row == fw.lastRow && col == fw.lastCol {
		return nil
	}
	if _, err := fmt.Fprint(out, showCursor+cursorTo(row, col)); err != nil {
		return err
	}
	fw.lastRow, fw.lastCol, fw.haveCursor = row, col, true
	return nil
}

// Run 拥有终端，直到操作员退出或 ctx 结束。屏幕通过
// 延迟清理恢复，因此错误、信号或 panic 都会
// 让终端保持可用。
func Run(ctx context.Context, opts Options) error {
	if opts.Theme != "" {
		if _, err := paletteFor(opts.Theme); err != nil {
			return err
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()

	in, out := opts.streams()
	real := opts.IO == nil

	env := opts.Env
	if env == nil {
		env = os.Getenv
	}

	model := New(Config{
		Root:     opts.Root,
		Model:    opts.Model,
		Locale:   opts.Locale,
		Theme:    opts.Theme,
		NoColor:  opts.NoColor,
		Env:      env,
		FS:       opts.FS,
		Sessions: opts.Sessions,
	})
	model.SetPromptStore(opts.PromptHistory)
	model.SetApproval(opts.Approval)
	model.SetTurnActions(opts.Undo, opts.Retry, opts.Rename)
	model.SetContextInfo(opts.ContextWindow, opts.Cost)
	model.SetCommandInfo(opts.CommandInfo)

	restore := func() {}
	if real {
		if fn, err := enableRaw(); err == nil {
			restore = fn
		}
	}
	// 启用 SGR 鼠标报告（1006 字符坐标，而非 1016 像素坐标）+ 拖拽跟踪，
	// 使终端发送按下、拖拽和滚轮事件（在 keys.go 中解码）用于文本选择。括号粘贴
	// 防止多行输入被当作按键读取（以及
	// 在第一个回车时提交）。
	fmt.Fprint(out, terminalTitle+enterAltScreen+clearScreen+hideCursor+"\x1b[?1002h\x1b[?1006h"+enablePaste)

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			restore()
			fmt.Fprint(out, "\x1b[?1002l\x1b[?1006l"+disablePaste+showCursor+leaveAltScreen+terminalTitle)
			if body := model.Plain(); body != "" {
				fmt.Fprint(out, body)
			}
		})
	}
	defer cleanup()

	// 信号必须恢复终端，因此它取消循环，而不是
	// 让进程在备用屏幕仍然打开时死亡。
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			stopRun()
		case <-runCtx.Done():
		}
	}()

	width, height := terminalSize()
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	resizes, stopWatch := watchResize()
	defer stopWatch()

	chunks := make(chan []byte, 32)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 512)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case chunks <- chunk:
				case <-runCtx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	streamCh := make(chan string, 256)
	turnDone := make(chan error, 1)
	events := opts.Events // nil disables this select case
	var cancelTurn context.CancelFunc

	startTurn := func(prompt string) {
		if opts.Prompt == nil {
			turnDone <- errors.New("no prompt function configured")
			return
		}
		if cancelTurn != nil {
			cancelTurn()
		}
		turnCtx, cancel := context.WithCancel(runCtx)
		cancelTurn = cancel
		go func() {
			err := opts.Prompt(turnCtx, prompt, func(delta string) {
				select {
				case streamCh <- delta:
				case <-turnCtx.Done():
				}
			})
			turnDone <- err
		}()
	}

	buffer := NewBuffer(width, height)

	// ctrl+g 把终端交给外部编辑器。在其运行期间离开备用
	// 屏幕和原始模式（使 vim/nano 行为正常），然后
	// 重新进入并从头重绘：离开备用屏幕会丢弃
	// 行差异所依据的像素。
	forceRedraw := false
	prevEditorScreen := externalEditorScreen
	externalEditorScreen = func(fn func()) {
		fmt.Fprint(out, disablePaste+"\x1b[?1002l\x1b[?1006l"+showCursor+leaveAltScreen)
		if real {
			restore()
		}
		fn()
		if real {
			if again, err := enableRaw(); err == nil {
				restore = again
			}
		}
		fmt.Fprint(out, terminalTitle+enterAltScreen+clearScreen+hideCursor+"\x1b[?1002h\x1b[?1006h"+enablePaste)
		buffer.Invalidate()
		forceRedraw = true
	}
	defer func() { externalEditorScreen = prevEditorScreen }()

	handleKeys := func(keys []Key) bool {
		runAction := func(fn TurnAction, arg, done string) {
			if fn == nil {
				return
			}
			if err := fn(runCtx, arg); err != nil {
				// 动作失败是真正的错误：用 SeverityError 发布，使状态行
				// 以红色 failure 样式显示，并在存活期间压过信息类通知。
				model.SetStatusSeverity(SeverityError, errorText(opts.Locale, err))
				return
			}
			model.SetStatus(text(opts.Locale, done))
		}
		for _, key := range keys {
			if key.Mouse != nil {
				ev := key.Mouse
				col, row := ev.Col-1, ev.Row-1 // SGR is 1-based; Model uses 0-based
				switch ev.Kind {
				case MousePress:
					model.MouseDown(col, row)
				case MouseDrag:
					model.MouseDrag(col, row)
				case MouseRelease:
					model.MouseUp(col, row)
					if text := model.SelectedText(buffer); text != "" {
						_ = CopyToClipboard(text)
						model.ClearSelection()
					}
				case MouseWheelUp, MouseWheelDown:
					model.MouseWheel(ev.Kind == MouseWheelUp)
				}
				continue // wheels keep legacy ScrollUp/Down Kinds
			}
			switch model.HandleKey(key) {
			case ActionSubmit:
				startTurn(model.TakeSubmitted())
			case ActionAbort:
				if cancelTurn != nil {
					cancelTurn()
				}
			case ActionUndo:
				runAction(opts.Undo, model.TakeActionArg(), "undo_done")
			case ActionRetry:
				runAction(opts.Retry, model.TakeActionArg(), "retry_done")
			case ActionRename:
				runAction(opts.Rename, model.TakeActionArg(), "rename_done")
			case ActionQuit:
				return true
			}
		}
		return false
	}

	buffer.SetNoColor(model.NoColor())
	decoder := &Decoder{}
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	var frames frameWriter
	draw := func(force bool) {
		buffer.SetNoColor(model.NoColor())
		_ = frames.draw(model, buffer, out, force)
	}

	draw(true)
	if opts.OnReady != nil {
		opts.OnReady(model)
	}

	for {
		quit := false
		force := false
		select {
		case chunk := <-chunks:
			quit = handleKeys(decoder.Feed(chunk))
		case <-readerDone:
			quit = true
		case delta := <-streamCh:
			model.AppendStream(delta)
			for drained := false; !drained; {
				select {
				case more := <-streamCh:
					model.AppendStream(more)
				default:
					drained = true
				}
			}
		case err := <-turnDone:
			model.EndTurn(err)
		case event, open := <-events:
			if !open {
				events = nil
				break
			}
			model.HandleEvent(event)
			for drained := false; !drained; {
				select {
				case more, stillOpen := <-events:
					if !stillOpen {
						events = nil
						drained = true
						continue
					}
					model.HandleEvent(more)
				default:
					drained = true
				}
			}
			// HandleEvent 可能就地基改某个条目（例如
			// 翻转某个工具行的 Failed）；循环拥有脏信号。
			model.MarkDirty()
		case next := <-resizes:
			buffer.Resize(next.w, next.h)
			buffer.Invalidate()
			fmt.Fprint(out, clearScreen)
			force = true
		case <-ticker.C:
			if keys := decoder.FlushEscape(); len(keys) > 0 {
				quit = handleKeys(keys)
			}
			model.Tick()
			model.PruneNotices(time.Now())
			// 头部时钟和工作中的 spinner 源自
			// 当前时间，因此每个 tick 都可能是视觉变化。
			model.MarkDirty()
		case <-runCtx.Done():
			quit = true
		}
		if quit {
			break
		}
		if forceRedraw {
			force = true
			forceRedraw = false
		}
		draw(force)
	}

	if cancelTurn != nil {
		cancelTurn()
	}
	return nil
}

// CursorPosition 是输入光标的 ANSI 基于 1 的行列。
func (m *Model) CursorPosition(width, height int) (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.layoutLocked(width, height)
	row := l.inputTop + l.cursorRow + 2
	col := 5 + l.cursorCol
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	if col > width {
		col = width
	}
	if row > height {
		row = height
	}
	return row, col
}
