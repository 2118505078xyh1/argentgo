package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ideResponder 回答一条 JSON-RPC 请求。非 nil 的 rpcError 作为响应的 "error" 成员发送，使客户端的 call 辅助函数传播它。
// 作为响应的 "error" 成员发送，使客户端的 call 辅助函数传播它。
type ideResponder func(method string, params json.RawMessage) (any, *rpcError)

// newIDEFakeClient 在两条管道上启动一个小的进程内语言服务器。它演练与生产客户端相同的分帧和调用路径，但
// 它演练与生产客户端相同的分帧和调用路径，但
// 只回答被测的功能请求。通知（无 id）被
// 只回答被测的功能请求。通知（无 id）被忽略；每个带 id 的请求传给 responder。
func newIDEFakeClient(t *testing.T, responder ideResponder) *Client {
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
				continue // notification
			}
			result, rpcErr := responder(msg.Method, msg.Params)
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

	client := newClient(Server{Name: "ide-fake"}, t.TempDir(), clientToServerWrite, serverToClientRead)
	t.Cleanup(func() {
		_ = client.Close()
		_ = clientToServerRead.Close()
		_ = serverToClientRead.Close()
		<-finished
	})
	return client
}

func ideCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func ideRange(line1, char1, line2, char2 int) Range {
	return Range{
		Start: Position{Line: line1, Character: char1},
		End:   Position{Line: line2, Character: char2},
	}
}

func rangeMap(r Range) map[string]any {
	return map[string]any{
		"start": map[string]any{"line": r.Start.Line, "character": r.Start.Character},
		"end":   map[string]any{"line": r.End.Line, "character": r.End.Character},
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

// ideCall 把类型化客户端方法适配为统一调用签名，供
// ideCall 把类型化客户端方法适配为下面表测试使用的统一调用签名，并带有有界 context，使卡住的假服务器不会挂住。
func ideCall[T any](fn func(context.Context, *Client) (T, error)) func(*Client) (any, error) {
	return func(c *Client) (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return fn(ctx, c)
	}
}

func TestParseSymbolsFlattensDocumentSymbolTreeWithDepth(t *testing.T) {
	outer := ideRange(0, 0, 10, 0)
	inner := ideRange(2, 2, 3, 4)
	leaf := ideRange(3, 6, 3, 9)

	raw := mustJSON(t, []any{
		map[string]any{
			"name":           "outer",
			"kind":           5,
			"detail":         "package",
			"range":          rangeMap(outer),
			"selectionRange": rangeMap(inner),
			"children": []any{
				map[string]any{
					"name":           "inner",
					"kind":           6,
					"range":          rangeMap(inner),
					"selectionRange": rangeMap(inner),
					"children": []any{
						map[string]any{
							"name":           "leaf",
							"kind":           12,
							"range":          rangeMap(leaf),
							"selectionRange": rangeMap(leaf),
						},
					},
				},
			},
		},
	})

	want := []Symbol{
		{Name: "outer", Kind: 5, Range: outer, SelectionRange: inner, Detail: "package", Depth: 0},
		{Name: "inner", Kind: 6, Range: inner, SelectionRange: inner, Depth: 1},
		{Name: "leaf", Kind: 12, Range: leaf, SelectionRange: leaf, Depth: 2},
	}
	got := parseSymbols(raw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSymbols = %#v, want %#v", got, want)
	}
}

func TestParseSymbolsHandlesSymbolInformationFallback(t *testing.T) {
	span := ideRange(4, 1, 4, 7)
	raw := mustJSON(t, []any{
		map[string]any{
			"name":          "Foo",
			"kind":          5,
			"containerName": "pkg",
			"location": map[string]any{
				"uri":   "file:///foo.go",
				"range": rangeMap(span),
			},
		},
	})

	want := []Symbol{{
		Name:           "Foo",
		Kind:           5,
		Range:          span,
		SelectionRange: span,
		URI:            "file:///foo.go",
		Depth:          0,
	}}
	got := parseSymbols(raw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSymbols fallback = %#v, want %#v", got, want)
	}
}

func TestParseHelpersReturnNilForNullOrEmptyResults(t *testing.T) {
	null := json.RawMessage("null")
	if got := parseSymbols(null); got != nil {
		t.Fatalf("parseSymbols(null) = %#v, want nil", got)
	}
	if got := parseSymbols(json.RawMessage(`[]`)); got != nil {
		t.Fatalf("parseSymbols([]) = %#v, want nil", got)
	}
	if got := parseSymbolInformation(null); got != nil {
		t.Fatalf("parseSymbolInformation(null) = %#v, want nil", got)
	}
	if got := parseTextEdits(null); got != nil {
		t.Fatalf("parseTextEdits(null) = %#v, want nil", got)
	}
	if got := parseCodeActions(null); got != nil {
		t.Fatalf("parseCodeActions(null) = %#v, want nil", got)
	}
	if got := parseWorkspaceEdit(null); got != nil {
		t.Fatalf("parseWorkspaceEdit(null) = %#v, want nil", got)
	}
}

func TestParseTextEditsAcceptsArrayAndSingleObject(t *testing.T) {
	span := ideRange(0, 0, 0, 3)
	editObj := map[string]any{"range": rangeMap(span), "newText": "x"}

	got := parseTextEdits(mustJSON(t, []any{editObj}))
	want := []TextEdit{{Range: span, NewText: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTextEdits(array) = %#v, want %#v", got, want)
	}

	got = parseTextEdits(mustJSON(t, editObj))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTextEdits(single) = %#v, want %#v", got, want)
	}
}

func TestParseWorkspaceEditMergesChangesAndDocumentChanges(t *testing.T) {
	span := ideRange(1, 0, 1, 4)
	other := ideRange(2, 0, 2, 2)
	fromMap := TextEdit{Range: span, NewText: "fromMap"}
	fromDoc := TextEdit{Range: other, NewText: "fromDoc"}

	raw := mustJSON(t, map[string]any{
		"changes": map[string]any{
			"file:///a.go": []any{map[string]any{"range": rangeMap(span), "newText": "fromMap"}},
		},
		"documentChanges": []any{
			map[string]any{
				"textDocument": map[string]any{"uri": "file:///b.go", "version": 3},
				"edits":        []any{map[string]any{"range": rangeMap(other), "newText": "fromDoc"}},
			},
			// 创建文件类资源操作不带编辑，被跳过。
			map[string]any{"kind": "create", "uri": "file:///new.go"},
		},
	})

	want := &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			"file:///a.go": {fromMap},
			"file:///b.go": {fromDoc},
		},
		DocumentChanges: []DocumentEdit{{URI: "file:///b.go", Edits: []TextEdit{fromDoc}}},
	}
	got := parseWorkspaceEdit(raw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWorkspaceEdit = %#v, want %#v", got, want)
	}
}

