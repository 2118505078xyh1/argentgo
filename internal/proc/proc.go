// Package proc 运行子进程，支持超时、对
// stdout/stderr 的有界捕获以及整棵进程树的终止。
//
// 它是 argent 核心进程辅助函数的 Go 移植：每个调用 shell 的工具
// 都经由 Run，因此超时、输出上限和进程树终止
// 都只在一个地方强制执行。
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// DefaultMaxOutput 是每个流捕获输出的默认上限。
const DefaultMaxOutput = 64 << 10

// MaxOutputBytes 限制每个捕获的流。流的头部和尾部
// 都会保留：嘈杂构建末尾的失败信息才是
// 关键部分。测试会调低它。
var MaxOutputBytes = DefaultMaxOutput

// cancellationGracePeriod 是向进程树发送 SIGTERM 后，被取消的子进程
// 在升级为 SIGKILL 之前，可自行退出的时长。
// 它故意设得很短：协作式清理（刷新缓冲区、删除
// 临时文件、释放锁）应在毫秒级完成。测试可以通过该变量
// 缩短或延长它。
var cancellationGracePeriod = 500 * time.Millisecond

const truncatedMarker = "\n... [output truncated] ...\n"

// Command 描述要运行的子进程。
type Command struct {
	// Path 是要执行的程序；绝不通过 shell 解析。
	Path string
	// Args 原样传给 Path。
	Args []string
	// Dir 是工作目录；为空则保持当前目录。
	Dir string
	// Env 条目（"K=V"）会追加到继承的环境中。
	Env []string
	// Stdin 设置后会被馈送给子进程，然后关闭。
	Stdin io.Reader
	// Timeout 到期后会终止整棵进程树。零值表示永久等待。
	Timeout time.Duration
	// IdleTimeout 是两次输出之间的最大静默时长；超过即终止整棵进程树。
	// 零值（默认）关闭看门狗，行为不变。持续输出的进程不会被误判。
	IdleTimeout time.Duration
	// KillTree 会在终止子进程的同时终止其后代。
	KillTree bool
	// KeepOnTimeout 让超时的子进程留在自己的会话中继续运行，
	// 而非终止它。只有 RunWithTimeout 尊重此选项；返回的
	// *TimeoutError.Live 是调用方必须接管的仍在运行的子进程。
	KeepOnTimeout bool
	// NoWindow 隐藏子进程原本会打开的控制台窗口。
	// 仅 Windows 有效；其他平台忽略。
	NoWindow bool
}

// Result 是已结束的子进程产生的结果。
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// Success 报告是否为干净退出。
func (r Result) Success() bool { return r.ExitCode == 0 }

// Combined 是按捕获顺序拼接的 stdout 后跟 stderr。
func (r Result) Combined() string {
	if r.Stderr == "" {
		return r.Stdout
	}
	if r.Stdout == "" {
		return r.Stderr
	}
	return r.Stdout + r.Stderr
}

// CancelReason 描述一次运行为何被提前终止。它是纯附加的
// 分类辅助：Run 仍像以前一样用 %w 包装 context 错误，
// 因此已使用 errors.Is 的调用方不受影响。ClassifyCancel
// 让只需要原因的调用方无需重新实现那条判断链。
type CancelReason uint8

const (
	// CancelNone 表示没有需要分类的错误。
	CancelNone CancelReason = iota
	// CancelTimeout 表示错误包装了 context.DeadlineExceeded。
	CancelTimeout
	// CancelUser 表示错误包装了 context.Canceled，即显式的
	// 中断，而非到期的截止时间。
	CancelUser
	// CancelSignal 表示运行被 OS 信号打断。它属于
	// 原因词汇表，供把信号取消包装进自己哨兵错误的调用方使用；
	// ClassifyCancel 自身从不返回它。
	CancelSignal
	// CancelUnknown 表示错误非 nil，但不带已知原因。
	CancelUnknown
)

// ClassifyCancel 用 errors.Is 把 err 映射到 CancelReason，因此像 Run 返回的
// 那种被包装的错误会按其原因分类。nil 错误为
// CancelNone；不匹配任何已知原因的错误为 CancelUnknown。
func ClassifyCancel(err error) CancelReason {
	if err == nil {
		return CancelNone
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return CancelTimeout
	case errors.Is(err, context.Canceled):
		return CancelUser
	default:
		return CancelUnknown
	}
}

// String 实现 fmt.Stringer，为每个原因给出简短稳定的标签。
func (r CancelReason) String() string {
	switch r {
	case CancelNone:
		return "none"
	case CancelTimeout:
		return "timeout"
	case CancelUser:
		return "user"
	case CancelSignal:
		return "signal"
	case CancelUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("CancelReason(%d)", uint8(r))
	}
}

