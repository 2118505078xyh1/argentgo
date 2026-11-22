package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCloser 把 net.Conn 的写端适配成 io.WriteCloser；测试不希望关闭管道。
type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }

// testHarness 是一条由 net.Pipe 驱动的假 DAP 会话：请求被记录到 requests，
// 响应由 responder 生成并直接投递到 Client 的 pending 表（无需运行 readLoop）。
type testHarness struct {
	session  *Session
	requests <-chan Message
	events   chan Event
}

// newHarness 启动假 adapter。responder 返回响应体与失败原因（非空表示失败）。
func newHarness(t *testing.T, responder func(Message) (json.RawMessage, string)) *testHarness {
	t.Helper()
	clientConn, adapterConn := net.Pipe()
	events := make(chan Event, 32)
	c := &Client{
		pending: make(map[int64]chan response),
		events:  events,
		stdin:   writeCloser{clientConn},
		done:    make(chan struct{}),
	}
	requests := make(chan Message, 32)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		r := bufio.NewReader(adapterConn)
		for {
			payload, err := readFrame(r)
			if err != nil {
				return
			}
			var m Message
			if err := json.Unmarshal(payload, &m); err != nil {
				return
			}
			requests <- m
			body, failure := responder(m)
			ch := c.takePending(int64(m.Seq))
			if ch == nil {
				continue
			}
			if failure != "" {
				ch <- response{Success: false, Message: failure}
			} else {
				ch <- response{Success: true, Body: body}
			}
			close(ch)
		}
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = adapterConn.Close()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("fake DAP adapter did not exit")
		}
	})
	return &testHarness{session: New(c), requests: requests, events: events}
}

// bodyResponder 按命令返回固定响应体，未列出的命令返回空对象。
func bodyResponder(bodies map[string]string) func(Message) (json.RawMessage, string) {
	return func(m Message) (json.RawMessage, string) {
		if body, ok := bodies[m.Command]; ok {
			return json.RawMessage(body), ""
		}
		return json.RawMessage(`{}`), ""
	}
}

func nextRequest(t *testing.T, h *testHarness) Message {
	t.Helper()
	select {
	case m := <-h.requests:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for DAP request")
		return Message{}
	}
}

func argsOf(t *testing.T, m Message) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(m.Arguments, &args); err != nil {
		t.Fatalf("decode arguments of %q: %v", m.Command, err)
	}
	return args
}

func assertCommand(t *testing.T, m Message, want string) map[string]any {
	t.Helper()
	if m.Command != want {
		t.Fatalf("command = %q, want %q", m.Command, want)
	}
	return argsOf(t, m)
}

