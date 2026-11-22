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
	"strings"
	"testing"
	"time"
)

// TestMain 同时充当假 MCP 服务器的入口：当辅助环境变量被设置时，
// 本进程提供 stdio JSON-RPC 服务，而不是
// 运行测试套件。
func TestMain(m *testing.M) {
	if mode := os.Getenv("ARGENT_MCP_HELPER"); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperSpec(mode string, timeout time.Duration) Spec {
	return Spec{
		Name:      "fake",
		Transport: "stdio",
		Command:   os.Args[0],
		Env:       []string{"ARGENT_MCP_HELPER=" + mode},
		Timeout:   timeout,
	}
}

// helperMain 是一个基于 stdin/stdout 的最小 MCP 服务器。
func helperMain(mode string) {
	fmt.Fprintln(os.Stderr, "fake mcp server ready")
	if mode == "failfast" {
		// 一台向 stderr 打印启动诊断后退出的服务器，
		// 且不回答 initialize —— 即审计中的裸 EOF 场景。
		fmt.Fprintln(os.Stderr, "BOOM: startup failed cause: port 3000 in use")
		return
	}
	if mode == "silent" {
		// 一台永不回答的服务器；客户端必须超时并杀死它。
		time.Sleep(30 * time.Second)
		return
	}

	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		idRaw, hasID := message["id"]
		if !hasID {
			continue
		}
		var id any
		if err := json.Unmarshal(idRaw, &id); err != nil {
			continue
		}

		response := map[string]any{"jsonrpc": "2.0", "id": id}
		result, err := helperReply(method, message["params"])
		if err != nil {
			response["error"] = map[string]any{"code": -32602, "message": err.Error()}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}

func helperReply(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "fake", "version": "0.1"},
			"capabilities":    map[string]any{},
		}, nil
	case "tools/list":
		return map[string]any{"tools": []any{
			map[string]any{
				"name":        "echo",
				"description": "echoes text",
				"inputSchema": map[string]any{"type": "object"},
			},
			map[string]any{
				"name":        "boom",
				"description": "always fails",
				"inputSchema": map[string]any{"type": "object"},
			},
		}}, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, err
		}
		switch call.Name {
		case "echo":
			text, _ := call.Arguments["text"].(string)
			return map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "echo: " + text},
			}}, nil
		case "cwd":
			// 报告假服务器子进程的工作目录，供工作目录接线测试断言。
			dir, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			return map[string]any{"content": []any{
				map[string]any{"type": "text", "text": dir},
			}}, nil
		case "boom":
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "kaboom"}},
				"isError": true,
			}, nil
		}
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
	return nil, fmt.Errorf("method %s not found", method)
}

func TestStdioRoundTrip(t *testing.T) {
	c, err := Connect(context.Background(), helperSpec("echo", 5*time.Second))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Name != "fake" || info.ProtocolVersion != "2024-11-05" {
		t.Fatalf("unexpected server info: %+v", info)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "boom" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].Server != "fake" {
		t.Fatalf("tool not tagged with the server: %+v", tools[0])
	}

	text, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || text != "echo: hi" {
		t.Fatalf("call echo: text=%q err=%v", text, err)
	}

	text, err = c.CallTool(context.Background(), "boom", nil)
	if err == nil || text != "kaboom" || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("isError not surfaced: text=%q err=%v", text, err)
	}

	resources, err := c.ListResources(context.Background())
	if err != nil || len(resources) != 0 {
		t.Fatalf("list resources: %+v %v", resources, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(Stderr(c), "fake mcp server ready") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stderr was not captured: %q", Stderr(c))
}

func TestStdioCancellationKillsTheChild(t *testing.T) {
	c, err := Connect(context.Background(), helperSpec("silent", 5*time.Second))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := c.ListTools(ctx); err == nil {
		t.Fatal("expected the call to time out")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not surfaced: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}

	// 取消一次 stdio 调用会杀死子进程，因此连接不可用。
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected the connection to be closed after cancellation")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close after cancellation: %v", err)
	}
}

func TestStdioFinishReapsKilledChild(t *testing.T) {
	transport, err := newStdioTransport(helperSpec("silent", 5*time.Second))
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	if transport.cmd.Process == nil {
		t.Fatal("no child process was started")
	}

	// 模拟调用超时/流失败：finish 必须既杀死又回收
	// 子进程，而不只是杀死它。
	transport.finish(errors.New("finish under test"))

	// reap 是幂等的；在我们自己的 goroutine 上调用它，使测试无论
	// 哪个调用方赢得与 finish goroutine 的 sync.Once 竞争都能观察到完成。
	reaped := make(chan struct{})
	go func() { transport.reap(); close(reaped) }()
	select {
	case <-reaped:
	case <-time.After(3 * time.Second):
		t.Fatal("finish killed the child but never waited for it")
	}
	if transport.cmd.ProcessState == nil {
		t.Fatal("child process was not reaped (ProcessState is nil)")
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("close after reap: %v", err)
	}
}

// blockedStdinTransport 构建一个 stdin 没有 reader 的传输，因此任何
// 写入都会阻塞直到 finish 关闭它。cmd.Process 为 nil，因此 finish 不需要
// 真正的子进程。
func blockedStdinTransport() (*stdioTransport, io.ReadCloser) {
	reader, writer := io.Pipe()
	return &stdioTransport{
		name:    "blocked",
		cmd:     &exec.Cmd{},
		stdin:   writer,
		waiters: newWaiters(),
		diag:    &ringBuffer{},
		done:    make(chan struct{}),
	}, reader
}

// TestNotifyHonorsDeadlineOnBlockedWrite 守护 notifications/initialized：
// 当子进程停止读取 stdin 时，通知不得永远挂起。
// 请求路径已经用 context 限制其写入；通知现在也
// 这样做，使停滞的子进程无法挫败调用方的超时。
func TestNotifyHonorsDeadlineOnBlockedWrite(t *testing.T) {
	transport, reader := blockedStdinTransport()
	defer reader.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- transport.notify(ctx, "notifications/initialized", nil) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("notify error = %v, want context deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify ignored its deadline while the stdin write was blocked")
	}
}

// TestCallHonorsDeadlineOnBlockedWrite 固定共享写入路径：在提取出
// 感知截止时间的写入辅助函数之后，当子进程停止读取 stdin 时，
// 请求侧仍必须超时（并关闭传输）。
func TestCallHonorsDeadlineOnBlockedWrite(t *testing.T) {
	transport, reader := blockedStdinTransport()
	defer reader.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, err := transport.call(ctx, "initialize", map[string]any{})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call error = %v, want context deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call ignored its deadline while the stdin write was blocked")
	}
	select {
	case <-transport.done:
	default:
		t.Fatal("a write timeout must close the transport")
	}
}

func TestStdioMissingCommand(t *testing.T) {
	_, err := Connect(context.Background(), Spec{
		Name:      "missing",
		Transport: "stdio",
		Command:   "definitely-not-a-real-command-xyz",
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name the server: %v", err)
	}
}