// Run 执行 c 并返回其捕获的输出。
//
// 非零退出不是错误：退出码在 Result 中。只有在子进程
// 无法启动、无法回收，或运行被 ctx（错误包装 ctx.Err()）
// 或 Command.Timeout（错误包装 context.DeadlineExceeded）
// 提前终止时才返回错误。这两种情况仍会终止整棵树，
// 也仍会填充 Result。
func Run(ctx context.Context, c Command) (Result, error) {
	started := time.Now()
	if c.Path == "" {
		return Result{ExitCode: -1}, errors.New("proc: empty command path")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	out := &cappedBuffer{max: MaxOutputBytes}
	errOut := &cappedBuffer{max: MaxOutputBytes}
	// 空闲看门狗的活动时钟：任何输出（stdout/stderr）都会刷新它。
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	// 在字节进入有界缓冲区之前，重新组装跨子进程任意读取分块边界
	// 拆开的字符，使捕获内容按字符解码时
	// 绝不会在分块边界出现 U+FFFD。
	outDec := newUTF8CarryWriter(out)
	errDec := newUTF8CarryWriter(errOut)
	var stdoutWriter, stderrWriter io.Writer = outDec, errDec
	// ARGENT_ACP_DECODE 默认开启：在每个程序的每条输出流上独立做 UTF-8→ACP
	// 判定。只有显式设为 "0" 才关闭并跳过包装，后续读写路径逐字节不变。
	var stdoutACP, stderrACP *acpByteDecoder
	if acpDecodeEnabled() {
		stdoutACP = newACPByteDecoder(stdoutWriter)
		stderrACP = newACPByteDecoder(stderrWriter)
		stdoutWriter, stderrWriter = stdoutACP, stderrACP
	}
	if c.IdleTimeout > 0 {
		stdoutWriter = &activityWriter{inner: stdoutWriter, last: &lastActivity}
		stderrWriter = &activityWriter{inner: stderrWriter, last: &lastActivity}
	}

	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	// 总是做净化：即使没有每次调用的 env，子进程也不得继承
	// 宿主私有的会话管道变量（嵌套 agent 依赖这一点）。
	cmd.Env = SanitizeEnv(c.Env)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	// IdleTimeout 触发时必须能终止整棵进程树（否则沉默的孙进程会一直持有
	// 管道，让 cmd.Wait 卡到它自然结束）。因此 IdleTimeout 隐含启用 KillTree。
	killTree := c.KillTree || c.IdleTimeout > 0
	configure(cmd, killTree, c.NoWindow)

	// 从我们自己拥有的管道馈送 stdin：exec 自身的 stdin 拷贝
	// 会被 Wait 等待，因此永不返回的 reader 可能挂起整个调用。
	// 拷贝 goroutine 只在 Start 成功后才启动。若提前启动，
	// 子进程根本无法生成时它会阻塞在永不产生数据的 Stdin reader 上，
	// 从而同时泄漏 goroutine
	// 和管道的写端。
	var stdinRead, stdinWrite *os.File
	if c.Stdin != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return Result{ExitCode: -1}, fmt.Errorf("proc: stdin pipe: %w", err)
		}
		cmd.Stdin = pr
		stdinRead, stdinWrite = pr, pw
	}

	// startTracked 在 opt-in 开启且需要整树终止时改走挂起启动：
	// 先入作业对象再恢复，封住孙进程逃逸窗口。默认路径与 cmd.Start() 等价。
	handle, err := startTracked(cmd, killTree)
	if err != nil {
		if stdinRead != nil {
			_ = stdinRead.Close()
		}
		if stdinWrite != nil {
			_ = stdinWrite.Close()
		}
		return Result{ExitCode: -1}, fmt.Errorf("proc: spawn %q: %w", c.Path, err)
	}
	// 子进程现在有了自己的句柄；关闭我们的句柄，会在拷贝完成后
	// 把管道写端变成它眼中的 EOF。
	if stdinRead != nil {
		_ = stdinRead.Close()
	}
	if stdinWrite != nil {
		go func() {
			_, _ = io.Copy(stdinWrite, c.Stdin)
			_ = stdinWrite.Close()
		}()
	}

	defer handle.close()

	done := make(chan waitOutcome, 1)
	go func() {
		err := cmd.Wait()
		done <- waitOutcome{err: err, state: cmd.ProcessState}
	}()

	var timeout <-chan time.Time
	if c.Timeout > 0 {
		timer := time.NewTimer(c.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	// 空闲看门狗：定期用单调时钟判定“距上次输出是否过久”，命中则经一条
	// 一次性 channel 通知主 select。进程结束后由 done 分支停止 ticker。
	var idleCh <-chan struct{}
	var idleStop chan struct{}
	if c.IdleTimeout > 0 {
		idleStop = make(chan struct{})
		ch := make(chan struct{}, 1)
		idleCh = ch
		go func() {
			ticker := time.NewTicker(c.IdleTimeout / 2)
			if ticker.C == nil { // 极短超时下周期可能被钳为 0
				ticker.Stop()
				return
			}
			defer ticker.Stop()
			for {
				select {
				case <-idleStop:
					return
				case <-ticker.C:
					if idleWatchdogExpired(lastActivityTime(lastActivity.Load()), time.Now(), c.IdleTimeout) {
						select {
						case ch <- struct{}{}:
						default:
						}
						return
					}
				}
			}
		}()
		defer close(idleStop)
	}

	var (
		got          waitOutcome
		timedOut     bool
		idleTimedOut bool
		ctxErr       error
	)
	select {
	case got = <-done:
	case <-timeout:
		timedOut = true
		handle.killTree()
		got = awaitOutcome(done)
	case <-idleCh:
		idleTimedOut = true
		handle.killTree()
		got = awaitOutcome(done)
	case <-ctx.Done():
		ctxErr = ctx.Err()
		if errors.Is(ctxErr, context.Canceled) {
			// 用户取消会给子进程一个短暂的清理机会：
			// 向整棵树发送 SIGTERM，最多等待一个宽限期让它
			// 自行退出，若仍不退出则升级为 SIGKILL。
			handle.terminateTree()
			select {
			case got = <-done:
				// 直接子进程在宽限期内退出了，但忽略 SIGTERM 的孙进程可能
				// 仍存活：它随直接子进程退出而成为孤儿。按判据补杀整棵树，
				// 对已退出的直接子进程发 SIGKILL 只是空操作，却能清理孙进程。
				if shouldKillTreeAfterExit(true, false, true) {
					handle.killTree()
				}
			case <-time.After(cancellationGracePeriod):
				// 宽限耗尽而直接子进程仍未退出：按判据升级为 SIGKILL。
				if shouldKillTreeAfterExit(true, true, false) {
					handle.killTree()
				}
				got = awaitOutcome(done)
			}
		} else {
			// 截止时间（或任何其他原因）保持原有的立即
			// 终止：超时语义不得因宽限等待而变慢。
			handle.killTree()
			got = awaitOutcome(done)
		}
	}

	// 通常 cmd.Wait 到这里已经返回，exec 的拷贝 goroutine 也随之结束。
	// 但若后代仍持有管道，awaitOutcome 也可能超时，此时那些 goroutine 仍活着；
	// 解码器、进位写入器和捕获缓冲区都由互斥锁保护，
	// 因此无论哪种情况 Finish/Flush/String 都是安全的。
	// ACP 解码器先排空截断尾部，再由 utf8CarryWriter 冲刷暂存字节。
	if stdoutACP != nil {
		stdoutACP.Finish()
	}
	if stderrACP != nil {
		stderrACP.Finish()
	}
	outDec.Flush()
	errDec.Flush()

	res := Result{
		Stdout:    out.String(),
		Stderr:    errOut.String(),
		ExitCode:  -1,
		Truncated: out.wasTruncated() || errOut.wasTruncated(),
		Duration:  time.Since(started),
	}
	if got.state != nil {
		res.ExitCode = got.state.ExitCode()
	}

	switch {
	case timedOut:
		return res, fmt.Errorf("proc: %q exceeded %s: %w", c.Path, c.Timeout, context.DeadlineExceeded)
	case idleTimedOut:
		return res, fmt.Errorf("proc: %q idle for %s: %w", c.Path, c.IdleTimeout, ErrIdleTimeout)
	case ctxErr != nil:
		return res, fmt.Errorf("proc: %q: %w", c.Path, ctxErr)
	case got.err != nil && !isExitError(got.err):
		return res, fmt.Errorf("proc: wait %q: %w", c.Path, got.err)
	}
	return res, nil
}

// waitOutcome 携带 Wait 获知的一切，因此调用方绝不会从
// 另一个 goroutine 读取 cmd.ProcessState。
type waitOutcome struct {
	err   error
	state *os.ProcessState
}

// awaitOutcome 给被终止的子进程一点时间被回收。把句柄泄漏给
// 孙进程的进程树不得让调用方永远挂起。
func awaitOutcome(done <-chan waitOutcome) waitOutcome {
	select {
	case o := <-done:
		return o
	case <-time.After(5 * time.Second):
		return waitOutcome{}
	}
}

func isExitError(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit)
}

