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

// Client-side errors.
var (
	// ErrClosed reports a client whose server has exited or been closed.
	ErrClosed = errors.New("lsp: client is closed")
	// ErrNoCommand reports a server specification without a program to run.
	ErrNoCommand = errors.New("lsp: server has no command")
	// ErrTimeout reports a diagnostics wait that expired before a publish.
	ErrTimeout = errors.New("lsp: timed out waiting for diagnostics")
)

const (
	initializeTimeout = 60 * time.Second
	shutdownTimeout   = 2 * time.Second
	readerGrace       = 3 * time.Second
	stderrTailLines   = 20
)

// Server describes how to launch one language server.
type Server struct {
	// Name identifies the server in messages, e.g. "gopls".
	Name string
	// Command is the program, resolved through PATH.
	Command string
	// Args are passed to the program.
	Args []string
	// Env holds extra KEY=VALUE pairs layered over the current environment.
	Env []string
	// LanguageIDs are the LSP language ids this server owns.
	LanguageIDs []string
	// RootURI overrides the workspace URI derived from the root path.
	RootURI string
}

// Client is one running language server and the documents it knows about.
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
	// mark is the publish sequence at the time of the last didOpen/didChange,
	// used to tell new diagnostics apart from ones the caller already saw.
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

// inbound is one decoded JSON-RPC message, whatever its kind.
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

// Start launches the server for root, completes the initialize handshake, and
// returns a ready client. The context bounds the handshake only; the client
// keeps running until Close.
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

// newClient wires a client onto an already connected transport. Tests use it
// with an io.Pipe pair; Start uses it with a child process.
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

// Root returns the absolute workspace root the client was started for.
func (c *Client) Root() string { return c.root }

// ServerName returns the specification's name.
func (c *Client) ServerName() string { return c.server.Name }

// initialize performs the LSP handshake.
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
		"clientInfo": map[string]any{"name": "argentgo", "version": "0.4.0"},
		"rootUri":    uri,
		"rootPath":   c.root,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition":         map[string]any{"linkSupport": true},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"publishDiagnostics": map[string]any{"relatedInformation": true},
				"synchronization":    map[string]any{"didSave": true},
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

// call sends a request and waits for the matching response.
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

// notify sends a notification, which has no response.
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

// cancelRequest asks the server to drop an abandoned request.
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

// readLoop turns framed messages into responses, notifications and answers.
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

// dispatch routes one decoded message.
func (c *Client) dispatch(payload []byte) {
	var message inbound
	if err := json.Unmarshal(payload, &message); err != nil {
		// A server may print banners on stdout; ignore unparsable frames.
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

// answer replies to a server-initiated request so the server never blocks.
func (c *Client) answer(id json.RawMessage, method string) {
	var result any
	switch method {
	case "workspace/configuration":
		// We have no settings to offer; an empty array is the documented answer.
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

// handleNotification keeps the state the tools read.
func (c *Client) handleNotification(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	c.storeDiagnostics(params)
}

// finish records the terminal error and wakes every waiter exactly once.
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

// Close shuts the server down: shutdown, exit, then kill as a backstop.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		_, _ = c.call(ctx, "shutdown", nil)
		_ = c.notify(ctx, "exit", nil)
		cancel()
		c.finish(ErrClosed)
		// Closing stdin under the write lock keeps it from racing a straggler.
		c.writeMu.Lock()
		_ = c.stdin.Close()
		c.writeMu.Unlock()
		// Give the server a moment to exit on its own; killing a process that
		// is already leaving turns a clean exit into a spurious failure.
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

// drainStderr keeps the tail of the server's stderr for diagnostics.
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

// tailBuffer keeps the last n lines of a stream.
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
