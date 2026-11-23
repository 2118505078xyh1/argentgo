package hooks

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// 本文件实现「事件钩子常驻外部进程扩展」（对标 pi-mono ExtensionAPI）。
//
// 与每条事件派生一个 shell 的既有路径不同，常驻进程在一次会话中只启动一次，
// 通过 stdio 上的换行分隔 JSON-RPC 2.0 与 hook 系统通信。它的启动文档必须与
// 普通钩子文档一样通过 SHA-256 信任门（Digest + needsTrustGate + ParseConfig），
// 不提供任何绕过安全门的旁路；信任失败时整个扩展不接线（fail-closed）。
//
// 文件的其余部分默认完全惰性：DaemonEnv 未设置时 AttachDaemon 是一个 no-op，
// Dispatcher 的 dispatchBase 只多做一次 nil 指针判断，既有行为逐字节不变。

const (
	// DaemonEnv 是常驻外部进程扩展的 opt-in 开关。值必须是受信任钩子文档的路径；
	// 未设置或空白表示关闭，此时本文件的所有逻辑都不进入执行路径。
	DaemonEnv = "ARGENT_HOOK_DAEMON"

	// daemonHandshakeTimeout 限制 initialize 握手的等待时间。
	daemonHandshakeTimeout = 5 * time.Second
	// daemonEventTimeout 限制单条 event 请求的等待时间。超时视为扩展故障，
	// 只降级为“无扩展”而不阻断主循环（warn-only）。
	daemonEventTimeout = 10 * time.Second
	// maxDaemonLogLines 限制从 stdout 跳过的非协议行数，避免守护进程的日志刷屏
	// 让读取陷入无限循环。
	maxDaemonLogLines = 1024
	// maxDaemonLineBytes 限制单行 JSON-RPC 的大小，防止异常进程耗尽内存。
	maxDaemonLineBytes = 1 << 20
	// daemonCommandLabel 是守护进程产生的 Outcome 的 Command 标签（仅用于诊断）。
	daemonCommandLabel = "daemon"
)

// rawDaemonDocument 在标准钩子文档之上增加一个 daemon 配置段。
// "hooks" 对象仍由 ParseConfig 解析，因此同一份字节同时满足现有的信任与
// 文档校验语义；"daemon" 只是附加的启动说明。
type rawDaemonDocument struct {
	Hooks  json.RawMessage `json:"hooks"`
	Daemon *rawDaemonSpec  `json:"daemon"`
}

// rawDaemonSpec 描述如何启动常驻进程以及它声明订阅的事件。
type rawDaemonSpec struct {
	Command string   `json:"command"`
	Events  []string `json:"events"`
}

// daemonSpec 是校验后的守护进程启动参数。
type daemonSpec struct {
	command string
	events  map[Event]bool
}

// daemonLauncher 启动一个常驻进程并建立 JSON-RPC 连接。它是变量注入点，
// 使测试能在不派生真实进程的情况下驱动整条握手/分发/降级路径。
type daemonLauncher func(spec *daemonSpec) (*daemonConn, error)

// loadDaemonSpec 从路径读取守护进程文档并做信任校验。
//
// 顺序与 LoadDocument 完全一致：读字节 → Digest → needsTrustGate → ParseConfig，
// 因此未授信的文档会在解析出任何命令之前以包装 ErrUntrusted 的错误被拒绝，
// 绝不启动外部进程。订阅事件来自 daemon.events；为空时回退到文档 "hooks"
// 段出现的事件，使普通钩子文档也能直接充当守护进程文档。
func loadDaemonSpec(path string, trust *TrustStore) (*daemonSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hooks: read daemon document %s: %w", path, err)
	}
	digest := Digest(data)
	if !needsTrustGate(path, digest, trust) {
		return nil, fmt.Errorf("%s: %w", path, ErrUntrusted)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw rawDaemonDocument
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("hooks: invalid daemon document %s: %w", path, err)
	}
	if raw.Daemon == nil || strings.TrimSpace(raw.Daemon.Command) == "" {
		return nil, fmt.Errorf("hooks: daemon document %s has no command", path)
	}
	events := make(map[Event]bool)
	for _, name := range raw.Daemon.Events {
		if event, ok := ParseEvent(strings.TrimSpace(name)); ok {
			events[event] = true
		}
	}
	if len(events) == 0 {
		for _, command := range config.Commands {
			events[command.Event] = true
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("hooks: daemon document %s declares no events", path)
	}
	return &daemonSpec{command: strings.TrimSpace(raw.Daemon.Command), events: events}, nil
}

