package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// TestMain 同时充当 TestStartSpawnsARealProcess 所用
// helper 进程的入口：设置环境变量后，该二进制
// TestMain 同时充当 TestStartSpawnsARealProcess 所用 helper 进程的入口：设置环境变量后，该二进制在 stdin/stdout 上讲 LSP 而非运行测试。
func TestMain(m *testing.M) {
	if os.Getenv("ARGENT_GO_LSP_HELPER") != "" {
		os.Exit(runFakeHelper())
	}
	os.Exit(m.Run())
}

func runFakeHelper() int {
	if err := serveFake(os.Stdin, os.Stdout, fakeOptions{}, &fakeRecorder{}); err != nil && err != io.EOF {
		fmt.Fprintln(os.Stderr, "fake lsp helper:", err)
		return 1
	}
	return 0
}

// fakeOptions 调整假服务器的行为。
type fakeOptions struct {
	// neverPublish 使 didOpen/didChange 静默，用于超时测试。
	neverPublish bool
	// noServerRequests 跳过 initialize 之后发送的服务器到客户端请求。
	noServerRequests bool
}

// fakeRecorder 收集假服务器所见内容，可安全并发读取。
type fakeRecorder struct {
	mu          sync.Mutex
	initialized bool
	versions    []int
	pending     map[string]string
	answers     map[string]string
}

func (r *fakeRecorder) setInitialized() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initialized = true
}

func (r *fakeRecorder) isInitialized() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initialized
}

func (r *fakeRecorder) recordVersion(version int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions = append(r.versions, version)
}

func (r *fakeRecorder) recordedVersions() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.versions...)
}

func (r *fakeRecorder) noteServerRequest(id, method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = make(map[string]string)
	}
	r.pending[id] = method
}

func (r *fakeRecorder) noteAnswer(id, result []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	method := r.pending[string(id)]
	if method == "" {
		return
	}
	if r.answers == nil {
		r.answers = make(map[string]string)
	}
	r.answers[method] = string(result)
}

func (r *fakeRecorder) answer(method string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.answers[method]
	return value, ok
}

// serveFake 为客户端测试实现了足够多的语言服务器功能：
// serveFake 为客户端测试实现了足够多的语言服务器功能：握手、带诊断的文档同步、definition 和 hover。
func serveFake(input io.Reader, output io.Writer, opts fakeOptions, recorder *fakeRecorder) error {
	reader := bufio.NewReader(input)
	send := func(value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return writeFrame(output, payload)
	}
	reply := func(id json.RawMessage, value any) error {
		return send(map[string]any{"jsonrpc": "2.0", "id": id, "result": value})
	}

	for {
		payload, err := readFrame(reader)
		if err != nil {
			return err
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}
		if message.Method == "" {
			recorder.noteAnswer(message.ID, message.Result)
			continue
		}

		switch message.Method {
		case "initialize":
			if err := reply(message.ID, map[string]any{
				"capabilities": map[string]any{"textDocumentSync": 1},
			}); err != nil {
				return err
			}
			if opts.noServerRequests {
				continue
			}
			recorder.noteServerRequest("100", "client/registerCapability")
			if err := send(map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage("100"),
				"method":  "client/registerCapability",
				"params":  map[string]any{"registrations": []any{}},
			}); err != nil {
				return err
			}
			recorder.noteServerRequest("101", "workspace/configuration")
			if err := send(map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage("101"),
				"method":  "workspace/configuration",
				"params":  map[string]any{"items": []any{}},
			}); err != nil {
				return err
			}
		case "initialized":
			recorder.setInitialized()
		case "shutdown":
			if err := reply(message.ID, nil); err != nil {
				return err
			}
		case "exit":
			return nil
		case "textDocument/didOpen", "textDocument/didChange":
			var params struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				continue
			}
			recorder.recordVersion(params.TextDocument.Version)
			if opts.neverPublish {
				continue
			}
			severity, text := 1, "boom"
			if message.Method == "textDocument/didChange" {
				severity, text = 2, "changed"
			}
			if err := publishDiagnostics(send, params.TextDocument.URI, params.TextDocument.Version, severity, text); err != nil {
				return err
			}
		case "textDocument/didSave":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				continue
			}
			if err := publishDiagnostics(send, params.TextDocument.URI, 0, 0, ""); err != nil {
				return err
			}
		case "textDocument/definition":
			if err := reply(message.ID, []map[string]any{{
				"uri": "file:///defs.go",
				"range": map[string]any{
					"start": map[string]any{"line": 3, "character": 1},
					"end":   map[string]any{"line": 3, "character": 9},
				},
			}}); err != nil {
				return err
			}
		case "textDocument/hover":
			if err := reply(message.ID, map[string]any{
				"contents": map[string]any{"kind": "markdown", "value": "**hover**"},
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 3},
				},
			}); err != nil {
				return err
			}
		default:
			if len(message.ID) > 0 {
				if err := reply(message.ID, nil); err != nil {
					return err
				}
			}
		}
	}
}

// publishDiagnostics 发送一条 textDocument/publishDiagnostics 通知。空消息按协议意图清除该 URI。
// 空消息按协议意图清除该 URI。
func publishDiagnostics(send func(any) error, uri string, version, severity int, message string) error {
	items := []any{}
	if message != "" {
		items = append(items, map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 2},
				"end":   map[string]any{"line": 0, "character": 7},
			},
			"severity": severity,
			"source":   "fake",
			"message":  message,
		})
	}
	params := map[string]any{"uri": uri, "diagnostics": items}
	if version > 0 {
		params["version"] = version
	}
	return send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  params,
	})
}

// startPipeClient 在进程内管道对上启动客户端，它演练与真实子进程相同的分帧和 goroutine。
// 与真实子进程相同的分帧和 goroutine。
func startPipeClient(t *testing.T, opts fakeOptions) (*Client, *fakeRecorder) {
	t.Helper()
	clientToServerRead, clientToServerWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	serverToClientRead, serverToClientWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	recorder := &fakeRecorder{}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer serverToClientWrite.Close()
		_ = serveFake(clientToServerRead, serverToClientWrite, opts, recorder)
	}()

	client := newClient(Server{Name: "fake"}, t.TempDir(), clientToServerWrite, serverToClientRead)
	t.Cleanup(func() {
		_ = client.Close()
		_ = clientToServerRead.Close()
		_ = serverToClientRead.Close()
		<-finished
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return client, recorder
}

// waitFor 轮询 cond 直到其成立或截止时间到达。
func waitFor(t *testing.T, description string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
