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

// 本文件在 Client（帧层）之上提供面向调试会话的高层 API。
// 覆盖 omp 28 个 DAP 操作中的核心子集：握手、断点、执行控制、状态查询与生命周期。
// 每个方法把参数装配成 map[string]any 交给 Client.Call，再把 response body 解析为
// 稳定的 Go 结构，调用方无需接触线上 JSON 的具体形状。

// Breakpoint 是 setBreakpoints 返回的一个断点状态。
type Breakpoint struct {
	ID       int
	Verified bool
	Line     int
}

// Thread 是一条执行线程（threads 响应）。
type Thread struct {
	ID   int
	Name string
}

// StackFrame 是调用栈中的一帧（stackTrace 响应）。
type StackFrame struct {
	ID         int
	Name       string
	SourcePath string
	Line       int
}

// Scope 是一个变量作用域（scopes 响应）。
type Scope struct {
	Name               string
	VariablesReference int
}

// Variable 是一个变量（variables 响应）；VariablesReference 非 0 表示可继续展开。
type Variable struct {
	Name               string
	Value              string
	VariablesReference int
}

// ErrWaitTimeout 表示等待 stopped/terminated 等事件超时，可用 errors.Is 匹配。
var ErrWaitTimeout = errors.New("dap: timed out waiting for event")

// Session 包装一条 Client 连接，提供序列化的调试会话操作。
type Session struct {
	client *Client

	// 事件通道由多个 Wait* 方法共享；暂存不匹配当前等待者的事件，避免丢弃
	// 后来才被另一个 Wait* 需要的 stopped/terminated 事件。
	eventsMu      sync.Mutex
	pendingEvents []Event

	supportsConfigurationDone bool
}

// New 用一条已建立的 Client 连接创建高层会话。
func New(c *Client) *Session {
	return &Session{client: c}
}

// SupportsConfigurationDone 报告 adapter 在 initialize 响应中是否声明需要
// configurationDone 握手（来自 capabilities.supportsConfigurationDoneRequest）。
func (s *Session) SupportsConfigurationDone() bool {
	return s.supportsConfigurationDone
}

// Initialize 发送 initialize 并记住 adapter 能力，是后续 launch/attach 的前置握手。
// clientID 为空时使用默认值 "argentgo"。
func (s *Session) Initialize(ctx context.Context, adapterID, clientID string) error {
	if clientID == "" {
		clientID = "argentgo"
	}
	args := map[string]any{
		"clientID":                     clientID,
		"clientName":                   clientID,
		"adapterID":                    adapterID,
		"pathFormat":                   "path",
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"supportsVariableType":         true,
		"supportsVariablePaging":       true,
		"supportsRunInTerminalRequest": true,
		"supportsInvalidatedEvent":     true,
	}
	body, err := s.client.Call(ctx, "initialize", args)
	if err != nil {
		return err
	}
	var caps struct {
		SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &caps) // 能力解析失败不影响握手本身
	}
	s.supportsConfigurationDone = caps.SupportsConfigurationDoneRequest
	return nil
}

// Launch 请求 adapter 启动并调试指定程序；args 是传给被调试程序的命令行参数。
func (s *Session) Launch(ctx context.Context, program string, args []string) error {
	launchArgs := map[string]any{"program": program}
	if len(args) > 0 {
		launchArgs["args"] = args
	}
	_, err := s.client.Call(ctx, "launch", launchArgs)
	return err
}

// Attach 连接到一个已运行的调试目标；参数由调用方按 adapter 要求填写
// （例如 connect、processId、request 等），按 DAP 规范原样透传。
func (s *Session) Attach(ctx context.Context, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	_, err := s.client.Call(ctx, "attach", args)
	return err
}

// ConfigurationDone 通知 adapter 断点等初始配置已下发完毕。
// 许多 adapter 会等到这条请求后才回应 launch/attach。
func (s *Session) ConfigurationDone(ctx context.Context) error {
	_, err := s.client.Call(ctx, "configurationDone", map[string]any{})
	return err
}

// pathToFileURI 把本地路径转成稳定的 file URI。filepath.Abs/ToSlash 统一成绝对路径
// 和正斜杠；Windows 盘符（D:/x）补前导斜杠以得到规范的 file:///D:/x。
// 每段单独 url.PathEscape 而非整体转义，这样 '/' 分隔符保持原样，空格/中文等被安全编码。
func pathToFileURI(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("dap: resolve path %q: %w", path, err)
	}
	slash := filepath.ToSlash(abs)
	if len(slash) > 1 && slash[1] == ':' {
		slash = "/" + slash
	}
	parts := strings.Split(slash, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "file://" + strings.Join(parts, "/"), nil
}

