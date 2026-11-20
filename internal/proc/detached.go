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

// ErrTimeout 标识一个超过截止时间的命令。调用方用
// errors.Is 匹配它，并在设置 KeepOnTimeout 时从
// TimeoutError.Live 取回仍在运行的子进程。
var ErrTimeout = errors.New("proc: timeout")

// TimeoutError 报告命令超过了其超时时间。
//
// 只有调用方设置了 KeepOnTimeout 时 Live 才非 nil：此时
// 子进程留在自己的会话中继续运行，由调用方接管（通常
// 作为后台作业）。没有该选项时整棵树已被终止。
type TimeoutError struct {
	Command string
	Timeout time.Duration
	Live    *Process
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("proc: %q exceeded %s", e.Command, e.Timeout)
}

// Is 保留历史上 errors.Is(err, context.DeadlineExceeded) 的检查，
// 同时为调用方提供专用的哨兵错误来匹配。
func (e *TimeoutError) Is(target error) bool {
	return target == ErrTimeout || target == context.DeadlineExceeded
}

// LiveChild 是已启动子进程面向 OS 的行为。测试通过
// SetStarter 注入假的进程，无需启动真正的 sleep 即可演练超时。
type LiveChild interface {
	Pid() int
	// Kill 终止子进程及其后代。
	Kill()
	// Wait 阻塞直到子进程退出并返回其退出码。
	Wait() int
}

// gracefulTerminator 是可选的 OS 侧能力：请求整棵树停止
// 而不直接杀死它，使协作式子进程可以刷新并清理。
// OS 支撑的子进程实现了它；未实现它的测试假进程保持原有的
// 立即终止行为，无需改动。
type gracefulTerminator interface {
	TerminateTree()
}

// Starter 启动一个 Command 并交回其存活的子进程。
type Starter func(Command) (*Process, error)

var (
	starterMu sync.RWMutex
	starter   Starter = startOS
)

// SetStarter 替换进程启动器。传入 nil 恢复 OS 启动器。
// 在测试设置阶段调用，不要在子进程运行期间调用。
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

// Process 是已启动的子进程，其生命周期可以交给另一个所有者
// （后台作业）。它的所有方法都可安全并发使用。
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

// NewProcess 把存活的子进程包装成 Process。测试启动器用它构造
// 假进程；模拟的子进程输出通过 Write 喂入。
func NewProcess(child LiveChild) *Process {
	return newProcess(child, newSwitchWriter())
}

// newProcess 是 OS 启动器已经持有 exec 拷贝子进程 stdout/stderr
// 所用的 writer 时使用的内部构造函数。Process 必须
// 通过 Output/SetOutput 暴露那同一个 writer；在这里另分配一个
// 会悄悄丢弃子进程打印的一切。
func newProcess(child LiveChild, out *switchWriter) *Process {
	return &Process{
		child:   child,
		out:     out,
		started: time.Now(),
		done:    make(chan struct{}),
	}
}

// Write 记录子进程输出。生产环境中由 exec 的拷贝
// goroutine 喂入；测试假进程直接调用它。
func (p *Process) Write(b []byte) (int, error) { return p.out.Write(b) }

// Pid 是子进程的进程 id。
func (p *Process) Pid() int { return p.child.Pid() }

// StartedAt 是子进程启动的时刻。
func (p *Process) StartedAt() time.Time { return p.started }

// Command 是被启动的程序。
func (p *Process) Command() string { return p.command }

// Output 是到目前为止捕获的全部内容。子进程仍在
// 产生输出时调用它也是安全的。
func (p *Process) Output() string { return p.out.Captured() }

// Truncated 报告捕获是否触及输出上限。
func (p *Process) Truncated() bool { return p.out.Truncated() }

// Done 在子进程被回收后关闭。
func (p *Process) Done() <-chan struct{} {
	p.once.Do(p.startWait)
	return p.done
}

// ExitCode 在 Done 关闭后有效。
func (p *Process) ExitCode() int {
	<-p.Done()
	return p.code
}

// Wait 阻塞直到子进程退出并返回其退出码。
func (p *Process) Wait() int { return p.ExitCode() }

// Kill 终止子进程及其整棵进程树。
func (p *Process) Kill() { p.child.Kill() }

// SetOutput 把目前捕获的一切刷新到 w，并把之后的子进程输出
// 重定向到那里。超时命令的部分输出正是这样变成作业日志的，
// 既不会丢失字节，也不会重新运行命令。
func (p *Process) SetOutput(w io.Writer) { p.out.Handoff(w) }

func (p *Process) startWait() {
	go func() {
		p.code = p.child.Wait()
		// cmd.Wait 已排空 exec 的拷贝 goroutine：流现已处于 EOF，
		// 因此尾部不完整的序列不会再有后续分块来
		// 补全，应原样保留在捕获中。
		p.out.flush()
		if p.closed != nil {
			p.closed()
		}
		close(p.done)
	}()
}

