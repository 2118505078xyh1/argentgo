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

// Terminal control sequences. Everything the UI does to the screen is one of
// these plus absolute cursor moves; there is no platform terminal library.
const (
	enterAltScreen = "\x1b[?1049h"
	leaveAltScreen = "\x1b[?1049l"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	clearScreen    = "\x1b[2J"
)

const frameInterval = 50 * time.Millisecond

var errNotConsole = errors.New("tui: not a terminal")

// IO replaces the terminal streams. The default is os.Stdin / os.Stdout; when
// it is set no raw-mode setup happens, so a test can drive Run with pipes.
type IO struct {
	In  io.Reader
	Out io.Writer
}

// Options configures Run.
type Options struct {
	Root     string
	Model    string
	Locale   string
	Theme    string
	NoColor  bool
	Env      func(string) string // injected lookup for NO_COLOR / TERM
	FS       FileSystem          // injected read-only workspace for the panel
	Sessions []string
	IO       *IO
	Prompt   func(ctx context.Context, text string, onText func(string)) error
	Approval func(ctx context.Context, name, args string) (bool, error)
	// Events, when set, feeds tool events into the optional side panels. A
	// closed channel simply disables further updates.
	Events <-chan Event
	// Undo, Retry and Rename are the checkpoint/session seams. nil means the
	// matching slash command explains that the host has not wired it yet.
	Undo   TurnAction
	Retry  TurnAction
	Rename TurnAction
	// OnReady, when set, receives the live model once the first frame is up.
	// It is the hook an integration uses to run the agent and to submit
	// approval requests from another goroutine via Model.Approve.
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

// size is a terminal geometry notification.
type size struct {
	w, h int
}

// frameWriter remembers the caret position written by the previous frame. The
// buffer already diffs rows; this adds the same diff for the cursor sequence,
// which otherwise went out on every 50 ms tick even while the UI was idle.
type frameWriter struct {
	lastRow, lastCol int
	haveCursor       bool
}

// draw paints the model, flushes the row diff and emits the caret only when
// something actually changed. A resize passes force so the full repaint (and
// the cursor sequence after clearScreen) still happens.
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

// Run owns the terminal until the operator quits or ctx ends. The screen is
// restored through a deferred cleanup, so an error, a signal or a panic all
// leave the terminal usable.
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
	model.SetApproval(opts.Approval)
	model.SetTurnActions(opts.Undo, opts.Retry, opts.Rename)

	restore := func() {}
	if real {
		if fn, err := enableRaw(); err == nil {
			restore = fn
		}
	}
	fmt.Fprint(out, enterAltScreen+clearScreen+hideCursor)

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			restore()
			fmt.Fprint(out, showCursor+leaveAltScreen)
			if body := model.Plain(); body != "" {
				fmt.Fprint(out, body)
			}
		})
	}
	defer cleanup()

	// A signal must restore the terminal, so it cancels the loop instead of
	// letting the process die with the alternate screen still up.
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

	handleKeys := func(keys []Key) bool {
		runAction := func(fn TurnAction, arg, done string) {
			if fn == nil {
				return
			}
			if err := fn(runCtx, arg); err != nil {
				model.SetStatus(errorText(opts.Locale, err))
				return
			}
			model.SetStatus(text(opts.Locale, done))
		}
		for _, key := range keys {
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

	buffer := NewBuffer(width, height)
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
			// HandleEvent may mutate an existing entry in place (for instance
			// flipping Failed on a tool row); the loop owns the dirty signal.
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
			// The header clock and the working spinner are derived from the
			// current time, so every tick is a potential visual change.
			model.MarkDirty()
		case <-runCtx.Done():
			quit = true
		}
		if quit {
			break
		}
		draw(force)
	}

	if cancelTurn != nil {
		cancelTurn()
	}
	return nil
}

// CursorPosition is the ANSI 1-based row and column of the input caret.
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
