package dap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrWaitTimeout 表示等待 DAP 事件超时。调用方可用 errors.Is 判定。
var ErrWaitTimeout = errors.New("dap: timed out waiting for event")

// Session 包装一个已有 *Client，提供 DAP 初始化握手、断点、执行控制、
// 状态查询与事件等待的高层语义。adapter 子进程的启动由调用方通过 Start 完成；
// 本类型只管理协议会话（Client 的所有权与关闭仍归调用方）。
type Session struct {
	client *Client

	mu                 sync.Mutex
	supportsConfigDone bool

	// 事件队列与暂存：stopped/terminated/exited 之外的事件被丢弃；
	// exited 暂存供 WaitTerminated 消费（即使它先于 terminated 到达）。
	eventsMu       sync.Mutex
	stopQueue      []*StoppedBody
	stopWaiters    []chan *StoppedBody
	termPending    bool
	termWaiters    []chan struct{}
	stoppedStopped bool
}

// Breakpoint 是 setBreakpoints 返回的单个断点状态。
type Breakpoint struct {
	ID       int  `json:"id"`
	Verified bool `json:"verified"`
	Line     int  `json:"line"`
}

// Thread 是 DAP 线程。
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// StackFrame 是调用栈中的一帧。SourcePath 是 DAP 返回的 source.path。
type StackFrame struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	SourcePath string `json:"-"`
	Line       int    `json:"line"`
	Source     struct {
		Path string `json:"path"`
		Name string `json:"name"`
	} `json:"source"`
}

// Scope 是变量作用域。
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
}

// Variable 是一个变量。
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// New 用一个已建立帧通道的 Client 构造 Session，并启动事件消费。
func New(client *Client) *Session {
	if client == nil {
		return nil
	}
	s := &Session{client: client}
	go s.consumeEvents()
	return s
}

// Client 返回底层客户端，供调用方发送原始请求或关闭连接。
func (s *Session) Client() *Client { return s.client }

func (s *Session) consumeEvents() {
	for ev := range s.client.Events() {
		switch ev.Event {
		case "stopped":
			var body StoppedBody
			_ = json.Unmarshal(ev.Body, &body)
			s.eventsMu.Lock()
			if len(s.stopWaiters) > 0 {
				w := s.stopWaiters[0]
				s.stopWaiters = s.stopWaiters[1:]
				s.eventsMu.Unlock()
				w <- &body
			} else {
				s.stopQueue = append(s.stopQueue, &body)
				s.eventsMu.Unlock()
			}
		case "terminated", "exited":
			s.eventsMu.Lock()
			s.termPending = true
			for _, w := range s.termWaiters {
				close(w)
			}
			s.termWaiters = nil
			s.eventsMu.Unlock()
		}
	}
}

