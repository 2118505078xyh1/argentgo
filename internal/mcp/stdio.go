package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// maxLineBytes 限制一帧换行分隔的 JSON-RPC 消息。
const maxLineBytes = 8 * 1024 * 1024

// execSpec 把 stdio 服务器的命令解析为可执行文件名和
// 交给 exec.Command 的参数。
//
// 在 Windows 上，PowerShell 脚本（.ps1）无法直接启动：
// CreateProcess 会拒绝它们（"%1 is not a valid Win32 application"），
// 而 Go 的 LookPath 永远找不到它们，因为 .PS1 不在 PATHEXT 中。像 npx 这样的
// Node 工具有时只安装为 npx.ps1（没有 .cmd 包装），而配置为本地 .ps1
// 脚本的服务器也会撞上同样的墙。改为通过 PowerShell 运行它们。
// 普通命令和常见的 npx.cmd 包装保持不变，因为 Go 已经能安全执行它们。
// 保持不变，因为 Go 已经能安全执行它们。
func execSpec(spec Spec) (string, []string) {
	return execSpecForOS(runtime.GOOS, spec.Command, spec.Args)
}

// execSpecForOS 是 execSpec 的可测试核心。goos 作为参数，
// 使 Windows 包装规则可在任何宿主上演练。
func execSpecForOS(goos, command string, args []string) (string, []string) {
	if goos != "windows" {
		return command, args
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return command, args
	}
	// 显式的 .ps1 路径永远不是有效的 Win32 可执行文件。
	if strings.EqualFold(filepath.Ext(command), ".ps1") {
		return powershellScript(command, args)
	}
	if resolved, err := exec.LookPath(command); err == nil {
		// 当用户把 .PS1 加入 PATHEXT 时，LookPath 可能返回 .ps1。
		if strings.EqualFold(filepath.Ext(resolved), ".ps1") {
			return powershellScript(resolved, args)
		}
		// 普通可执行文件或标准的 npx.cmd 包装无需包装器。
		return command, args
	}
	// Go 找不到的裸名称：只安装了 .ps1 的情况（如 npx.ps1）。
	if filepath.Base(command) == command {
		if script := findPS1OnPath(command); script != "" {
			return powershellScript(script, args)
		}
	}
	return command, args
}

// powershellScript 构建一个运行 .ps1 文件的调用。绕过执行策略
// 是因为该脚本是显式配置的服务器命令；
// -NoProfile 防止用户的配置文件改变子进程的环境。
func powershellScript(script string, args []string) (string, []string) {
	wrapped := make([]string, 0, len(args)+5)
	wrapped = append(wrapped, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
	wrapped = append(wrapped, args...)
	return "powershell.exe", wrapped
}

// findPS1OnPath 返回 PATH 上 <name>.ps1 的绝对路径，找不到则返回 ""。
// Go 的 LookPath 忽略 .ps1，因此这填补了对 Windows 基于脚本的
// MCP 服务器而言唯一重要的缺口。
func findPS1OnPath(name string) string {
	if filepath.Ext(name) != "" {
		return ""
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name+".ps1")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// stdioTransport 通过子进程的管道传输换行分隔的 JSON-RPC。
type stdioTransport struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	ids     idSequence
	waiters *waiters
	diag    *ringBuffer
	// 当 stderr 排空 goroutine 拷贝完子进程写出的所有内容时，drainDone 关闭。
	// 错误丰富化会（短暂地）等待它，使子进程退出前打印的启动诊断
	// 不会因竞态而丢失。
	drainDone chan struct{}
	done      chan struct{}

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error

	mu      sync.Mutex
	doneErr error
}

func newStdioTransport(spec Spec) (*stdioTransport, error) {
	name, args := execSpec(spec)
	cmd := exec.Command(name, args...)
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stdin pipe: %w", spec.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stdout pipe: %w", spec.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stderr pipe: %w", spec.Name, err)
	}

	t := &stdioTransport{
		name:      spec.Name,
		cmd:       cmd,
		stdin:     stdin,
		waiters:   newWaiters(),
		diag:      &ringBuffer{limit: stderrRingBytes},
		drainDone: make(chan struct{}),
		done:      make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %q: start %q: %w", spec.Name, spec.Command, err)
	}
	go t.readLoop(stdout)
	go t.drain(stderr)
	return t, nil
}

// 当某帧超过上限时，readBoundedLine 返回 errLineTooLong。
var errLineTooLong = errors.New("mcp: frame too long")