// cappedBuffer 保留流的前半部分和后半部分。
//
// 它可安全并发使用：Run 通常只在 cmd.Wait 排空 exec 的拷贝 goroutine 后读取它，
// 但当 awaitOutcome 放弃等待某个子进程时，若其后代保持了管道打开，
// 那些 goroutine 仍可能在写入。互斥锁使 String/Truncated 的读取
// 不会与这些写入竞争。
type cappedBuffer struct {
	mu      sync.Mutex
	max     int
	head    []byte
	tail    []byte
	seen    int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.seen += n
	if b.max <= 0 {
		if n > 0 {
			b.dropped = true
		}
		return n, nil
	}
	headCap := (b.max + 1) / 2
	tailCap := b.max - headCap
	if room := headCap - len(b.head); room > 0 && len(p) > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > tailCap {
			b.dropped = true
			if tailCap == 0 {
				b.tail = b.tail[:0]
			} else {
				copy(b.tail, b.tail[len(b.tail)-tailCap:])
				b.tail = b.tail[:tailCap]
			}
		}
	}
	return n, nil
}

// wasTruncated 报告是否触及上限。它是该字段的受锁保护
// 访问器，供可能在拷贝 goroutine 仍在写入时
// 读取缓冲区的调用方使用。
func (b *cappedBuffer) wasTruncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.dropped {
		// 此处 head 和 tail 是连续的；跨越其边界的字符仍是完整的，
		// 因此返回原始字节即可精确保留流内容。
		return string(b.head) + string(b.tail)
	}
	// 标记插在 head 和 tail 之间，因此恰好跨越字节上限的字符
	// 会被切成两半并解码为 U+FFFD。应改为丢弃每个边界处的
	// 不完整片段：最多牺牲一个字符，
	// 但其余每个字符都保持格式正确。
	return string(trimIncompleteRuneTail(b.head)) + truncatedMarker +
		string(trimIncompleteRuneHead(b.tail))
}

