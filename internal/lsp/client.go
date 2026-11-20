package lsp

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
	"strconv"
	"strings"
	"sync"
	"time"
)

// 客户端错误。
var (
	// ErrClosed 报告服务端已退出或被关闭的客户端。
	ErrClosed = errors.New("lsp: client is closed")
	// ErrNoCommand 报告没有可执行程序的服务器规格。
	ErrNoCommand = errors.New("lsp: server has no command")
	// ErrTimeout 报告在发布之前过期的诊断等待。
	ErrTimeout = errors.New("lsp: timed out waiting for diagnostics")
)

const (
	initializeTimeout = 60 * time.Second
	shutdownTimeout   = 2 * time.Second
	readerGrace       = 3 * time.Second
	stderrTailLines   = 20
)

// Server 描述如何启动一个语言服务器。
type Server struct {
	// Name 在消息中标识服务器，例如 "gopls"。
	Name string
	// Command 是程序，通过 PATH 解析。
	Command string
	// Args 传给程序。
	Args []string
	// Env 保存叠加在当前环境之上的额外 KEY=VALUE 对。
	Env []string
	// LanguageIDs 是该服务器拥有的 LSP 语言 id。
	LanguageIDs []string
	// RootURI 覆盖从根路径推导的工作区 URI。
	RootURI string
}

// Client 是一个运行中的语言服务器及其已知文档。
type Client struct {
	server  Server
	root    string
	rootURI string

	stdin   io.WriteCloser
	buf     *bufio.Reader
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan response
	err     error
	doneCh  chan struct{}

	readDone   chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	kill       func() error
	wait       func() error
	waitErr    error
	ledger     *tailBuffer

	diagMu     sync.Mutex
	seq        uint64
	docs       map[string]*documentState
	published  map[string]publication
	waiters    map[uint64]diagWaiter
	nextWaiter uint64
}

type response struct {
	result json.RawMessage
	err    error
}

type documentState struct {
	version    int
	open       bool
	languageID string
	// mark 是最后一次 didOpen/didChange 时的发布序号，用于把新诊断与调用方已见过的区分开。
	// 用于把新诊断与调用方已见过的区分开。
	mark uint64
}

type publication struct {
	seq   uint64
	items []Diagnostic
}

type diagWaiter struct {
	uri string
	ch  chan struct{}
}

// inbound 是一条解码后的 JSON-RPC 消息，无论其种类。
type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("json-rpc error %d", e.Code)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// Start 为 root 启动服务器，完成 initialize 握手，并返回就绪的客户端。context 只限制握手；客户端一直运行到 Close。
// 返回就绪的客户端。context 只限制握手；客户端
// 一直运行到 Close。
func Start(ctx context.Context, server Server, root string) (*Client, error) {
	if strings.TrimSpace(server.Command) == "" {
		return nil, fmt.Errorf("%w (%s)", ErrNoCommand, server.Name)
	}
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("lsp %s: resolve root %s: %w", server.Name, root, err)
	}

	command := exec.Command(server.Command, server.Args...)
	command.Dir = absoluteRoot
	if len(server.Env) > 0 {
		command.Env = append(os.Environ(), server.Env...)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp %s: stdin pipe: %w", server.Name, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp %s: stdout pipe: %w", server.Name, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp %s: stderr pipe: %w", server.Name, err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("lsp %s: start %q: %w (is it installed and on PATH?)", server.Name, server.Command, err)
	}

	client := newClient(server, absoluteRoot, stdin, stdout)
	client.kill = command.Process.Kill
	client.wait = command.Wait
	go client.drainStderr(stderr)

	if err := client.initialize(ctx); err != nil {
		client.Close()
		if tail := client.stderrText(); tail != "" {
			return nil, fmt.Errorf("%w\nserver stderr:\n%s", err, tail)
		}
		return nil, err
	}
	return client, nil
}

