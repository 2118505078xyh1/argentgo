package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTransport answers in-process, so client behaviour can be tested without
// spawning anything.
type fakeTransport struct {
	mu       sync.Mutex
	handlers map[string]func(json.RawMessage) (any, error)
	notes    []string
	delay    time.Duration
	closed   bool
}

func (f *fakeTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	handler := f.handlers[method]
	f.mu.Unlock()
	if handler == nil {
		return nil, &rpcErrorBody{Code: -32601, Message: "no such method: " + method}
	}
	value, err := handler(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (f *fakeTransport) notify(_ context.Context, method string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, method)
	return nil
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func newFakeClient(name string, ft *fakeTransport, timeout time.Duration) *client {
	return &client{spec: Spec{Name: name}, tr: ft, timeout: timeout}
}

func TestClientRoundTrip(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"initialize": func(json.RawMessage) (any, error) {
			return map[string]any{
				"protocolVersion": "2024-11-05",
				"instructions":    "be brief",
				"serverInfo":      map[string]any{"name": "fake", "version": "1.2.3"},
			}, nil
		},
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{
					"name":        "read_file",
					"description": "reads",
					"inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": true},
				},
				map[string]any{"description": "nameless tools are skipped"},
			}}, nil
		},
		"tools/call": func(params json.RawMessage) (any, error) {
			var call struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(params, &call); err != nil {
				return nil, err
			}
			text, _ := call.Arguments["text"].(string)
			return map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "first " + text},
				map[string]any{"type": "text", "text": "second"},
			}}, nil
		},
		"resources/list": func(json.RawMessage) (any, error) {
			return map[string]any{"resources": []any{
				map[string]any{"uri": "file:///a.txt", "name": "a", "mimeType": "text/plain"},
			}}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Name != "fake" || info.Version != "1.2.3" || info.ProtocolVersion != "2024-11-05" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Instructions != "be brief" {
		t.Fatalf("instructions lost: %+v", info)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "read_file" || !tools[0].ReadOnly {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].Server != "fake" {
		t.Fatalf("tool not tagged with its server: %+v", tools[0])
	}
	if string(tools[0].Schema) != `{"type":"object"}` {
		t.Fatalf("schema lost: %s", tools[0].Schema)
	}

	text, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if text != "first x\nsecond" {
		t.Fatalf("text blocks not joined: %q", text)
	}

	resources, err := c.ListResources(context.Background())
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if len(resources) != 1 || resources[0].URI != "file:///a.txt" {
		t.Fatalf("unexpected resources: %+v", resources)
	}

	ft.mu.Lock()
	notes := append([]string(nil), ft.notes...)
	ft.mu.Unlock()
	if len(notes) != 1 || notes[0] != "notifications/initialized" {
		t.Fatalf("unexpected notifications: %v", notes)
	}
}

func TestCallToolSurfacesIsError(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) {
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "kaboom"}},
				"isError": true,
			}, nil
		},
	}}
	c := newFakeClient("fake", ft, time.Second)

	text, err := c.CallTool(context.Background(), "boom", nil)
	if err == nil {
		t.Fatal("expected an error for isError results")
	}
	if text != "kaboom" || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("error detail lost: text=%q err=%v", text, err)
	}
}

func TestCallToolRejectsInvalidArguments(t *testing.T) {
	c := newFakeClient("fake", &fakeTransport{}, time.Second)
	if _, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

func TestListResourcesTolerateMethodNotFound(t *testing.T) {
	c := newFakeClient("fake", &fakeTransport{}, time.Second)
	resources, err := c.ListResources(context.Background())
	if err != nil {
		t.Fatalf("method-not-found should be empty, not an error: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected no resources, got %+v", resources)
	}
}

func TestClientTimeout(t *testing.T) {
	ft := &fakeTransport{
		delay: 10 * time.Second,
		handlers: map[string]func(json.RawMessage) (any, error){
			"tools/list": func(json.RawMessage) (any, error) { return map[string]any{}, nil },
		},
	}
	c := newFakeClient("slow", ft, 40*time.Millisecond)

	started := time.Now()
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not surfaced: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestConnectRejectsUnknownTransport(t *testing.T) {
	_, err := Connect(context.Background(), Spec{Name: "x", Transport: "carrier-pigeon"})
	if !errors.Is(err, ErrUnsupportedTransport) {
		t.Fatalf("expected ErrUnsupportedTransport, got %v", err)
	}
}

func TestConnectInfersTransportAndValidatesSpec(t *testing.T) {
	c, err := Connect(context.Background(), Spec{
		Name:    "x",
		Command: os.Args[0],
		Env:     []string{"ARGENT_MCP_HELPER=echo"},
	})
	if err != nil {
		t.Fatalf("transport should be inferred as stdio: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := Connect(context.Background(), Spec{Name: "x", Command: "definitely-not-a-real-command-xyz"}); err == nil {
		t.Fatal("missing stdio command must fail")
	}
	if _, err := Connect(context.Background(), Spec{Name: "x", Transport: "stdio"}); err == nil {
		t.Fatal("stdio without a command must fail")
	}
	if _, err := Connect(context.Background(), Spec{Name: "x", Transport: "http"}); err == nil {
		t.Fatal("http without a URL must fail")
	}
}

func TestNamespaced(t *testing.T) {
	if got := Namespaced("fs", "read_file"); got != "fs__read_file" {
		t.Fatalf("unexpected namespace: %q", got)
	}
}

func TestWaitersCorrelateBatchesAndSkipNotifications(t *testing.T) {
	w := newWaiters()
	first := w.add(1)
	third := w.add(3)

	payload := `[{"jsonrpc":"2.0","method":"notifications/progress","params":{}},` +
		`{"jsonrpc":"2.0","id":1,"result":{"ok":true}},` +
		`{"jsonrpc":"2.0","id":"3","error":{"code":-32601,"message":"no such method"}}]`
	if err := w.handle([]byte(payload)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if reply := <-first; reply.err != nil || string(reply.result) != `{"ok":true}` {
		t.Fatalf("unexpected first reply: %+v", reply)
	}
	reply := <-third
	var body *rpcErrorBody
	if !errors.As(reply.err, &body) || body.Code != -32601 {
		t.Fatalf("string id not correlated: %+v", reply)
	}
}
