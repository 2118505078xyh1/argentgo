package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrTimeout identifies a command that outlived its deadline. Callers match it
// with errors.Is and, when KeepOnTimeout was set, recover the live child from
// TimeoutError.Live.
var ErrTimeout = errors.New("proc: timeout")

// TimeoutError reports that a command exceeded its timeout.
//
// Live is non-nil only when the caller set KeepOnTimeout: in that case the
// child was left running in its own session and the caller owns it (typically
// as a background job). Without that option the whole tree was killed.
type TimeoutError struct {
	Command string
	Timeout time.Duration
	Live    *Process
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("proc: %q exceeded %s", e.Command, e.Timeout)
}

// Is preserves the historical errors.Is(err, context.DeadlineExceeded) check
// while giving callers a dedicated sentinel to match.
func (e *TimeoutError) Is(target error) bool {
	return target == ErrTimeout || target == context.DeadlineExceeded
}

// LiveChild is the OS-facing behaviour of a started child. Tests inject a fake
// through SetStarter to exercise timeouts without launching real sleepers.
type LiveChild interface {
	Pid() int
	// Kill terminates the child and its descendants.
	Kill()
	// Wait blocks until the child exits and returns its exit code.
	Wait() int
}

// gracefulTerminator is the optional OS-side capability of asking a whole tree
// to stop without killing it, so a cooperative child can flush and clean up.
// The OS-backed child implements it; test fakes that do not keep the original
// immediate-kill behaviour and need no changes.
type gracefulTerminator interface {
	TerminateTree()
}

// Starter starts a Command and hands back its live child.
type Starter func(Command) (*Process, error)

var (
	starterMu sync.RWMutex
	starter   Starter = startOS
)

// SetStarter replaces the process starter. Passing nil restores the OS starter.
// Call it during test setup, not while children are running.
func SetStarter(s Starter) {
	starterMu.Lock()
	defer starterMu.Unlock()
	if s == nil {
		s = startOS
	}
	starter = s
}

func currentStarter() Starter {
	starterMu.RLock()
	defer starterMu.RUnlock()
	return starter
}

// Process is a started child whose lifetime can be handed to another owner (a
// background job). All its methods are safe for concurrent use.
type Process struct {
	child   LiveChild
	out     *switchWriter
	closed  func()
	started time.Time
	command string

	once sync.Once
	done chan struct{}
	code int
}

// NewProcess wraps a live child in a Process. Test starters use it to build a
// fake; simulated child output is fed through Write.
func NewProcess(child LiveChild) *Process {
	return newProcess(child, newSwitchWriter())
}

// newProcess is the internal constructor used when the OS starter already has
// the writer that exec copies the child's stdout/stderr into. The Process must
// expose that exact writer through Output/SetOutput; allocating a second one
// here would silently discard everything the child prints.
func newProcess(child LiveChild, out *switchWriter) *Process {
	return &Process{
		child:   child,
		out:     out,
		started: time.Now(),
		done:    make(chan struct{}),
	}
}

// Write records child output. Production processes are fed by exec's copy
// goroutines; test fakes call it directly.
func (p *Process) Write(b []byte) (int, error) { return p.out.Write(b) }

// Pid is the child's process id.
func (p *Process) Pid() int { return p.child.Pid() }

// StartedAt is when the child was started.
func (p *Process) StartedAt() time.Time { return p.started }

// Command is the program that was started.
func (p *Process) Command() string { return p.command }

// Output is everything captured so far. It is safe to call while the child is
// still producing output.
func (p *Process) Output() string { return p.out.Captured() }

// Truncated reports whether the capture hit the output cap.
func (p *Process) Truncated() bool { return p.out.Truncated() }

// Done is closed once the child has been reaped.
func (p *Process) Done() <-chan struct{} {
	p.once.Do(p.startWait)
	return p.done
}

// ExitCode is valid after Done is closed.
func (p *Process) ExitCode() int {
	<-p.Done()
	return p.code
}

// Wait blocks until the child exits and returns its exit code.
func (p *Process) Wait() int { return p.ExitCode() }

// Kill terminates the child and its whole process tree.
func (p *Process) Kill() { p.child.Kill() }

// SetOutput flushes everything captured so far into w and redirects future
// child output there. It is how a timed-out command's partial output becomes
// the job log, without losing bytes and without running the command again.
func (p *Process) SetOutput(w io.Writer) { p.out.Handoff(w) }

