package bash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/proc"
	"github.com/2118505078xyh1/argentgo/internal/pty"
)

// 本文件提供统一 exec 会话的进程侧实现。jobs 注册表只依赖注入的
// SessionHandle/SessionRunner 接口，这里分别给出管道（非 TTY）与
// 伪终端（TTY）两种实现，因此 TTY 开关可以在每个会话上独立选择。

// execRunner 按 shellInvocation 解析出的程序与参数启动会话进程。
type execRunner struct {
	path string
	args []string
	dir  string
	tty  bool
}

// Start 实现 jobs.SessionRunner。
func (r execRunner) Start(output io.Writer) (jobs.SessionHandle, error) {
	if r.tty {
		return startPTYHandle(r, output)
	}
	return startPipeHandle(r, output)
}

// ---- 非 TTY：保留 stdin 写端的普通管道会话 ----

type pipeSessionHandle struct {
	stdin   io.WriteCloser
	process *os.Process
	done    chan struct{}
	code    int
}

func (h *pipeSessionHandle) WriteStdin(p []byte) (int, error) { return h.stdin.Write(p) }
func (h *pipeSessionHandle) Done() <-chan struct{}            { return h.done }
func (h *pipeSessionHandle) PID() int {
	if h.process == nil {
		return 0
	}
	return h.process.Pid
}

// Kill 终止整棵进程树并关闭 stdin 写端；重复调用安全。
func (h *pipeSessionHandle) Kill() {
	proc.KillGroup(h.process)
	_ = h.stdin.Close()
}

func startPipeHandle(r execRunner, output io.Writer) (jobs.SessionHandle, error) {
	cmd := exec.Command(r.path, r.args...)
	cmd.Dir = r.dir
	// 与 proc.Command{KillTree:true} 共用底层的进程组配置，使 Kill 能带上后代。
	proc.ConfigureGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("bash: exec session stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("bash: exec session stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("bash: exec session stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("bash: exec session spawn %q: %w", r.path, err)
	}

	h := &pipeSessionHandle{stdin: stdin, process: cmd.Process, done: make(chan struct{})}
	// 两条流各自汇入环形缓冲；exec 的拷贝 goroutine 在 cmd.Wait 时被排空，
	// 因此 done 关闭后不会再有写入。
	go func() { _, _ = io.Copy(output, stdout) }()
	go func() { _, _ = io.Copy(output, stderr) }()
	go func() {
		err := cmd.Wait()
		h.code = exitCodeFromWait(err)
		// 进程退出后关闭写端，使下一轮 WriteStdin 得到明确的错误而非静默丢弃。
		_ = stdin.Close()
		close(h.done)
	}()
	return h, nil
}

// ---- TTY：ConPTY / ptmx 会话 ----

type ptySessionHandle struct {
	pty  *pty.Pty
	done chan struct{}
	code int
}

func (h *ptySessionHandle) WriteStdin(p []byte) (int, error) { return h.pty.Write(p) }
func (h *ptySessionHandle) Done() <-chan struct{}            { return h.done }
func (h *ptySessionHandle) PID() int                         { return h.pty.PID() }

// Kill 关闭伪终端；pty.Close 幂等且会终止子进程。
func (h *ptySessionHandle) Kill() { _ = h.pty.Close() }

func startPTYHandle(r execRunner, output io.Writer) (jobs.SessionHandle, error) {
	if !pty.Supported() {
		return nil, pty.ErrUnsupported
	}
	p, err := pty.Start(context.Background(), pty.Command{
		Path: r.path,
		Args: r.args,
		Dir:  r.dir,
	}, pty.Size{})
	if err != nil {
		return nil, fmt.Errorf("bash: exec session pty: %w", err)
	}
	h := &ptySessionHandle{pty: p, done: make(chan struct{})}
	go func() { _, _ = io.Copy(output, p) }()
	go func() {
		h.code, _ = p.Wait()
		close(h.done)
	}()
	return h, nil
}

// exitCodeFromWait 把 cmd.Wait 的错误还原为退出码；被信号终止记为 -1。
func exitCodeFromWait(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// sessionYield 把工具参数转换为 yield 时长。非正值表示让注册表使用默认值，
// 避免把显式不合法的输入变成极短的 busy-wait。
func sessionYield(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// unifiedExecEnabled 报告是否启用统一 exec 会话。
// 默认关闭：只有 ARGENT_EXEC_SESSION=1 才接管新增的会话字段，
// 关闭时 bash 工具的 schema、参数解析与执行路径逐字节不变。
func unifiedExecEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_EXEC_SESSION")) == "1"
}

// unifiedExecRegistry 返回进程内唯一的会话注册表，惰性构造避免
// opt-in 关闭时无谓地启动回收器 goroutine。
var (
	unifiedOnce     sync.Once
	unifiedRegistry *jobs.SessionRegistry
)

func unifiedExecRegistry() *jobs.SessionRegistry {
	unifiedOnce.Do(func() {
		unifiedRegistry = jobs.NewSessionRegistry()
	})
	return unifiedRegistry
}

// isSessionInterrupt 识别一次 stdin 写入是否表示"中断当前会话"。
// TTY 下 Ctrl-C 的 0x03 字节在部分平台（尤其 ConPTY）不会被翻译成信号，
// 因此显式把它映射为只终止目标会话的 Kill，避免影响其它会话或宿主。
func isSessionInterrupt(input string) bool {
	return input == "\x03" || input == "\r\x03" || input == "\n\x03"
}