// switchWriter 在允许一次目标替换的同时串行化写入，用于
// 把正在运行的子进程提升为后台作业的场景。
//
// 所有字节在到达目标前都经过 utf8CarryWriter。
// exec 按与 UTF-8 字符边界无关的分块读取子进程的 stdout/stderr：
// 一个中文字符可能被拆分到两次读取中。Run 已重新组装这些字符；
// 分离的子进程也必须如此，否则在它仍运行时取的部分结果
// （RunWithTimeout 的提升路径）会以悬空序列结尾，解码为 U+FFFD。
// 待定序列不会出现在 Captured() 中，
// 只在交接时或 EOF 时才发出。
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
	// 这里不调用 Flush：不完整的字符必须留在 dec.carry 中，
	// 使读取仍在运行子进程的调用方永远看不到悬空序列。
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
		// 交接即捕获的 EOF：在复制已捕获的前缀之前，先原样发出任何悬空字节，
		// 这样一个字节也不会丢失。
		w.dec.Flush()
		_, _ = io.WriteString(dst, w.buf.String())
	}
	w.dec.setWriter(dst)
	w.dst = dst
}

// flush 在已知子进程已退出后发出悬空序列。
func (w *switchWriter) flush() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dec.Flush()
}

// StartDetached 在自己的进程组/会话中启动 c，使子进程能在
// 启动它的 shell 退出后继续存活，之后可按进程树终止。
func StartDetached(c Command) (*Process, error) {
	return currentStarter()(c)
}

// RunWithTimeout 运行 c，用 c.Timeout 限制等待（零值表示永久等待）。
//
// 超时时返回 *TimeoutError。当设置 c.KeepOnTimeout 时，子进程
// 不会被终止：它作为第二个返回值，在自己的会话中被交回，
// 调用方必须接管它（存为作业，然后用 SetOutput 指向其日志）。
// 没有 KeepOnTimeout 时整棵树被终止，与 Run 在超时
// 或 context 取消时一贯的做法完全相同。
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
		// c.Timeout 是软性的"提升为后台"截止时间；ctx 是
		// 调用方的硬性界限。两者同时触发时 select 会随机
		// 选一个。若定时器胜出而 context 实际上已经
		// 结束，KeepOnTimeout 就会交回一个活过调用方截止时间的子进程
		// （例如 runPromotableBash 的硬超时），把有界的调用
		// 变成失控的作业。只要定时器触发时 context 已经结束，
		// 就优先选择 context。
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
		// 用户取消获得与 Run 对其子进程相同的两阶段停止：
		// 向整棵树发信号，让协作式子进程自行退出，
		// 然后再升级。已到期的截止时间仍会立即终止，
		// 因为超时语义不得因宽限等待而变慢。
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

// cancelChildGracefully 请求子进程树终止，等待最多共享的
// 宽限期以等待协作式退出，然后升级为 Kill。无法
// 向进程树发信号的假进程会被立即终止。
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

// awaitOutcome 给被终止的子进程一点时间被回收。把句柄泄漏给
// 孙进程的进程树不得让调用方永远挂起。
func awaitDone(p *Process) {
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
	}
}

// startOS 是 OS 支撑的 Starter，除非测试安装了假实现，否则默认使用它。
func startOS(c Command) (*Process, error) {
	if c.Path == "" {
		return nil, errors.New("proc: empty command path")
	}
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	// 分离的子进程通常是运行嵌套 agent 的、被提升的后台作业。
	// 它需要与 Run 相同的宿主私有 env 过滤，否则会
	// 把宿主的会话管道误认为自己的。
	cmd.Env = SanitizeEnv(c.Env)
	// 像 Run 一样自己拥有 stdin 管道。把非 *os.File reader
	// 直接交给 exec 会让 Cmd.Wait 等待 exec 的 stdin 拷贝 goroutine：
	// 比子进程活得更久的 reader（慢速生产者或永不结束的流）
	// 会使 Done 永远无法关闭，因此被提升的作业永远
	// 无法被回收，其日志文件也永远无法释放。传入 *os.File 让 exec
	// 不进入拷贝循环；我们自己的拷贝器不是 Wait 等待的对象。
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
	// 子进程现在有了自己的句柄；关闭我们的句柄，会在拷贝完成后
	// 把写端变成它眼中的 EOF。
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
	// 使用 exec 正在拷贝进去的那个 writer，使 Output/SetOutput 看到
	// 子进程的真实字节，而不是另一个永远为空的缓冲区。
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

// TerminateTree 实现 gracefulTerminator：与 Run 相同的两阶段停止中
// 优雅的那一半。在 Windows 上，后端的 terminateTree 是
// 立即终止，因此宽限等待在那里退化为无操作。
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
