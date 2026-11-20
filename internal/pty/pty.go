// Package pty 运行一个附加到伪终端的子进程。
//
// 一个检查 isatty 然后表现为交互式的程序 —— 开发服务器、REPL、
// 监视模式的测试运行器 —— 需要终端；在普通管道后面它以为
// 自己在写文件并停止着色、提示或刷新。
// Package pty 运行一个附加到伪终端的子进程。一个检查 isatty 然后表现为交互式的程序 —— 开发服务器、REPL、监视模式的测试运行器 —— 需要终端；在普通管道后面它以为自己在写文件并停止着色、提示或刷新。Windows 使用 ConPTY（CreatePseudoConsole），unix 打开 /dev/ptmx，两者都通过原始系统调用使包保持无 cgo。
// 原始系统调用使包保持无 cgo。
package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrUnsupported 在此平台无法创建终端时返回。
var ErrUnsupported = errors.New("pty: pseudoterminals are not supported on this platform")

// Size 是以字符单元格为单位的终端大小。
type Size struct {
	Cols uint16
	Rows uint16
}

// DefaultSize 是调用方传入零 Size 时使用的大小。
var DefaultSize = Size{Cols: 120, Rows: 40}

// Command 是要附加到终端的子进程。
type Command struct {
	// Path 是要执行的程序；绝不通过 shell 解析。
	Path string
	// Args 原样传给 Path。
	Args []string
	// Dir 是工作目录；空表示保持当前目录。
	Dir string
	// Running 报告作业是否仍在运行。Terminal 报告作业是否已结束。Elapsed 是作业多久前启动的。Command 描述要启动的进程。Program 是可执行文件；不通过 shell 解析。Args 原样传递。Dir 是工作目录；空表示保持 registry 当前目录。Env 条目（"K=V"）追加到继承的环境。
	Env []string
}

// terminal 是会话的平台部分。
type terminal interface {
	io.ReadWriteCloser
	// Resize 改变报告给子进程的窗口大小。
	Resize(Size) error
	// Wait 阻塞直到子进程退出并返回其退出码。
	Wait() (int, error)
	// Exited 报告子进程是否已退出。
	Exited() bool
	// ExitCode 是退出码，子进程运行时为 -1。
	ExitCode() int
	// terminal 是会话的平台部分。Resize 改变报告给子进程的窗口大小。Wait 阻塞直到子进程退出并返回其退出码。Exited 报告子进程是否已退出。ExitCode 是退出码，子进程运行时为 -1。PID 是子进程的进程 id。
	PID() int
}

// Pty 是一个附加到伪终端的运行进程。
type Pty struct {
	term terminal
	// sizeMu 保护 size：Resize 可能在 UI/窗口事件上运行，而另一个
	// goroutine 为渲染读取 Size，且两个并发 Resize 不得
	// 让 size 与实际应用到终端的大小不一致。
	sizeMu sync.RWMutex
	size   Size

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}

	waitOnce sync.Once
	waitCode int
	waitErr  error
}

// Supported 报告此构建能否创建伪终端。
func Supported() bool { return supported() }

// Start 在给定大小的新伪终端内启动 cmd。
//
// 取消 ctx 会杀死子进程并关闭终端。
func Start(ctx context.Context, cmd Command, size Size) (*Pty, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd.Path == "" {
		return nil, errors.New("pty: empty command path")
	}
	if !supported() {
		return nil, ErrUnsupported
	}
	if size.Cols < 2 || size.Rows < 2 {
		size = DefaultSize
	}
	term, err := startPlatform(cmd, size)
	if err != nil {
		return nil, err
	}
	p := &Pty{term: term, size: size, closed: make(chan struct{})}
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				_ = p.Close()
			case <-p.closed:
			}
		}()
	}
	return p, nil
}

// Read 读取终端接下来产生的任何内容。
//
// 子进程退出且流被排空后它返回 io.EOF。在 Windows 上
// 控制台保持其输出管道端打开直到 Wait 回收子进程
// 并释放控制台（或直到 Close），因此那里 EOF 跟随 Wait/Close。
func (p *Pty) Read(b []byte) (int, error) { return p.term.Read(b) }

// Write 向终端发送输入。
func (p *Pty) Write(b []byte) (int, error) { return p.term.Write(b) }

func (p *Pty) Resize(size Size) error {
	if size.Cols < 2 || size.Rows < 2 {
		return fmt.Errorf("pty: refusing a %dx%d size", size.Cols, size.Rows)
	}
	// 整个平台调用都持有写锁，使并发 Resize
	// 串行化：报告的大小始终与最后应用到终端的大小一致，
	// 且 Size() 永不会观察到半更新的值。
	p.sizeMu.Lock()
	defer p.sizeMu.Unlock()
	if err := p.term.Resize(size); err != nil {
		return err
	}
	p.size = size
	return nil
}

// Size 是最后请求的终端大小。
func (p *Pty) Size() Size {
	p.sizeMu.RLock()
	defer p.sizeMu.RUnlock()
	return p.size
}

// Close 终止子进程并释放终端。它是幂等的。
func (p *Pty) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.closeErr = p.term.Close()
	})
	return p.closeErr
}

// Wait 阻塞直到子进程退出并返回其退出码。
func (p *Pty) Wait() (int, error) {
	p.waitOnce.Do(func() {
		p.waitCode, p.waitErr = p.term.Wait()
	})
	return p.waitCode, p.waitErr
}

// Exited 报告子进程是否已退出。
func (p *Pty) Exited() bool { return p.term.Exited() }

// ExitCode 是子进程的退出码，运行时为 -1。
func (p *Pty) ExitCode() int { return p.term.ExitCode() }

// PID 是子进程的进程 id。
func (p *Pty) PID() int { return p.term.PID() }