// rpcRequest / rpcResponse 是换行分隔 JSON-RPC 2.0 的最小消息形态。
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponseError struct {
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int64             `json:"id"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *rpcResponseError `json:"error,omitempty"`
}

// daemonConn 封装一个常驻进程的 stdio 通道。所有请求共用一把互斥锁：
// 事件可能从多个 hook 调用点并发到达，但一个长驻进程的 stdin/stdout 是单一
// 有序流，写请求与读响应必须成对串行，否则响应会串线。
type daemonConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *bufio.Reader
	stderr *limitedBuffer
	mu     sync.Mutex
	nextID int64
}

// request 发送一个 JSON-RPC 请求并等待同 id 的响应。非协议行（守护进程误写到
// stdout 的日志）与 id 不匹配的行会被跳过，且跳过数量有上限：守护进程的裸
// stdout 永远不会作为 hook 结果或模型上下文被转发，因此不会污染协议。
func (c *daemonConn) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	id := atomic.AddInt64(&c.nextID, 1)
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	setter, _ := c.stdout.(interface{ SetReadDeadline(time.Time) error })
	if setter != nil {
		if err := setter.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer func() { _ = setter.SetReadDeadline(time.Time{}) }()
	}
	if _, err := c.stdin.Write(body); err != nil {
		return nil, fmt.Errorf("hooks: daemon write %s: %w", method, err)
	}
	for skipped := 0; skipped < maxDaemonLogLines; skipped++ {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("hooks: daemon read %s: %w", method, err)
		}
		if len(line) > maxDaemonLineBytes {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
			continue
		}
		if response.ID != id || response.JSONRPC != "2.0" {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("hooks: daemon %s: %s", method, response.Error.Message)
		}
		return response.Result, nil
	}
	return nil, fmt.Errorf("hooks: daemon %s: too many non-protocol stdout lines", method)
}

// close 终止守护进程并回收。它只由 AttachDaemon 的调用方在会话结束时或
// 首次检测到崩溃时调用，绝不出现在每条事件的热路径上。
func (c *daemonConn) close() {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	go proc.KillGroup(c.cmd.Process)
	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitDelay):
	}
}

// startDaemonProcess 按 spec 启动常驻进程并完成 initialize 握手。它是生产用的
// daemonLauncher；握手失败时进程会被立即回收，不留下孤儿。
func startDaemonProcess(spec *daemonSpec) (conn *daemonConn, err error) {
	name, args := shellCommand(spec.command)
	cmd := exec.Command(name, args...)
	// 与既有 shell hook 一致：放进独立进程组，使超时/关闭能终止整棵子树。
	proc.ConfigureGroup(cmd)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "ARGENT_HOOK_DAEMON_MODE=1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &limitedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	conn = &daemonConn{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		reader: bufio.NewReader(stdout),
		stderr: stderr,
	}
	names := make([]string, 0, len(spec.events))
	for event := range spec.events {
		names = append(names, string(event))
	}
	// 握手只用于确认对端讲同一套协议；订阅集合以受信任文档为准，防止 daemon
	// 通过握手响应擅自扩大它能接收的事件范围。
	if _, err := conn.request(context.Background(), "initialize", struct {
		Events []string `json:"events"`
	}{Events: names}, daemonHandshakeTimeout); err != nil {
		conn.close()
		return nil, err
	}
	return conn, nil
}

// daemonProcess 是一个已握手的常驻扩展实例。崩溃后 dead 置位，此后所有事件
// 直接走原有主循环；故障通过 warn 写一条有界提示（warn-only，不阻断）。
type daemonProcess struct {
	conn   *daemonConn
	events map[Event]bool
	warn   io.Writer
	dead   atomic.Bool
}

// subscribes 报告该事件是否应由常驻进程处理。已降级的实例恒为 false。
func (p *daemonProcess) subscribes(event Event) bool {
	return p != nil && !p.dead.Load() && p.events[event]
}

// degrade 记录一次故障并永久关闭扩展；它让主循环继续，而不是把扩展的崩溃
// 变成一次工具拦截。warn 为 nil 时提示为空操作。
func (p *daemonProcess) degrade(err error) {
	if p.dead.Swap(true) {
		return
	}
	if p.warn != nil {
		fmt.Fprintf(p.warn, "hook daemon degraded: %v\n", err)
	}
	p.conn.close()
}

// daemonDecision 是守护进程 event 响应中的结构化决策。
type daemonDecision struct {
	Verdict           string `json:"verdict"`
	AdditionalContext string `json:"additionalContext"`
	Message           string `json:"message"`
}

// dispatch 向常驻进程发送一次事件通知，并把结构化响应投影成既有 Outcome。
// 第二个返回值为 false 表示未处理（未订阅、已降级或协议故障），调用方应
// 保持原有分发结果不变。
func (p *daemonProcess) dispatch(ctx context.Context, payload Payload) (Outcome, bool) {
	event := Event(payload.Event)
	if !p.subscribes(event) {
		return Outcome{}, false
	}
	result, err := p.conn.request(ctx, "event", payload, daemonEventTimeout)
	if err != nil {
		p.degrade(err)
		return Outcome{}, false
	}
	var decision daemonDecision
	if err := json.Unmarshal(result, &decision); err != nil {
		p.degrade(fmt.Errorf("invalid daemon decision: %w", err))
		return Outcome{}, false
	}
	return daemonOutcome(event, decision), true
}

// daemonOutcome 把守护进程决策投影成与 shell hook 等价的 Outcome：
// 结构化协议 JSON 放进 Stdout，使 VerdictFromOutcome、AdditionalContextFromOutcome
// 与 applyVerdict 全部复用既有解析，不另开一套裁决/注入语义。人类可读的
// Message 走 daemonMessage，由 Outcome.detail 优先选用，避免协议 JSON 泄漏到
// 用户看到的拦截理由里。
func daemonOutcome(event Event, decision daemonDecision) Outcome {
	outcome := Outcome{Event: event, Command: daemonCommandLabel, ExitCode: 0}
	object := map[string]any{}
	switch strings.ToLower(strings.TrimSpace(decision.Verdict)) {
	case "deny":
		object["permissionDecision"] = "deny"
		// 与 shell hook 的 exit 2 对齐：PreToolUse 拦截，其余事件为警告。
		outcome.ExitCode = 2
		if event == PreToolUse {
			outcome.Blocked = true
		}
	case "allow":
		object["permissionDecision"] = "allow"
	}
	if decision.AdditionalContext != "" {
		object["additionalContext"] = decision.AdditionalContext
	}
	if body, err := json.Marshal(object); err == nil {
		outcome.Stdout = string(body)
	}
	message := strings.TrimSpace(decision.Message)
	if message == "" {
		// 即使守护进程未给理由也不让协议 JSON 出现在可读理由中。
		message = "hook daemon response"
		if outcome.Blocked {
			message = "hook daemon denied the call"
		}
	}
	outcome.daemonMessage = message
	outcome.AdditionalContext = AdditionalContextFromOutcome(outcome)
	return outcome
}

// attachDaemon 是 AttachDaemon 的可注入内核：校验 opt-in、加载受信任文档、
// 启动常驻进程，并把它挂到 dispatcher 上。launch 为 nil 时使用真实启动器。
func attachDaemon(d *Dispatcher, value string, trust *TrustStore, warn io.Writer, launch daemonLauncher) error {
	if strings.TrimSpace(value) == "" {
		return nil // 严格 opt-in：未配置时完全不触碰 dispatcher
	}
	if d == nil {
		return errors.New("hooks: attach daemon to nil dispatcher")
	}
	if d.daemon.Load() != nil {
		return nil // 一次会话只启动一次
	}
	spec, err := loadDaemonSpec(strings.TrimSpace(value), trust)
	if err != nil {
		return err
	}
	if launch == nil {
		launch = startDaemonProcess
	}
	conn, err := launch(spec)
	if err != nil {
		return err
	}
	d.daemon.Store(&daemonProcess{conn: conn, events: spec.events, warn: warn})
	return nil
}

// AttachDaemon 把常驻外部进程扩展挂到 dispatcher 上，供调用方（internal/app）
// 在构造钩子桥接时以 opt-in 方式调用。getenv 由调用方注入（通常传 os.Getenv），
// 使激活状态可在测试中验证而不污染进程全局。
//
// 默认（DaemonEnv 未设置）时本函数是纯 no-op，不读文件、不派生进程、不改动
// dispatcher，因此既有行为逐字节不变。设置后文档必须经过 SHA-256 信任门；
// 未授信、格式错误或启动失败都返回错误且不留后台进程，调用方只需按既有
// noteHook 风格告警即可，主循环无需改动。
//
// 调用方应在一次会话中只调用一次（钩子桥接构造处），并在会话结束时调用
// StopDaemon 回收进程。
func AttachDaemon(d *Dispatcher, getenv func(string) string, trust *TrustStore, warn io.Writer) error {
	if getenv == nil {
		return nil
	}
	return attachDaemon(d, getenv(DaemonEnv), trust, warn, startDaemonProcess)
}

// StopDaemon 终止已挂载的常驻扩展并清空引用；未挂载或已经降级时为 no-op。
// 它与 AttachDaemon 成对使用，供会话结束时回收子进程。
func StopDaemon(d *Dispatcher) {
	if d == nil {
		return
	}
	daemon := d.daemon.Swap(nil)
	if daemon == nil {
		return
	}
	daemon.dead.Store(true)
	daemon.conn.close()
}
