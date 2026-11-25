package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"example.com/argentgo/internal/proc"
)

// Client 是一条到 debug adapter 的 DAP 会话。它管理子进程、帧读写、
// seq↔响应匹配和事件分发。零跨包依赖（proc 提供环境净化）。
type Client struct {
	mu      sync.Mutex
	seq     atomic.Int64
	pending map[int64]chan response
	events  chan Event

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd

	done      chan struct{}
	closeOnce sync.Once
	readerErr error
}

// Start 启动一个 debug adapter 并建立 DAP 帧通道。
// command/args 由调用方给出（如 "dlv dap"）；dir 为空时继承 agent cwd；
// env 是额外环境变量（经 proc.SanitizeEnv 剔除宿主私有会话变量）。
func Start(ctx context.Context, command string, args []string, dir string, env []string) (*Client, error) {
	if command == "" {
		return nil, errors.New("dap: empty adapter command")
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = proc.SanitizeEnv(env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dap: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("dap: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("dap: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("dap: start %s: %w", command, err)
	}
	c := &Client{
		pending: make(map[int64]chan response),
		events:  make(chan Event, 64),
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		cmd:     cmd,
		done:    make(chan struct{}),
	}
	go c.readLoop()
	// 子进程退出时收尾，使读循环结束并唤醒所有等待者。
	go func() {
		_ = cmd.Wait()
		c.finish(io.EOF)
	}()
	return c, nil
}

// readLoop 持续读帧并按类型分流：response → 对应 pending，event → events channel。
func (c *Client) readLoop() {
	r := bufio.NewReader(c.stdout)
	defer c.finish(io.EOF)
	for {
		payload, err := readFrame(r)
		if err != nil {
			c.finish(err)
			return
		}
		var msg Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue // 畸形帧不影响后续帧
		}
		switch msg.Type {
		case TypeResponse:
			ch := c.takePending(int64(msg.RequestSeq))
			if ch == nil {
				continue
			}
			success := msg.Success != nil && *msg.Success
			ch <- response{RequestSeq: msg.RequestSeq, Success: success, Message: msg.Message, Body: msg.Body}
			close(ch)
		case TypeEvent:
			ev := Event{Seq: msg.Seq, Event: msg.Event, Body: msg.Body}
			select {
			case c.events <- ev:
			default: // 事件满时丢弃最旧的，保证不阻塞读循环
				select {
				case <-c.events:
				default:
				}
				c.events <- ev
			}
		}
	}
}

func (c *Client) finish(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.readerErr = err
		for seq, ch := range c.pending {
			ch <- response{Success: false, Message: "dap connection closed"}
			close(ch)
			delete(c.pending, seq)
		}
		c.mu.Unlock()
		close(c.done)
		close(c.events)
	})
}

func (c *Client) addPending(seq int64) chan response {
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[seq] = ch
	c.mu.Unlock()
	return ch
}

func (c *Client) takePending(seq int64) chan response {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[seq]
	if ok {
		delete(c.pending, seq)
	}
	return ch
}

// Call 发送一条 request 并等待对应 response。ctx 取消时撤销等待，但不杀进程
// （与 LSP 的 cancelRequest 范式一致，会话仍可用于后续命令）。
func (c *Client) Call(ctx context.Context, command string, args any) (json.RawMessage, error) {
	seq := c.seq.Add(1)
	ch := c.addPending(seq)
	raw, _ := json.Marshal(args)
	msg := Message{Seq: int(seq), Type: TypeRequest, Command: command}
	if raw != nil && string(raw) != "null" {
		msg.Arguments = raw
	}
	if err := writeFrame(c.stdin, mustMarshal(msg)); err != nil {
		c.takePending(seq)
		return nil, fmt.Errorf("dap: send %s: %w", command, err)
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return nil, fmt.Errorf("dap: %s failed: %s", command, resp.Message)
		}
		return resp.Body, nil
	case <-ctx.Done():
		c.takePending(seq)
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("dap: connection closed")
	}
}

// Notify 发送一条不等待响应的 request（用于 disconnect/configurationDone 等
// 某些 adapter 不回包的情况）。
func (c *Client) Notify(command string, args any) error {
	seq := c.seq.Add(1)
	raw, _ := json.Marshal(args)
	msg := Message{Seq: int(seq), Type: TypeRequest, Command: command}
	if raw != nil && string(raw) != "null" {
		msg.Arguments = raw
	}
	return writeFrame(c.stdin, mustMarshal(msg))
}

func (c *Client) Events() <-chan Event { return c.events }

// Alive 报告会话是否仍可用。
func (c *Client) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Close 关闭 stdin 使 adapter 退出，并终止整棵进程树（超时兜底）。
func (c *Client) Close() error {
	var firstErr error
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		if c.cmd.Process != nil {
			if err := terminateProcessTree(c.cmd); err != nil {
				firstErr = err
			}
		}
		c.mu.Lock()
		c.readerErr = io.EOF
		for seq, ch := range c.pending {
			ch <- response{Success: false, Message: "dap closed"}
			close(ch)
			delete(c.pending, seq)
		}
		c.mu.Unlock()
		close(c.done)
		close(c.events)
	})
	return firstErr
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Kill 会让 cmd.Wait 的 goroutine 收尾；平台进程组由 proc 的启动路径配置时
	// 由 OS 负责整树回收，这里至少保证直接进程被终止。
	return cmd.Process.Kill()
}