// breakpointWire 是 setBreakpoints 响应中单个断点的线上形状。
type breakpointWire struct {
	ID       int  `json:"id"`
	Verified bool `json:"verified"`
	Line     int  `json:"line"`
}

// SetBreakpoints 用给定行号整体替换 path 文件的断点集（DAP 语义是全量设置）。
// path 会先转为 file URI；返回的断点按 adapter 确认的顺序原样映射。
func (s *Session) SetBreakpoints(ctx context.Context, path string, lines []int) ([]Breakpoint, error) {
	uri, err := pathToFileURI(path)
	if err != nil {
		return nil, err
	}
	bps := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		bps = append(bps, map[string]any{"line": line})
	}
	args := map[string]any{
		"source": map[string]any{
			"path": uri,
			"name": filepath.Base(path),
		},
		"breakpoints": bps,
	}
	body, err := s.client.Call(ctx, "setBreakpoints", args)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Breakpoints []breakpointWire `json:"breakpoints"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("dap: decode setBreakpoints: %w", err)
		}
	}
	out := make([]Breakpoint, 0, len(payload.Breakpoints))
	for _, bp := range payload.Breakpoints {
		out = append(out, Breakpoint{ID: bp.ID, Verified: bp.Verified, Line: bp.Line})
	}
	return out, nil
}

// SetExceptionBreakpoints 设置异常断点过滤器（如 "all"、"uncaught"）。
func (s *Session) SetExceptionBreakpoints(ctx context.Context, filters []string) error {
	if filters == nil {
		filters = []string{}
	}
	_, err := s.client.Call(ctx, "setExceptionBreakpoints", map[string]any{"filters": filters})
	return err
}

// Continue 继续指定线程；返回值对应响应体中的 allThreadsContinued。
func (s *Session) Continue(ctx context.Context, threadID int) (bool, error) {
	body, err := s.client.Call(ctx, "continue", map[string]any{"threadId": threadID})
	if err != nil {
		return false, err
	}
	var payload struct {
		AllThreadsContinued bool `json:"allThreadsContinued"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	return payload.AllThreadsContinued, nil
}

// step 是 next/stepIn/stepOut/pause 的公共实现：它们都只带 threadId。
func (s *Session) step(ctx context.Context, command string, threadID int) error {
	_, err := s.client.Call(ctx, command, map[string]any{"threadId": threadID})
	return err
}

// Next 单步跳过（step over）。
func (s *Session) Next(ctx context.Context, threadID int) error { return s.step(ctx, "next", threadID) }

// StepIn 单步进入（step into）。
func (s *Session) StepIn(ctx context.Context, threadID int) error {
	return s.step(ctx, "stepIn", threadID)
}

// StepOut 单步跳出（step out）。
func (s *Session) StepOut(ctx context.Context, threadID int) error {
	return s.step(ctx, "stepOut", threadID)
}

// Pause 暂停指定线程。
func (s *Session) Pause(ctx context.Context, threadID int) error {
	return s.step(ctx, "pause", threadID)
}

// Threads 列出当前所有执行线程。
func (s *Session) Threads(ctx context.Context) ([]Thread, error) {
	body, err := s.client.Call(ctx, "threads", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Threads []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"threads"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("dap: decode threads: %w", err)
		}
	}
	out := make([]Thread, 0, len(payload.Threads))
	for _, t := range payload.Threads {
		out = append(out, Thread{ID: t.ID, Name: t.Name})
	}
	return out, nil
}

