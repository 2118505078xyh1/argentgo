package lsp

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedMessage 记录假服务器收到的一条 JSON-RPC 消息。
type capturedMessage struct {
	Method string
	Params json.RawMessage
}

// newWritethroughFakeClient 在两条管道上启动进程内假服务器。与 newIDEFakeClient 不同，
// 它把无 id 的通知也交给 onNotify，以便断言 didChangeWatchedFiles；带 id 的请求照常交给 responder。
func newWritethroughFakeClient(t *testing.T, responder ideResponder, onNotify func(string, json.RawMessage)) *Client {
	t.Helper()
	clientToServerRead, clientToServerWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	serverToClientRead, serverToClientWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer serverToClientWrite.Close()
		reader := bufio.NewReader(clientToServerRead)
		for {
			payload, err := readFrame(reader)
			if err != nil {
				return
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(payload, &msg) != nil {
				continue
			}
			if len(msg.ID) == 0 {
				if onNotify != nil {
					onNotify(msg.Method, append(json.RawMessage(nil), msg.Params...))
				}
				continue
			}
			var result any
			var rpcErr *rpcError
			if responder != nil {
				result, rpcErr = responder(msg.Method, msg.Params)
			}
			reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
			if rpcErr != nil {
				reply["error"] = rpcErr
			} else {
				reply["result"] = result
			}
			encoded, err := json.Marshal(reply)
			if err != nil || writeFrame(serverToClientWrite, encoded) != nil {
				return
			}
		}
	}()

	client := newClient(Server{Name: "writethrough-fake"}, t.TempDir(), clientToServerWrite, serverToClientRead)
	t.Cleanup(func() {
		_ = client.Close()
		_ = clientToServerRead.Close()
		_ = serverToClientRead.Close()
		<-finished
	})
	return client
}