func (p *Process) startWait() {
	go func() {
		p.code = p.child.Wait()
		// cmd.Wait has drained exec's copy goroutines: the stream is at EOF
		// now, so a trailing incomplete sequence has no future chunk to
		// complete it and belongs in the capture verbatim.
		p.out.flush()
		if p.closed != nil {
			p.closed()
		}
		close(p.done)
	}()
}

// switchWriter serialises writes while allowing one destination swap, used
// when a running child is promoted to a background job.
//
// All bytes pass through a utf8CarryWriter before reaching the destination.
// exec reads a child's stdout/stderr in chunks whose boundaries are unrelated
// to UTF-8 rune boundaries: a zh-CN rune can be split across two reads. Run
// already reassembles those runes; a detached child must too, or a partial
// result taken while it is still running (RunWithTimeout's promotion path)
// ends in a dangling sequence and decodes as U+FFFD. The pending sequence is
// held back from Captured() and only emitted at handoff or at EOF.
type switchWriter struct {
	mu  sync.Mutex
	dst io.Writer
	buf *cappedBuffer
	dec *utf8CarryWriter // writes complete runes to dst (buf until handoff)
}

func newSwitchWriter() *switchWriter {
	buf := &cappedBuffer{max: MaxOutputBytes}
	return &switchWriter{dst: buf, buf: buf, dec: newUTF8CarryWriter(buf)}
}

func (w *switchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dec.Write(p)
}

func (w *switchWriter) Captured() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	// No Flush here: an incomplete rune must stay in dec.carry so callers
	// reading a still-running child never see a dangling sequence.
	return w.buf.String()
}

func (w *switchWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.wasTruncated()
}

func (w *switchWriter) Handoff(dst io.Writer) {
	if dst == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dst == w.buf {
		// Handoff is the EOF of the capture: emit any dangling bytes verbatim
		// before copying the captured prefix, so not one byte is lost.
		w.dec.Flush()
		_, _ = io.WriteString(dst, w.buf.String())
	}
	w.dec.setWriter(dst)
	w.dst = dst
}

// flush emits a dangling sequence once the child is known to have exited.
func (w *switchWriter) flush() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dec.Flush()
}

// StartDetached starts c in its own process group/session so the child survives
// the shell that launched it and can be killed as a tree later.
func StartDetached(c Command) (*Process, error) {
	return currentStarter()(c)
}