// Initialize 发送 initialize 并据 capabilities 记录是否需要 configurationDone。
func (s *Session) Initialize(ctx context.Context, adapterID, clientName string) error {
	if adapterID == "" {
		adapterID = "argentgo"
	}
	if clientName == "" {
		clientName = "argentgo"
	}
	args := map[string]any{
		"clientID":             clientName,
		"clientName":           clientName,
		"adapterID":            adapterID,
		"pathFormat":           "path",
		"linesStartAt1":        true,
		"columnsStartAt1":      true,
		"supportsVariableType": true,
	}
	body, err := s.client.Call(ctx, "initialize", args)
	if err != nil {
		return fmt.Errorf("dap: initialize: %w", err)
	}
	s.mu.Lock()
	s.supportsConfigDone = true
	if len(body) > 0 {
		var caps struct {
			SupportsConfigurationDoneRequest *bool `json:"supportsConfigurationDoneRequest"`
		}
		if err := json.Unmarshal(body, &caps); err == nil && caps.SupportsConfigurationDoneRequest != nil {
			s.supportsConfigDone = *caps.SupportsConfigurationDoneRequest
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) SupportsConfigurationDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supportsConfigDone
}

// Launch 发送 launch 请求。
func (s *Session) Launch(ctx context.Context, program string, args []string) error {
	if program == "" {
		return errors.New("dap: empty program")
	}
	_, err := s.client.Call(ctx, "launch", map[string]any{
		"program":    program,
		"args":       args,
		"console":    "internalConsole",
		"justMyCode": false,
	})
	return err
}

// Attach 发送 attach 请求，参数由调用方给出（如 processId）。
func (s *Session) Attach(ctx context.Context, args map[string]any) error {
	_, err := s.client.Call(ctx, "attach", args)
	return err
}

// ConfigurationDone 通知 adapter 配置发送完毕。
func (s *Session) ConfigurationDone(ctx context.Context) error {
	return s.client.Notify("configurationDone", map[string]any{})
}

// Disconnect 断开会话并请求终止被调试进程。
func (s *Session) Disconnect(ctx context.Context) error {
	_, err := s.client.Call(ctx, "disconnect", map[string]any{"terminateDebuggee": true})
	return err
}

// SetBreakpoints 把 path 的断点设为 lines，返回每个断点的确认状态。
func (s *Session) SetBreakpoints(ctx context.Context, path string, lines []int) ([]Breakpoint, error) {
	body, err := s.client.Call(ctx, "setBreakpoints", map[string]any{
		"source":      fileSource(path),
		"breakpoints": lineBreakpoints(lines),
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Breakpoints []Breakpoint `json:"breakpoints"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Breakpoints, nil
}

// fileSource 把本地路径转成 DAP source（path 用 file:// URI，name 用基名）。
func fileSource(path string) map[string]any {
	uri := path
	if !strings.HasPrefix(uri, "file://") {
		uri = "file:///" + strings.TrimPrefix(filepath.ToSlash(path), "/")
		// 逐段转义，保留 / 分隔符（url.PathEscape 会编码空格为 %20）。
		parts := strings.Split(strings.TrimPrefix(uri, "file:///"), "/")
		for i, p := range parts {
			parts[i] = url.PathEscape(p)
		}
		uri = "file:///" + strings.Join(parts, "/")
	}
	return map[string]any{"path": uri, "name": filepath.Base(path)}
}

func lineBreakpoints(lines []int) []map[string]any {
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		out = append(out, map[string]any{"line": l})
	}
	return out
}

// SetExceptionBreakpoints 设置异常断点过滤器（如 all/uncaught）。
func (s *Session) SetExceptionBreakpoints(ctx context.Context, filters []string) error {
	_, err := s.client.Call(ctx, "setExceptionBreakpoints", map[string]any{"filters": filters})
	return err
}

// Continue 恢复线程，返回 adapter 是否继续了所有线程。
func (s *Session) Continue(ctx context.Context, threadID int) (bool, error) {
	body, err := s.client.Call(ctx, "continue", map[string]any{"threadId": threadID})
	if err != nil {
		return false, err
	}
	var resp struct {
		AllThreadsContinued bool `json:"allThreadsContinued"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &resp)
	}
	return resp.AllThreadsContinued, nil
}

func (s *Session) Next(ctx context.Context, threadID int) error {
	_, err := s.client.Call(ctx, "next", map[string]any{"threadId": threadID})
	return err
}

func (s *Session) StepIn(ctx context.Context, threadID int) error {
	_, err := s.client.Call(ctx, "stepIn", map[string]any{"threadId": threadID})
	return err
}

func (s *Session) StepOut(ctx context.Context, threadID int) error {
	_, err := s.client.Call(ctx, "stepOut", map[string]any{"threadId": threadID})
	return err
}

func (s *Session) Pause(ctx context.Context, threadID int) error {
	_, err := s.client.Call(ctx, "pause", map[string]any{"threadId": threadID})
	return err
}

// Threads 返回线程列表。
func (s *Session) Threads(ctx context.Context) ([]Thread, error) {
	body, err := s.client.Call(ctx, "threads", map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Threads []Thread `json:"threads"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Threads, nil
}

// StackTrace 返回线程调用栈。
func (s *Session) StackTrace(ctx context.Context, threadID int) ([]StackFrame, error) {
	body, err := s.client.Call(ctx, "stackTrace", map[string]any{
		"threadId": threadID, "startFrame": 0, "levels": 50,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		StackFrames []StackFrame `json:"stackFrames"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	for i := range resp.StackFrames {
		resp.StackFrames[i].SourcePath = resp.StackFrames[i].Source.Path
	}
	return resp.StackFrames, nil
}

// Scopes 返回某帧的变量作用域。
func (s *Session) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	body, err := s.client.Call(ctx, "scopes", map[string]any{"frameId": frameID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Scopes []Scope `json:"scopes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Scopes, nil
}

// Variables 返回某变量引用下的变量列表。
func (s *Session) Variables(ctx context.Context, ref int) ([]Variable, error) {
	body, err := s.client.Call(ctx, "variables", map[string]any{"variablesReference": ref})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Variables []Variable `json:"variables"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Variables, nil
}

// Evaluate 在指定帧上下文求值表达式（DAP context=repl）。
func (s *Session) Evaluate(ctx context.Context, expr string, frameID int) (string, error) {
	args := map[string]any{"expression": expr, "context": "repl"}
	if frameID > 0 {
		args["frameId"] = frameID
	}
	body, err := s.client.Call(ctx, "evaluate", args)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	return resp.Result, nil
}

// WaitStopped 等待下一个 stopped 事件，最多 timeout。
func (s *Session) WaitStopped(ctx context.Context, timeout time.Duration) (*StoppedBody, error) {
	ch := make(chan *StoppedBody, 1)
	s.eventsMu.Lock()
	if len(s.stopQueue) > 0 {
		body := s.stopQueue[0]
		s.stopQueue = s.stopQueue[1:]
		s.eventsMu.Unlock()
		return body, nil
	}
	s.stopWaiters = append(s.stopWaiters, ch)
	s.eventsMu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case body := <-ch:
		return body, nil
	case <-timer.C:
		s.removeStopWaiter(ch)
		return nil, ErrWaitTimeout
	case <-ctx.Done():
		s.removeStopWaiter(ch)
		return nil, ctx.Err()
	}
}

func (s *Session) removeStopWaiter(ch chan *StoppedBody) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	for i, w := range s.stopWaiters {
		if w == ch {
			s.stopWaiters = append(s.stopWaiters[:i], s.stopWaiters[i+1:]...)
			return
		}
	}
}

// WaitTerminated 等待 terminated 或 exited 事件，最多 timeout。
func (s *Session) WaitTerminated(ctx context.Context, timeout time.Duration) error {
	ch := make(chan struct{})
	s.eventsMu.Lock()
	if s.termPending {
		s.eventsMu.Unlock()
		return nil
	}
	s.termWaiters = append(s.termWaiters, ch)
	s.eventsMu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-timer.C:
		s.removeTermWaiter(ch)
		return ErrWaitTimeout
	case <-ctx.Done():
		s.removeTermWaiter(ch)
		return ctx.Err()
	}
}

func (s *Session) removeTermWaiter(ch chan struct{}) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	for i, w := range s.termWaiters {
		if w == ch {
			s.termWaiters = append(s.termWaiters[:i], s.termWaiters[i+1:]...)
			return
		}
	}
}

// Events 暴露底层事件流。
func (s *Session) Events() <-chan Event { return s.client.Events() }