// trimIncompleteRuneTail 丢弃不属于完整 UTF-8 字符的尾部
// 字节。utf8.DecodeLastRune 对孤立无效字节和切片末尾被截断的
// 序列都报告 (RuneError, 1)，因此逐字节裁剪
// 会收敛到最后一个完整字符的边界。
func trimIncompleteRuneTail(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size > 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// trimIncompleteRuneHead 是 trimIncompleteRuneTail 的镜像：它跳过
// 无法作为完整字符起始的前导字节，这正是尾部切片在
// 任意字节偏移处被切开后的样子。
func trimIncompleteRuneHead(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r != utf8.RuneError || size > 1 {
			return b
		}
		b = b[1:]
	}
	return b
}

// Which 根据 PATH 解析程序名。
func Which(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("proc: empty program name")
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("proc: %q not found on PATH", name)
	}
	if abs, err := filepath.Abs(found); err == nil {
		found = abs
	}
	return found, nil
}

var (
	shellOnce sync.Once
	shellPath string
	shellArgs []string
)

// Shell 构造一个通过 POSIX shell 运行 command 的 Command。
//
// 优先使用 bash（Windows 上是 Git Bash，工具都期望它），且
// 只有它真正能运行才使用：WSL 的应用执行别名留在 PATH 上的 bash.exe
// 在未安装发行版时会报错退出，因此候选者必须通过探测。
// 若没有可用者，Windows 上使用 cmd.exe，
// 其他平台使用 /bin/sh。
func Shell(command string) Command {
	path, args := locateShell()
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, args...)
	argv = append(argv, command)
	return Command{
		Path:     path,
		Args:     argv,
		NoWindow: runtime.GOOS == "windows",
	}
}

func locateShell() (string, []string) {
	shellOnce.Do(func() {
		if path, ok := firstWorkingShell(shellCandidates()); ok {
			shellPath, shellArgs = path, shellArgsFor(path)
			return
		}
		shellPath, shellArgs = fallbackShell()
	})
	return shellPath, shellArgs
}

// firstWorkingShell 返回第一个证明自己能运行的候选者。
func firstWorkingShell(candidates []string) (string, bool) {
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if probeShell(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func probeShell(path string) bool {
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Command{
		Path:     path,
		Args:     []string{"-c", "exit 0"},
		Timeout:  10 * time.Second,
		NoWindow: true,
	})
	return err == nil && res.ExitCode == 0
}

func shellArgsFor(path string) []string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "bash"):
		if runtime.GOOS == "windows" {
			// -l 会加载 profile，从而获得 Git Bash 期望的 PATH。
			return []string{"-lc"}
		}
		return []string{"-c"}
	case base == "cmd.exe" || base == "cmd":
		return []string{"/C"}
	default:
		return []string{"-c"}
	}
}