// RunWithTimeout runs c, bounding the wait by c.Timeout (zero waits forever).
//
// On timeout it returns a *TimeoutError. When c.KeepOnTimeout is set the child
// is NOT killed: it is returned as the second result, in its own session, and
// the caller must adopt it (store it as a job, then SetOutput to its log).
// Without KeepOnTimeout the whole tree is killed, exactly as Run has always
// done on a timeout or a cancelled context.
func RunWithTimeout(ctx context.Context, c Command) (Result, *Process, error) {
	started := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := StartDetached(c)
	if err != nil {
		return Result{ExitCode: -1}, nil, err
	}

	var timeoutCh <-chan time.Time
	if c.Timeout > 0 {
		timer := time.NewTimer(c.Timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	var (
		code     int
		timedOut bool
		ctxErr   error
	)
	select {
	case <-p.Done():
		code = p.ExitCode()
	case <-timeoutCh:
		// c.Timeout is the soft "promote to background" deadline; ctx is the
		// caller's hard bound. When both fire at the same instant select picks
		// one at random. If the timeout wins while the context is already
		// done, KeepOnTimeout would hand back a live child that outlives the
		// caller's deadline (e.g. runPromotableBash's hard timeout), turning a
		// bounded call into a runaway job. Prefer the context whenever it is
		// done by the time the timer fires.
		if err := ctx.Err(); err != nil {
			ctxErr = err
		} else {
			timedOut = true
		}
	case <-ctx.Done():
		ctxErr = ctx.Err()
	}

	res := Result{
		Stdout:    p.Output(),
		Truncated: p.Truncated(),
		ExitCode:  -1,
		Duration:  time.Since(started),
	}
	if !timedOut && ctxErr == nil {
		res.ExitCode = code
	}

	if timedOut {
		te := &TimeoutError{Command: c.Path, Timeout: c.Timeout}
		if c.KeepOnTimeout {
			te.Live = p
			return res, p, te
		}
		p.Kill()
		awaitDone(p)
		return res, nil, te
	}
	if ctxErr != nil {
		// A user cancellation gets the same two-phase stop Run gives its
		// children: signal the whole tree and let a cooperative child exit on
		// its own before escalating. An elapsed deadline still kills at once,
		// because timeout semantics must not be slowed by the grace wait.
		if errors.Is(ctxErr, context.Canceled) {
			cancelChildGracefully(p)
		} else {
			p.Kill()
		}
		awaitDone(p)
		return res, nil, fmt.Errorf("proc: %q: %w", c.Path, ctxErr)
	}
	return res, nil, nil
}

// cancelChildGracefully asks the child's tree to terminate, waits up to the
// shared grace period for a cooperative exit, then escalates to Kill. Fakes
// that cannot signal a tree are killed straight away.
func cancelChildGracefully(p *Process) {
	terminator, ok := p.child.(gracefulTerminator)
	if !ok {
		p.Kill()
		return
	}
	terminator.TerminateTree()
	select {
	case <-p.Done():
		return
	case <-time.After(cancellationGracePeriod):
	}
	p.Kill()
}

// awaitDone gives a killed child a moment to be reaped. A tree that leaks a
// handle into a grandchild must not hang the caller forever.
func awaitDone(p *Process) {
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
	}
}

// startOS is the OS-backed Starter used unless a test installs a fake.
func startOS(c Command) (*Process, error) {
	if c.Path == "" {
		return nil, errors.New("proc: empty command path")
	}
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	// A detached child is often a promoted background job that runs a nested
	// agent. It needs the same host-private env filter Run applies, or it
	// mistakes the host's session plumbing for its own.
	cmd.Env = SanitizeEnv(c.Env)
	// Own the stdin pipe the same way Run does. Handing a non-*os.File reader
	// straight to exec makes Cmd.Wait wait for exec's stdin copy goroutine: a
	// reader that outlives the child (a slow producer or a never-ending stream)
	// then keeps Done from closing forever, so a promoted job can never be
	// reaped and its log file is never released. Passing an *os.File keeps exec
	// out of the copy loop; our own copier is not something Wait waits for.
	var stdinRead, stdinWrite *os.File
	if c.Stdin != nil {
		if f, ok := c.Stdin.(*os.File); ok {
			cmd.Stdin = f
		} else {
			pr, pw, err := os.Pipe()
			if err != nil {
				return nil, fmt.Errorf("proc: stdin pipe: %w", err)
			}
			cmd.Stdin = pr
			stdinRead, stdinWrite = pr, pw
		}
	}
	configureDetached(cmd, c.NoWindow)
	out := newSwitchWriter()
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		if stdinRead != nil {
			_ = stdinRead.Close()
		}
		if stdinWrite != nil {
			_ = stdinWrite.Close()
		}
		return nil, fmt.Errorf("proc: spawn %q: %w", c.Path, err)
	}
	// The child has its own handle now; closing ours turns the write end into
	// EOF for it once the copier finishes.
	if stdinRead != nil {
		_ = stdinRead.Close()
	}
	if stdinWrite != nil {
		go func() {
			_, _ = io.Copy(stdinWrite, c.Stdin)
			_ = stdinWrite.Close()
		}()
	}
	killer := newPlatformProcess(cmd.Process, true)
	// Use the very writer exec is copying into, so Output/SetOutput see the
	// child's real bytes instead of a separate, always-empty buffer.
	p := newProcess(osLiveChild{cmd: cmd, killer: killer}, out)
	p.command = c.Path
	p.closed = killer.close
	return p, nil
}

type osLiveChild struct {
	cmd    *exec.Cmd
	killer *platformProcess
}

func (c osLiveChild) Pid() int { return c.cmd.Process.Pid }

func (c osLiveChild) Kill() { c.killer.killTree() }

// TerminateTree implements gracefulTerminator: the graceful half of the same
// two-phase stop Run uses. On Windows the backend's terminateTree is an
// immediate kill, so the grace wait collapses to a no-op there.
func (c osLiveChild) TerminateTree() { c.killer.terminateTree() }

func (c osLiveChild) Wait() int {
	err := c.cmd.Wait()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