// readBoundedLine 从 reader 读取一帧换行分隔的消息，
// 最多累积 limit 字节。bufio.Reader.ReadBytes 会不断增大结果，
// 直到遇到换行符（或 EOF），因此发出无换行巨行的服务器会无界分配，
// 使 maxLineBytes 失效。ReadSlice 只返回读取器固定缓冲区中已有的字节，
// 并在还有更多时报告 bufio.ErrBufferFull，
// 这使我们能在读取过程中强制执行上限。
func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > limit {
			return line, errLineTooLong
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// readLoop 把 stdout 行转换为相关联的响应。
func (t *stdioTransport) readLoop(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readBoundedLine(reader, maxLineBytes)
		if errors.Is(err, errLineTooLong) {
			t.finish(fmt.Errorf("mcp %q: server sent a frame larger than %d bytes", t.name, maxLineBytes))
			return
		}
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				// 容忍无效行：服务器可能在 stdout 上打印日志。
				_ = t.waiters.handle(trimmed)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.finish(fmt.Errorf("mcp %q: server closed the connection", t.name))
			} else {
				t.finish(fmt.Errorf("mcp %q: read from server failed: %w", t.name, err))
			}
			return
		}
	}
}

// drain 把子进程的 stderr 保存在有界缓冲区中用于诊断。
func (t *stdioTransport) drain(stderr io.Reader) {
	defer close(t.drainDone)
	_, _ = io.Copy(t.diag, stderr)
}

// stderrWithDrain 返回捕获的 stderr 尾部，
// 先短暂等待异步排空 goroutine 结束。恰好在打印启动错误后
// 退出的服务器，会在 stderr 排空保证运行之前就关闭 stdout（唤醒 readLoop）；
// 有界的等待使诊断具有确定性，同时绝不拖慢调用方的错误路径。
// 测试中手工构造的传输没有排空通道，
// 只返回当前已捕获的
// 内容。
func (t *stdioTransport) stderrWithDrain() string {
	if t == nil {
		return ""
	}
	if t.drainDone != nil {
		select {
		case <-t.drainDone:
		case <-time.After(stderrDrainWait):
		}
	}
	if t.diag == nil {
		return ""
	}
	return t.diag.String()
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.ids.take()
	ch := t.waiters.add(id)
	defer t.waiters.drop(id)

	frame, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	if err := t.writeContext(ctx, method, frame); err != nil {
		return nil, err
	}

	select {
	case reply := <-ch:
		return reply.result, reply.err
	case <-ctx.Done():
		// 取消一次调用会杀死子进程：stdio 服务器没有其他中断方式，
		// 任其运行会泄漏进程。
		t.finish(fmt.Errorf("mcp %q: %s cancelled: %w", t.name, method, ctx.Err()))
		return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return nil, t.closedError(method)
	}
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mcp %q: %s: %w", t.name, method, err)
	}
	frame, err := marshalNotification(method, params)
	if err != nil {
		return fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	return t.writeContext(ctx, method, frame)
}

// writeContext 写入一帧，同时不让停止读取 stdin 的子进程把调用方
// 拖过其截止时间。管道写入一旦子进程缓冲区填满就可能永远阻塞，
// 因此它在自己的 goroutine 中运行，由 context（或已关闭的传输）
// 赢得竞争。finish 关闭 stdin，使已在途的写入解除阻塞，
// 因此 goroutine 不会泄漏。
func (t *stdioTransport) writeContext(ctx context.Context, method string, frame []byte) error {
	written := make(chan error, 1)
	go func() { written <- t.write(frame) }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		t.finish(fmt.Errorf("mcp %q: %s cancelled: %w", t.name, method, ctx.Err()))
		return fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return t.closedError(method)
	}
}

func (t *stdioTransport) write(frame []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	select {
	case <-t.done:
		return t.closedError("write")
	default:
	}
	frame = append(frame, '\n')
	if _, err := t.stdin.Write(frame); err != nil {
		return fmt.Errorf("mcp %q: write to server failed: %w", t.name, err)
	}
	return nil
}

// reap 只等待子进程一次并记录结果。它不能在 readLoop 或 drain 上运行：
// cmd.Wait 会关闭那些 goroutine 读取的管道，在那里调用它
// 可能与自己死锁。
func (t *stdioTransport) reap() {
	t.waitOnce.Do(func() { t.waitErr = t.cmd.Wait() })
}

// finish 记录连接结束的原因并唤醒所有等待者。
func (t *stdioTransport) finish(err error) {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.doneErr = err
		t.mu.Unlock()
		close(t.done)
		t.waiters.failAll(err)
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
			// 杀死还不够：没有 Wait，进程就得不到回收
			// （Unix 上是僵尸，Windows 上是泄漏的句柄）。在后台回收它，
			// 使调用超时绝不会留下子进程。
			go t.reap()
		}
		_ = t.stdin.Close()
	})
}

func (t *stdioTransport) closedError(method string) error {
	t.mu.Lock()
	err := t.doneErr
	t.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("mcp %q: connection closed", t.name)
	}
	if method == "" {
		return err
	}
	return fmt.Errorf("%w (during %s)", err, method)
}

// Close 杀死子进程并回收它。
func (t *stdioTransport) Close() error {
	t.finish(fmt.Errorf("mcp %q: client closed", t.name))
	if t.cmd.Process != nil {
		t.reap()
	}
	var exitErr *exec.ExitError
	if t.waitErr != nil && !errors.As(t.waitErr, &exitErr) {
		return t.waitErr
	}
	return nil
}