// StackTrace 返回指定线程的调用栈，SourcePath 取自 source.path（缺失时回退到 source.name）。
func (s *Session) StackTrace(ctx context.Context, threadID int) ([]StackFrame, error) {
	body, err := s.client.Call(ctx, "stackTrace", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	var payload struct {
		StackFrames []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Source *struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"source"`
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("dap: decode stackTrace: %w", err)
		}
	}
	out := make([]StackFrame, 0, len(payload.StackFrames))
	for _, f := range payload.StackFrames {
		frame := StackFrame{ID: f.ID, Name: f.Name, Line: f.Line}
		if f.Source != nil {
			frame.SourcePath = f.Source.Path
			if frame.SourcePath == "" {
				frame.SourcePath = f.Source.Name
			}
		}
		out = append(out, frame)
	}
	return out, nil
}

// Scopes 返回指定栈帧中的变量作用域。
func (s *Session) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	body, err := s.client.Call(ctx, "scopes", map[string]any{"frameId": frameID})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Scopes []struct {
			Name               string `json:"name"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"scopes"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("dap: decode scopes: %w", err)
		}
	}
	out := make([]Scope, 0, len(payload.Scopes))
	for _, sc := range payload.Scopes {
		out = append(out, Scope{Name: sc.Name, VariablesReference: sc.VariablesReference})
	}
	return out, nil
}

// Variables 展开一个 variablesReference 下的变量列表。
func (s *Session) Variables(ctx context.Context, ref int) ([]Variable, error) {
	body, err := s.client.Call(ctx, "variables", map[string]any{"variablesReference": ref})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Variables []struct {
			Name               string `json:"name"`
			Value              string `json:"value"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"variables"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("dap: decode variables: %w", err)
		}
	}
	out := make([]Variable, 0, len(payload.Variables))
	for _, v := range payload.Variables {
		out = append(out, Variable{Name: v.Name, Value: v.Value, VariablesReference: v.VariablesReference})
	}
	return out, nil
}

// Evaluate 在指定栈帧上下文中求值表达式，context 固定为 "repl"。
func (s *Session) Evaluate(ctx context.Context, expression string, frameID int) (string, error) {
	body, err := s.client.Call(ctx, "evaluate", map[string]any{
		"expression": expression,
		"context":    "repl",
		"frameId":    frameID,
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Result string `json:"result"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", fmt.Errorf("dap: decode evaluate: %w", err)
		}
	}
	return payload.Result, nil
}

// Disconnect 请求断开会话，并带 terminateDebuggee=true 终止被调试进程。
func (s *Session) Disconnect(ctx context.Context) error {
	_, err := s.client.Call(ctx, "disconnect", map[string]any{"terminateDebuggee": true})
	return err
}

// eventIn 判断事件名是否属于目标集合。
func eventIn(name string, names []string) bool {
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}

// waitEvent 从 Client 的事件流中等待指定名字之一的事件。先扫描之前暂存的事件，
// 再读取通道；不匹配当前等待者的事件会被暂存，供其他 Wait* 方法后续取用。
// 这些方法按设计供顺序调用；多个 Wait* 并发等待不同事件时可能互相暂存而超时。
func (s *Session) waitEvent(ctx context.Context, timeout time.Duration, names ...string) (Event, error) {
	s.eventsMu.Lock()
	for i, ev := range s.pendingEvents {
		if eventIn(ev.Event, names) {
			s.pendingEvents = append(s.pendingEvents[:i], s.pendingEvents[i+1:]...)
			s.eventsMu.Unlock()
			return ev, nil
		}
	}
	s.eventsMu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-s.client.Events():
			if !ok {
				return Event{}, errors.New("dap: event channel closed")
			}
			if eventIn(ev.Event, names) {
				return ev, nil
			}
			s.eventsMu.Lock()
			s.pendingEvents = append(s.pendingEvents, ev)
			s.eventsMu.Unlock()
		case <-timer.C:
			return Event{}, fmt.Errorf("%w: %s", ErrWaitTimeout, strings.Join(names, "/"))
		case <-ctx.Done():
			return Event{}, ctx.Err()
		}
	}
}

// WaitStopped 等待 stopped 事件并解析其载荷；超时或通道关闭时返回 nil 与错误。
func (s *Session) WaitStopped(ctx context.Context, timeout time.Duration) (*StoppedBody, error) {
	ev, err := s.waitEvent(ctx, timeout, "stopped")
	if err != nil {
		return nil, err
	}
	var body StoppedBody
	if len(ev.Body) > 0 {
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			return nil, fmt.Errorf("dap: decode stopped body: %w", err)
		}
	}
	return &body, nil
}

// WaitTerminated 等待 terminated 或 exited 事件，表示被调试目标已经结束。
func (s *Session) WaitTerminated(ctx context.Context, timeout time.Duration) error {
	_, err := s.waitEvent(ctx, timeout, "terminated", "exited")
	return err
}