// newClient 把客户端接到已连接的传输上。测试用
// newClient 把客户端接到已连接的传输上。测试用 io.Pipe 对；Start 用子进程。
func newClient(server Server, root string, stdin io.WriteCloser, stdout io.Reader) *Client {
	client := &Client{
		server:    server,
		root:      root,
		stdin:     stdin,
		buf:       bufio.NewReaderSize(stdout, 64<<10),
		pending:   make(map[uint64]chan response),
		doneCh:    make(chan struct{}),
		readDone:  make(chan struct{}),
		ledger:    &tailBuffer{limit: stderrTailLines},
		docs:      make(map[string]*documentState),
		published: make(map[string]publication),
		waiters:   make(map[uint64]diagWaiter),
	}
	go client.readLoop()
	return client
}

// Root 返回客户端启动时所在的绝对工作区根。
func (c *Client) Root() string { return c.root }

// ServerName 返回规格中的名称。
func (c *Client) ServerName() string { return c.server.Name }

// initialize 执行 LSP 握手。
func (c *Client) initialize(ctx context.Context) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, initializeTimeout)
		defer cancel()
	}
	uri := c.server.RootURI
	if uri == "" {
		uri = PathToURI(c.root)
	}
	c.rootURI = uri

	params := map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": "argentgo", "version": "0.7.0"},
		"rootUri":    uri,
		"rootPath":   c.root,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition":         map[string]any{"linkSupport": true},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"publishDiagnostics": map[string]any{"relatedInformation": true},
				"synchronization":    map[string]any{"didSave": true},
				// 声明 features_ide.go 和 features_advanced.go 中实现的 IDE 级功能，使服务器确实发送结果。
				// features_advanced.go 中实现的 IDE 级功能，使服务器确实发送结果。
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"references":     map[string]any{},
				"codeAction":     map[string]any{},
				"rename":         map[string]any{"prepareSupport": false},
				"formatting":     map[string]any{},
				"codeLens":       map[string]any{},
				"inlayHint":      map[string]any{},
				"semanticTokens": map[string]any{
					"requests": map[string]any{"full": map[string]any{"delta": true}},
					"tokenTypes": []string{
						"namespace", "type", "class", "enum", "interface", "struct",
						"typeParameter", "parameter", "variable", "property", "enumMember",
						"event", "function", "method", "macro", "keyword",
					},
					"tokenModifiers": []string{
						"declaration", "definition", "readonly", "static", "deprecated",
						"abstract", "async", "modification", "documentation", "defaultLibrary",
					},
				},
			},
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true},
		},
		"workspaceFolders": []map[string]any{{
			"uri":  uri,
			"name": filepath.Base(c.root),
		}},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify(ctx, "initialized", map[string]any{})
}

// call 发送请求并等待匹配的响应。
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	replies := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, fmt.Errorf("lsp %s: %s: %w", c.server.Name, method, err)
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("lsp %s: encode %s: %w", c.server.Name, method, err)
	}
	if err := c.write(payload); err != nil {
		return nil, err
	}

	select {
	case reply := <-replies:
		if reply.err != nil {
			return nil, fmt.Errorf("lsp %s: %s: %w", c.server.Name, method, reply.err)
		}
		return reply.result, nil
	case <-ctx.Done():
		c.cancelRequest(id)
		return nil, fmt.Errorf("lsp %s: %s: %w", c.server.Name, method, ctx.Err())
	case <-c.doneCh:
		return nil, c.closedError(method)
	}
}

// notify 发送通知，它没有响应。
func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lsp %s: %s: %w", c.server.Name, method, err)
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return fmt.Errorf("lsp %s: encode %s: %w", c.server.Name, method, err)
	}
	return c.write(payload)
}

// cancelRequest 要求服务器放弃被废弃的请求。
func (c *Client) cancelRequest(id uint64) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/cancelRequest",
		"params":  map[string]any{"id": id},
	})
	if err == nil {
		_ = c.write(payload)
	}
}

func (c *Client) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.doneCh:
		return c.closedError("write")
	default:
	}
	if err := writeFrame(c.stdin, payload); err != nil {
		return fmt.Errorf("lsp %s: %w", c.server.Name, err)
	}
	return nil
}

// readLoop 把分帧消息转换为响应、通知和应答。
func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		payload, err := readFrame(c.buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.finish(fmt.Errorf("%w: %s exited", ErrClosed, c.server.Name))
			} else {
				c.finish(fmt.Errorf("lsp %s: %w", c.server.Name, err))
			}
			return
		}
		c.dispatch(payload)
	}
}