func TestParseCodeActionsDecodesEditsAndDiagnostics(t *testing.T) {
	span := ideRange(0, 2, 0, 7)
	raw := mustJSON(t, []any{
		map[string]any{
			"title":       "Fix it",
			"kind":        "quickfix",
			"isPreferred": true,
			"diagnostics": []any{
				map[string]any{"range": rangeMap(span), "severity": 2, "message": "warn", "source": "fake"},
			},
			"edit": map[string]any{
				"changes": map[string]any{
					"file:///x.go": []any{map[string]any{"range": rangeMap(span), "newText": "ok"}},
				},
			},
			"command":  map[string]any{"title": "Run", "command": "tool.run"},
			"disabled": map[string]any{"reason": "not now"},
		},
	})

	want := []CodeAction{{
		Title:       "Fix it",
		Kind:        "quickfix",
		IsPreferred: true,
		Diagnostics: []Diagnostic{{Range: span, Severity: "warning", Message: "warn", Source: "fake"}},
		Edit: &WorkspaceEdit{
			Changes: map[string][]TextEdit{
				"file:///x.go": {{Range: span, NewText: "ok"}},
			},
		},
		Command:  &Command{Title: "Run", Command: "tool.run"},
		Disabled: &CodeActionDisabled{Reason: "not now"},
	}}
	got := parseCodeActions(raw)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCodeActions = %#v, want %#v", got, want)
	}
}