func TestWillRenameFilesSendsMethodParamsAndParsesEdit(t *testing.T) {
	span := ideRange(0, 0, 1, 2)
	var mu sync.Mutex
	var method string
	var params json.RawMessage
	client := newWritethroughFakeClient(t, func(m string, p json.RawMessage) (any, *rpcError) {
		mu.Lock()
		method = m
		params = append(json.RawMessage(nil), p...)
		mu.Unlock()
		return map[string]any{
			"changes": map[string]any{
				"file:///dst.go": []any{map[string]any{"range": rangeMap(span), "newText": "Dst"}},
			},
		}, nil
	}, nil)

	edit, err := client.WillRenameFiles(ideCtx(t), []FileRename{
		{OldURI: "file:///old.go", NewURI: "file:///new.go"},
	})
	if err != nil {
		t.Fatalf("WillRenameFiles: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if method != "workspace/willRenameFiles" {
		t.Fatalf("method = %q, want workspace/willRenameFiles", method)
	}
	var decoded struct {
		Files []struct {
			OldURI string `json:"oldUri"`
			NewURI string `json:"newUri"`
		} `json:"files"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatalf("willRenameFiles params: %v", err)
	}
	wantFiles := []struct {
		OldURI string `json:"oldUri"`
		NewURI string `json:"newUri"`
	}{{OldURI: "file:///old.go", NewURI: "file:///new.go"}}
	if !reflect.DeepEqual(decoded.Files, wantFiles) {
		t.Fatalf("files = %#v, want %#v", decoded.Files, wantFiles)
	}

	wantEdit := &WorkspaceEdit{
		Changes: map[string][]TextEdit{"file:///dst.go": {{Range: span, NewText: "Dst"}}},
	}
	if !reflect.DeepEqual(edit, wantEdit) {
		t.Fatalf("WorkspaceEdit = %#v, want %#v", edit, wantEdit)
	}
}

func TestWillRenameFilesNullResultAndError(t *testing.T) {
	nullClient := newWritethroughFakeClient(t, func(_ string, p json.RawMessage) (any, *rpcError) {
		var decoded struct {
			Files []json.RawMessage `json:"files"`
		}
		if err := json.Unmarshal(p, &decoded); err != nil {
			t.Errorf("params: %v", err)
		}
		if len(decoded.Files) != 0 {
			t.Errorf("nil renames should send empty files, got %d", len(decoded.Files))
		}
		return nil, nil
	}, nil)
	edit, err := nullClient.WillRenameFiles(ideCtx(t), nil)
	if err != nil {
		t.Fatalf("WillRenameFiles with null result: %v", err)
	}
	if edit != nil {
		t.Fatalf("WillRenameFiles with null result = %#v, want nil", edit)
	}

	const message = "rename refused"
	errClient := newWritethroughFakeClient(t, func(_ string, _ json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: -32000, Message: message}
	}, nil)
	_, err = errClient.WillRenameFiles(ideCtx(t), []FileRename{
		{OldURI: "file:///a.go", NewURI: "file:///b.go"},
	})
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("WillRenameFiles error = %v, want propagated server error %q", err, message)
	}
}

func TestDidChangeWatchedFilesSendsNotification(t *testing.T) {
	notifications := make(chan capturedMessage, 8)
	client := newWritethroughFakeClient(t, nil, func(method string, params json.RawMessage) {
		notifications <- capturedMessage{Method: method, Params: params}
	})

	err := client.DidChangeWatchedFiles(ideCtx(t), []FileChange{
		{URI: "file:///a.go", Type: FileChangeCreated},
		{URI: "file:///b.go", Type: FileChangeChanged},
		{URI: "file:///c.go", Type: FileChangeDeleted},
	})
	if err != nil {
		t.Fatalf("DidChangeWatchedFiles: %v", err)
	}

	select {
	case got := <-notifications:
		if got.Method != "workspace/didChangeWatchedFiles" {
			t.Fatalf("notification method = %q, want workspace/didChangeWatchedFiles", got.Method)
		}
		var decoded struct {
			Changes []struct {
				URI  string `json:"uri"`
				Type int    `json:"type"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(got.Params, &decoded); err != nil {
			t.Fatalf("didChangeWatchedFiles params: %v", err)
		}
		want := []struct {
			URI  string `json:"uri"`
			Type int    `json:"type"`
		}{
			{URI: "file:///a.go", Type: 1},
			{URI: "file:///b.go", Type: 2},
			{URI: "file:///c.go", Type: 3},
		}
		if !reflect.DeepEqual(decoded.Changes, want) {
			t.Fatalf("changes = %#v, want %#v", decoded.Changes, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for didChangeWatchedFiles notification")
	}
}

func TestWorkspaceDiagnosticsParsesReports(t *testing.T) {
	span := ideRange(1, 2, 1, 6)
	var mu sync.Mutex
	var method string
	client := newWritethroughFakeClient(t, func(m string, _ json.RawMessage) (any, *rpcError) {
		mu.Lock()
		method = m
		mu.Unlock()
		return map[string]any{
			"items": []any{
				map[string]any{
					"uri":     "file:///a.go",
					"kind":    "full",
					"version": 7,
					"diagnostics": []any{map[string]any{
						"range": rangeMap(span), "severity": 2, "message": "warn", "source": "fake",
					}},
				},
				map[string]any{"uri": "file:///b.go", "kind": "unchanged", "resultId": "r1"},
			},
		}, nil
	}, nil)

	reports, err := client.WorkspaceDiagnostics(ideCtx(t))
	if err != nil {
		t.Fatalf("WorkspaceDiagnostics: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if method != "workspace/diagnostic" {
		t.Fatalf("method = %q, want workspace/diagnostic", method)
	}
	want := []WorkspaceDiagnosticReport{
		{
			URI:     "file:///a.go",
			Kind:    "full",
			Version: 7,
			Diagnostics: []Diagnostic{{
				URI: "file:///a.go", Range: span, Severity: "warning", Message: "warn", Source: "fake",
			}},
		},
		{URI: "file:///b.go", Kind: "unchanged", ResultID: "r1"},
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("WorkspaceDiagnostics = %#v, want %#v", reports, want)
	}
}

func TestWorkspaceDiagnosticsNullEmptyAndError(t *testing.T) {
	for _, result := range []any{nil, map[string]any{"items": []any{}}} {
		client := newWritethroughFakeClient(t, func(_ string, _ json.RawMessage) (any, *rpcError) {
			return result, nil
		}, nil)
		reports, err := client.WorkspaceDiagnostics(ideCtx(t))
		if err != nil {
			t.Fatalf("WorkspaceDiagnostics with empty result: %v", err)
		}
		if reports != nil {
			t.Fatalf("WorkspaceDiagnostics with empty result = %#v, want nil", reports)
		}
	}

	const message = "workspace diagnostics unavailable"
	errClient := newWritethroughFakeClient(t, func(_ string, _ json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: -32801, Message: message}
	}, nil)
	_, err := errClient.WorkspaceDiagnostics(ideCtx(t))
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("WorkspaceDiagnostics error = %v, want propagated server error %q", err, message)
	}
}