// dispatch 路由一条解码后的消息。
func (c *Client) dispatch(payload []byte) {
	var message inbound
	if err := json.Unmarshal(payload, &message); err != nil {
		// 服务器可能在 stdout 上打印横幅；忽略无法解析的帧。
		return
	}
	if message.Method != "" {
		if len(message.ID) > 0 && string(message.ID) != "null" {
			c.answer(message.ID, message.Method)
		} else {
			c.handleNotification(message.Method, message.Params)
		}
		return
	}

	id, ok := parseID(message.ID)
	if !ok {
		return
	}
	c.mu.Lock()
	replies := c.pending[id]
	c.mu.Unlock()
	if replies == nil {
		return
	}
	reply := response{result: message.Result}
	if message.Error != nil {
		reply.err = message.Error
	}
	select {
	case replies <- reply:
	default:
	}
}

// answer 回复服务器发起的请求，使服务器永不阻塞。
func (c *Client) answer(id json.RawMessage, method string) {
	var result any
	switch method {
	case "workspace/configuration":
		// 我们没有可提供的设置；空数组是文档规定的答案。
		result = []any{}
	case "workspace/workspaceFolders":
		result = []any{}
	case "workspace/applyEdit":
		result = map[string]any{"applied": false}
	case "client/registerCapability", "client/unregisterCapability":
		result = nil
	case "window/workDoneProgress/create":
		result = nil
	default:
		result = nil
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		return
	}
	_ = c.write(payload)
}

// handleNotification 保存工具读取的状态。
func (c *Client) handleNotification(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	c.storeDiagnostics(params)
}

// finish 记录终止错误并恰好唤醒每个等待者一次。
func (c *Client) finish(err error) {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		pending := c.pending
		c.pending = make(map[uint64]chan response, len(pending))
		c.mu.Unlock()

		close(c.doneCh)
		for _, replies := range pending {
			select {
			case replies <- response{err: err}:
			default:
			}
		}
		c.diagMu.Lock()
		for key, waiter := range c.waiters {
			close(waiter.ch)
			delete(c.waiters, key)
		}
		c.diagMu.Unlock()
	})
}

func (c *Client) closedError(method string) error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err == nil {
		err = ErrClosed
	}
	if method == "" {
		return err
	}
	return fmt.Errorf("%w (during %s)", err, method)
}

// Close 关闭服务器：shutdown、exit，最后以 kill 兜底。
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		_, _ = c.call(ctx, "shutdown", nil)
		_ = c.notify(ctx, "exit", nil)
		cancel()
		c.finish(ErrClosed)
		// 在写锁下关闭 stdin，防止其与迟到的写入竞争。
		c.writeMu.Lock()
		_ = c.stdin.Close()
		c.writeMu.Unlock()
		// 给服务器一点时间自行退出；杀掉正在离开的进程
		// 给服务器一点时间自行退出；杀掉正在离开的进程会把干净的退出变成虚假的失败。
		select {
		case <-c.readDone:
		case <-time.After(readerGrace):
			if c.kill != nil {
				_ = c.kill()
			}
			select {
			case <-c.readDone:
			case <-time.After(readerGrace):
			}
		}
		if c.wait != nil {
			c.waitErr = c.wait()
		}
	})
	return c.waitErr
}

// drainStderr 保留服务器 stderr 的尾部供诊断。
func (c *Client) drainStderr(stderr io.Reader) {
	reader := bufio.NewReaderSize(stderr, 4<<10)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			c.ledger.add(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) stderrText() string { return c.ledger.text() }

func parseID(raw json.RawMessage) (uint64, bool) {
	text := bytes.TrimSpace(raw)
	if len(text) == 0 || string(text) == "null" {
		return 0, false
	}
	id, err := strconv.ParseUint(string(text), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// tailBuffer 保留流中最后 n 行。
type tailBuffer struct {
	mu    sync.Mutex
	lines []string
	limit int
}

func (t *tailBuffer) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.limit {
		t.lines = t.lines[len(t.lines)-t.limit:]
	}
}

func (t *tailBuffer) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