func TestIDEMethodsAgainstFakeServer(t *testing.T) {
	span := ideRange(0, 0, 2, 0)
	editObj := map[string]any{"range": rangeMap(span), "newText": "fixed"}
	edit := TextEdit{Range: span, NewText: "fixed"}

	cases := []struct {
		name     string
		method   string
		result   any
		want     any
		nullWant any
		call     func(*Client) (any, error)
	}{
		{
			name:   "DocumentSymbol",
			method: "textDocument/documentSymbol",
			result: []any{map[string]any{
				"name": "Foo", "kind": 5,
				"range": rangeMap(span), "selectionRange": rangeMap(span),
			}},
			want:     []Symbol{{Name: "Foo", Kind: 5, Range: span, SelectionRange: span, Depth: 0}},
			nullWant: []Symbol(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]Symbol, error) {
				return c.DocumentSymbol(ctx, "file:///x.go")
			}),
		},
		{
			name:     "References",
			method:   "textDocument/references",
			result:   []any{map[string]any{"uri": "file:///ref.go", "range": rangeMap(span)}},
			want:     []Location{{URI: "file:///ref.go", Range: span}},
			nullWant: []Location(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]Location, error) {
				return c.References(ctx, "file:///x.go", Human(1, 1), true)
			}),
		},
		{
			name:   "CodeActions",
			method: "textDocument/codeAction",
			result: []any{map[string]any{
				"title": "Fix it", "kind": "quickfix",
				"diagnostics": []any{map[string]any{
					"range": rangeMap(span), "severity": 1, "message": "boom",
				}},
				"edit": map[string]any{"changes": map[string]any{
					"file:///x.go": []any{editObj},
				}},
			}},
			want: []CodeAction{{
				Title:       "Fix it",
				Kind:        "quickfix",
				Diagnostics: []Diagnostic{{Range: span, Severity: "error", Message: "boom"}},
				Edit: &WorkspaceEdit{
					Changes: map[string][]TextEdit{"file:///x.go": {edit}},
				},
			}},
			nullWant: []CodeAction(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]CodeAction, error) {
				return c.CodeActions(ctx, "file:///x.go", span, nil)
			}),
		},
		{
			name:   "Rename",
			method: "textDocument/rename",
			result: map[string]any{
				"changes": map[string]any{"file:///x.go": []any{editObj}},
			},
			want: &WorkspaceEdit{
				Changes: map[string][]TextEdit{"file:///x.go": {edit}},
			},
			nullWant: (*WorkspaceEdit)(nil),
			call: ideCall(func(ctx context.Context, c *Client) (*WorkspaceEdit, error) {
				return c.Rename(ctx, "file:///x.go", Human(1, 1), "Bar")
			}),
		},
		{
			name:     "Formatting",
			method:   "textDocument/formatting",
			result:   []any{editObj},
			want:     []TextEdit{edit},
			nullWant: []TextEdit(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]TextEdit, error) {
				return c.Formatting(ctx, "file:///x.go", FormattingOptions{TabSize: 4, InsertSpaces: true})
			}),
		},
		{
			name:   "WorkspaceSymbols",
			method: "workspace/symbol",
			result: []any{map[string]any{
				"name": "Foo", "kind": 5,
				"location": map[string]any{"uri": "file:///x.go", "range": rangeMap(span)},
			}},
			want: []SymbolInformation{{
				Name:     "Foo",
				Kind:     5,
				Location: Location{URI: "file:///x.go", Range: span},
			}},
			nullWant: []SymbolInformation(nil),
			call: ideCall(func(ctx context.Context, c *Client) ([]SymbolInformation, error) {
				return c.WorkspaceSymbols(ctx, "Foo")
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			okClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return tc.result, nil
				}
				return nil, nil
			})
			got, err := tc.call(okClient)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s = %#v, want %#v", tc.name, got, tc.want)
			}

			nullClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return nil, nil
				}
				return nil, nil
			})
			got, err = tc.call(nullClient)
			if err != nil {
				t.Fatalf("%s with null result: %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.nullWant) {
				t.Fatalf("%s with null result = %#v, want %#v", tc.name, got, tc.nullWant)
			}

			message := "kaboom " + tc.method
			errClient := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
				if method == tc.method {
					return nil, &rpcError{Code: -32000, Message: message}
				}
				return nil, nil
			})
			_, err = tc.call(errClient)
			if err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("%s error = %v, want propagated server error %q", tc.name, err, message)
			}
		})
	}
}

func TestIDEMethodsSendExpectedParams(t *testing.T) {
	var mu sync.Mutex
	got := make(map[string]json.RawMessage)
	client := newIDEFakeClient(t, func(method string, params json.RawMessage) (any, *rpcError) {
		mu.Lock()
		got[method] = params
		mu.Unlock()
		return nil, nil
	})
	ctx := ideCtx(t)

	if _, err := client.References(ctx, "file:///x.go", Human(2, 3), true); err != nil {
		t.Fatalf("References: %v", err)
	}
	if _, err := client.Rename(ctx, "file:///x.go", Human(2, 3), "Bar"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := client.WorkspaceSymbols(ctx, "Foo"); err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var references struct {
		Context struct {
			IncludeDeclaration bool `json:"includeDeclaration"`
		} `json:"context"`
	}
	if err := json.Unmarshal(got["textDocument/references"], &references); err != nil {
		t.Fatalf("references params: %v", err)
	}
	if !references.Context.IncludeDeclaration {
		t.Fatal("references context lost includeDeclaration=true")
	}

	var rename struct {
		NewName string `json:"newName"`
	}
	if err := json.Unmarshal(got["textDocument/rename"], &rename); err != nil {
		t.Fatalf("rename params: %v", err)
	}
	if rename.NewName != "Bar" {
		t.Fatalf("rename newName = %q, want Bar", rename.NewName)
	}

	var symbols struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(got["workspace/symbol"], &symbols); err != nil {
		t.Fatalf("workspace/symbol params: %v", err)
	}
	if symbols.Query != "Foo" {
		t.Fatalf("workspace/symbol query = %q, want Foo", symbols.Query)
	}
}

func TestDocumentSymbolAtOpensPath(t *testing.T) {
	client := newIDEFakeClient(t, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "textDocument/documentSymbol" {
			return []any{}, nil
		}
		return nil, nil
	})
	path := filepath.Join(t.TempDir(), "main.go")
	writeTestFile(t, path, "package main\n")

	symbols, err := client.DocumentSymbolAt(ideCtx(t), path)
	if err != nil {
		t.Fatalf("DocumentSymbolAt: %v", err)
	}
	if len(symbols) != 0 {
		t.Fatalf("symbols = %#v, want none", symbols)
	}
}