func TestSessionHandshake(t *testing.T) {
	h := newHarness(t, bodyResponder(map[string]string{
		"initialize": `{"supportsConfigurationDoneRequest":true}`,
	}))
	ctx := context.Background()

	if err := h.session.Initialize(ctx, "dlv-dap", ""); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	args := assertCommand(t, nextRequest(t, h), "initialize")
	if args["adapterID"] != "dlv-dap" || args["clientID"] != "argentgo" {
		t.Fatalf("initialize arguments = %v, want adapterID/clientID", args)
	}
	if args["linesStartAt1"] != true || args["pathFormat"] != "path" {
		t.Fatalf("initialize missed path/column contract: %v", args)
	}
	if !h.session.SupportsConfigurationDone() {
		t.Fatal("supportsConfigurationDone not recorded from capabilities")
	}

	if err := h.session.Launch(ctx, "/demo/app", []string{"-v"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	args = assertCommand(t, nextRequest(t, h), "launch")
	if args["program"] != "/demo/app" {
		t.Fatalf("launch program = %v", args["program"])
	}
	if got, ok := args["args"].([]any); !ok || len(got) != 1 || got[0] != "-v" {
		t.Fatalf("launch args = %v", args["args"])
	}

	if err := h.session.ConfigurationDone(ctx); err != nil {
		t.Fatalf("ConfigurationDone: %v", err)
	}
	assertCommand(t, nextRequest(t, h), "configurationDone")

	if err := h.session.Attach(ctx, map[string]any{"processId": 1234}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	args = assertCommand(t, nextRequest(t, h), "attach")
	if args["processId"] != float64(1234) {
		t.Fatalf("attach processId = %v", args["processId"])
	}
}

func TestSessionSetBreakpoints(t *testing.T) {
	h := newHarness(t, bodyResponder(map[string]string{
		"setBreakpoints": `{"breakpoints":[{"id":7,"verified":true,"line":12},{"id":8,"verified":false,"line":34}]}`,
	}))
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "my file.go")

	bps, err := h.session.SetBreakpoints(ctx, path, []int{12, 34})
	if err != nil {
		t.Fatalf("SetBreakpoints: %v", err)
	}
	if len(bps) != 2 || bps[0].ID != 7 || !bps[0].Verified || bps[0].Line != 12 ||
		bps[1].ID != 8 || bps[1].Verified || bps[1].Line != 34 {
		t.Fatalf("breakpoints = %+v", bps)
	}

	args := assertCommand(t, nextRequest(t, h), "setBreakpoints")
	source, ok := args["source"].(map[string]any)
	if !ok {
		t.Fatalf("source = %v", args["source"])
	}
	uri, _ := source["path"].(string)
	if !strings.HasPrefix(uri, "file:///") || !strings.Contains(uri, "my%20file.go") {
		t.Fatalf("source.path = %q, want escaped file URI", uri)
	}
	if source["name"] != "my file.go" {
		t.Fatalf("source.name = %v", source["name"])
	}
	lines, ok := args["breakpoints"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("breakpoints = %v", args["breakpoints"])
	}

	if err := h.session.SetExceptionBreakpoints(ctx, []string{"all", "uncaught"}); err != nil {
		t.Fatalf("SetExceptionBreakpoints: %v", err)
	}
	args = assertCommand(t, nextRequest(t, h), "setExceptionBreakpoints")
	if filters, ok := args["filters"].([]any); !ok || len(filters) != 2 || filters[0] != "all" || filters[1] != "uncaught" {
		t.Fatalf("filters = %v", args["filters"])
	}
}

func TestSessionExecutionControl(t *testing.T) {
	h := newHarness(t, bodyResponder(map[string]string{
		"continue": `{"allThreadsContinued":true}`,
	}))
	ctx := context.Background()

	all, err := h.session.Continue(ctx, 5)
	if err != nil || !all {
		t.Fatalf("Continue = %v, %v; want true, nil", all, err)
	}
	if args := assertCommand(t, nextRequest(t, h), "continue"); args["threadId"] != float64(5) {
		t.Fatalf("continue threadId = %v", args["threadId"])
	}

	for _, command := range []string{"next", "stepIn", "stepOut", "pause"} {
		var err error
		switch command {
		case "next":
			err = h.session.Next(ctx, 9)
		case "stepIn":
			err = h.session.StepIn(ctx, 9)
		case "stepOut":
			err = h.session.StepOut(ctx, 9)
		case "pause":
			err = h.session.Pause(ctx, 9)
		}
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if args := assertCommand(t, nextRequest(t, h), command); args["threadId"] != float64(9) {
			t.Fatalf("%s threadId = %v", command, args["threadId"])
		}
	}
}

func TestSessionStateQueries(t *testing.T) {
	h := newHarness(t, bodyResponder(map[string]string{
		"threads":    `{"threads":[{"id":1,"name":"main"},{"id":2,"name":"worker"}]}`,
		"stackTrace": `{"stackFrames":[{"id":10,"name":"main.func","source":{"path":"file:///x/main.go","name":"main.go"},"line":42}]}`,
		"scopes":     `{"scopes":[{"name":"Locals","variablesReference":100}]}`,
		"variables":  `{"variables":[{"name":"x","value":"7","variablesReference":0},{"name":"p","value":"*main.T","variablesReference":200}]}`,
		"evaluate":   `{"result":"4"}`,
	}))
	ctx := context.Background()

	threads, err := h.session.Threads(ctx)
	if err != nil || len(threads) != 2 || threads[0].ID != 1 || threads[0].Name != "main" || threads[1].Name != "worker" {
		t.Fatalf("Threads = %+v, %v", threads, err)
	}
	assertCommand(t, nextRequest(t, h), "threads")

	frames, err := h.session.StackTrace(ctx, 1)
	if err != nil || len(frames) != 1 {
		t.Fatalf("StackTrace = %+v, %v", frames, err)
	}
	if frames[0].ID != 10 || frames[0].Name != "main.func" || frames[0].SourcePath != "file:///x/main.go" || frames[0].Line != 42 {
		t.Fatalf("stack frame = %+v", frames[0])
	}
	if args := assertCommand(t, nextRequest(t, h), "stackTrace"); args["threadId"] != float64(1) {
		t.Fatalf("stackTrace threadId = %v", args["threadId"])
	}

	scopes, err := h.session.Scopes(ctx, 10)
	if err != nil || len(scopes) != 1 || scopes[0].Name != "Locals" || scopes[0].VariablesReference != 100 {
		t.Fatalf("Scopes = %+v, %v", scopes, err)
	}
	if args := assertCommand(t, nextRequest(t, h), "scopes"); args["frameId"] != float64(10) {
		t.Fatalf("scopes frameId = %v", args["frameId"])
	}

	vars, err := h.session.Variables(ctx, 100)
	if err != nil || len(vars) != 2 || vars[0].Name != "x" || vars[0].Value != "7" || vars[1].VariablesReference != 200 {
		t.Fatalf("Variables = %+v, %v", vars, err)
	}
	if args := assertCommand(t, nextRequest(t, h), "variables"); args["variablesReference"] != float64(100) {
		t.Fatalf("variables variablesReference = %v", args["variablesReference"])
	}

	result, err := h.session.Evaluate(ctx, "x ^ 3", 10)
	if err != nil || result != "4" {
		t.Fatalf("Evaluate = %q, %v", result, err)
	}
	args := assertCommand(t, nextRequest(t, h), "evaluate")
	if args["expression"] != "x ^ 3" || args["context"] != "repl" || args["frameId"] != float64(10) {
		t.Fatalf("evaluate arguments = %v", args)
	}
}

func TestSessionWaitEvents(t *testing.T) {
	h := newHarness(t, bodyResponder(nil))
	ctx := context.Background()

	// 先推一个无关事件：WaitStopped 应跳过它并仍能拿到 stopped。
	h.events <- Event{Seq: 1, Event: "output", Body: json.RawMessage(`{}`)}
	h.events <- Event{Seq: 2, Event: "stopped", Body: json.RawMessage(`{"reason":"breakpoint","threadId":3,"text":"hit"}`)}
	body, err := h.session.WaitStopped(ctx, time.Second)
	if err != nil || body == nil {
		t.Fatalf("WaitStopped = %+v, %v", body, err)
	}
	if body.Reason != "breakpoint" || body.ThreadID != 3 || body.Text != "hit" {
		t.Fatalf("stopped body = %+v", body)
	}

	// 之前暂存的 output 不应导致 terminated 事件丢失。
	h.events <- Event{Seq: 3, Event: "terminated", Body: nil}
	if err := h.session.WaitTerminated(ctx, time.Second); err != nil {
		t.Fatalf("WaitTerminated: %v", err)
	}

	// 等待 stopped 时收到的 exited 应被暂存，供之后的 WaitTerminated 使用。
	h.events <- Event{Seq: 4, Event: "exited", Body: nil}
	h.events <- Event{Seq: 5, Event: "stopped", Body: json.RawMessage(`{"reason":"step","threadId":3}`)}
	if _, err := h.session.WaitStopped(ctx, time.Second); err != nil {
		t.Fatalf("second WaitStopped: %v", err)
	}
	if err := h.session.WaitTerminated(ctx, time.Second); err != nil {
		t.Fatalf("WaitTerminated from buffered exited: %v", err)
	}

	body, err = h.session.WaitStopped(ctx, 20*time.Millisecond)
	if body != nil || !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("WaitStopped timeout = %+v, %v; want nil, ErrWaitTimeout", body, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	body, err = h.session.WaitStopped(canceled, time.Second)
	if body != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitStopped canceled = %+v, %v", body, err)
	}
}

func TestSessionDisconnectAndCallError(t *testing.T) {
	h := newHarness(t, bodyResponder(nil))
	if err := h.session.Disconnect(context.Background()); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if args := assertCommand(t, nextRequest(t, h), "disconnect"); args["terminateDebuggee"] != true {
		t.Fatalf("disconnect arguments = %v", args)
	}

	failing := newHarness(t, func(m Message) (json.RawMessage, string) {
		if m.Command == "threads" {
			return nil, "boom"
		}
		return json.RawMessage(`{}`), ""
	})
	if _, err := failing.session.Threads(context.Background()); err == nil || !strings.Contains(err.Error(), "threads failed: boom") {
		t.Fatalf("Threads error = %v, want adapter failure surfaced", err)
	}
	assertCommand(t, nextRequest(t, failing), "threads")
}
