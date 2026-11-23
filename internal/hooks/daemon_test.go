package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeTempHookDoc 把一个钩子文档写到临时目录并返回路径。
func writeTempHookDoc(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeDaemonState 记录伪守护进程收到的事件，供测试断言订阅过滤与降级行为。
type fakeDaemonState struct {
	mu       sync.Mutex
	received []string
	closed   bool
}

func (s *fakeDaemonState) events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

// newFakeDaemonConn 用内存管道模拟一个常驻进程：它解析 initialize/event 请求，
// 按 decide 回调给出结构化决策。noise 行会被写在每条响应之前，用于验证 stdout
// 上的杂项输出不会污染协议。
func newFakeDaemonConn(events []string, decide func(string) daemonDecision, noise []string) (*daemonConn, *fakeDaemonState) {
	toDaemonR, toDaemonW := io.Pipe()
	fromDaemonR, fromDaemonW := io.Pipe()
	state := &fakeDaemonState{}
	conn := &daemonConn{
		stdin:  toDaemonW,
		stdout: fromDaemonR,
		reader: bufio.NewReader(fromDaemonR),
		stderr: &limitedBuffer{},
	}
	go func() {
		defer func() {
			_ = fromDaemonW.Close()
			state.mu.Lock()
			state.closed = true
			state.mu.Unlock()
		}()
		scanner := bufio.NewScanner(toDaemonR)
		for scanner.Scan() {
			var req rpcRequest
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			for _, line := range noise {
				_, _ = fmt.Fprintln(fromDaemonW, line)
			}
			var result any
			switch req.Method {
			case "initialize":
				result = struct {
					Events []string `json:"events"`
				}{Events: events}
			case "event":
				var params map[string]any
				raw, _ := json.Marshal(req.Params)
				_ = json.Unmarshal(raw, &params)
				name, _ := params["event"].(string)
				state.mu.Lock()
				state.received = append(state.received, name)
				state.mu.Unlock()
				result = decide(name)
			}
			response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
			response.Result, _ = json.Marshal(result)
			line, _ := json.Marshal(response)
			_, _ = fmt.Fprintln(fromDaemonW, string(line))
		}
	}()
	return conn, state
}

// trustedDaemonDoc 是经过信任门的守护进程文档：标准 hooks 段用于复用解析语义，
// daemon 段声明启动命令与订阅的三类事件。
const trustedDaemonDoc = `{
  "hooks": {"PreToolUse": [], "PostToolUse": [], "UserPromptSubmit": []},
  "daemon": {"command": "fake-daemon", "events": ["PreToolUse", "PostToolUse", "UserPromptSubmit"]}
}`

func trustedDaemonTrust(path string) *TrustStore {
	trust := NewTrustStore()
	trust.Trust(path, Digest([]byte(trustedDaemonDoc)))
	return trust
}

func TestLoadDaemonSpecTrustGate(t *testing.T) {
	path := writeTempHookDoc(t, trustedDaemonDoc)
	trust := trustedDaemonTrust(path)

	spec, err := loadDaemonSpec(path, trust)
	if err != nil {
		t.Fatalf("授信文档应加载成功：%v", err)
	}
	if spec.command != "fake-daemon" {
		t.Fatalf("command = %q", spec.command)
	}
	for _, event := range []Event{PreToolUse, PostToolUse, UserPromptSubmit} {
		if !spec.events[event] {
			t.Fatalf("缺少订阅事件 %s", event)
		}
	}
	if spec.events[SessionEnd] {
		t.Fatal("未声明的事件不应被订阅")
	}

	// 未授信的同一份字节必须被拒绝，且不产生任何 spec。
	if _, err := loadDaemonSpec(path, NewTrustStore()); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("未授信文档应包装 ErrUntrusted，得到 %v", err)
	}
}

func TestLoadDaemonSpecFallsBackToDocumentEvents(t *testing.T) {
	// daemon.events 缺失时回退到 hooks 段出现的事件。
	doc := `{"hooks": {"PreToolUse": [{"command":"echo"}], "SessionEnd": [{"command":"echo"}]}, "daemon": {"command": "fake"}}`
	path := writeTempHookDoc(t, doc)
	trust := NewTrustStore()
	trust.Trust(path, Digest([]byte(doc)))
	spec, err := loadDaemonSpec(path, trust)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !spec.events[PreToolUse] || !spec.events[SessionEnd] || spec.events[PostToolUse] {
		t.Fatalf("回退事件集合不正确：%+v", spec.events)
	}
}

func TestAttachDaemonDisabledIsNoop(t *testing.T) {
	d := &Dispatcher{}
	if err := AttachDaemon(d, func(string) string { return "" }, NewTrustStore(), io.Discard); err != nil {
		t.Fatalf("未配置时应无错误：%v", err)
	}
	if d.daemon.Load() != nil {
		t.Fatal("未配置时不得挂载守护进程")
	}
	// 即使 daemon 段文档存在但 env 未设置，也不读文件、不启动进程。
	if err := attachDaemon(d, "", nil, io.Discard, func(*daemonSpec) (*daemonConn, error) {
		t.Fatal("未配置时不得调用启动器")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachDaemonUntrustedDoesNotLaunch(t *testing.T) {
	path := writeTempHookDoc(t, trustedDaemonDoc)
	d := &Dispatcher{}
	err := attachDaemon(d, path, NewTrustStore(), io.Discard, func(*daemonSpec) (*daemonConn, error) {
		t.Fatal("未授信文档不得启动外部进程")
		return nil, nil
	})
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("应返回 ErrUntrusted，得到 %v", err)
	}
	if d.daemon.Load() != nil {
		t.Fatal("失败后不得挂载")
	}
}

func TestDaemonOutcomeVerdictAndContext(t *testing.T) {
	cases := []struct {
		name        string
		event       Event
		decision    daemonDecision
		wantBlocked bool
		wantExit    int
		wantVerdict Verdict
		wantContext string
		wantMessage string
	}{
		{
			name:        "deny blocks pre-tool-use",
			event:       PreToolUse,
			decision:    daemonDecision{Verdict: "deny", Message: "policy says no", AdditionalContext: "note A"},
			wantBlocked: true,
			wantExit:    2,
			wantVerdict: VerdictDeny,
			wantContext: "note A",
			wantMessage: "policy says no",
		},
		{
			name:        "deny on notification is warn only",
			event:       Notification,
			decision:    daemonDecision{Verdict: "deny", Message: "heads up"},
			wantBlocked: false,
			wantExit:    2,
			wantVerdict: VerdictDeny, // 裁决解析与事件无关；applyVerdict 才限定 PreToolUse
		},
		{
			name:        "allow",
			event:       PreToolUse,
			decision:    daemonDecision{Verdict: "allow"},
			wantVerdict: VerdictAllow,
		},
		{
			name:        "unknown verdict is none",
			event:       PreToolUse,
			decision:    daemonDecision{Verdict: "maybe"},
			wantVerdict: VerdictNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := daemonOutcome(tc.event, tc.decision)
			if o.Blocked != tc.wantBlocked || o.ExitCode != tc.wantExit {
				t.Fatalf("blocked/exit = %t/%d, want %t/%d", o.Blocked, o.ExitCode, tc.wantBlocked, tc.wantExit)
			}
			if got := VerdictFromOutcome(o); got != tc.wantVerdict {
				t.Fatalf("verdict = %v, want %v", got, tc.wantVerdict)
			}
			if o.AdditionalContext != tc.wantContext {
				t.Fatalf("context = %q, want %q", o.AdditionalContext, tc.wantContext)
			}
			if tc.wantMessage != "" && o.detail() != tc.wantMessage {
				t.Fatalf("detail = %q, want %q (协议 JSON 不应泄漏)", o.detail(), tc.wantMessage)
			}
			if strings.Contains(o.detail(), "permissionDecision") {
				t.Fatalf("detail 泄漏了协议 JSON：%q", o.detail())
			}
		})
	}
}

func TestDaemonConnSkipsNoisyStdout(t *testing.T) {
	conn, _ := newFakeDaemonConn(nil, func(string) daemonDecision { return daemonDecision{} },
		[]string{"2026-01-01 log line", "not json {{{"})
	defer conn.close()
	result, err := conn.request(context.Background(), "initialize", struct{}{}, time.Second)
	if err != nil {
		t.Fatalf("应跳过杂项行并解析响应：%v", err)
	}
	if len(result) == 0 {
		t.Fatal("应返回非空 result")
	}
}

func TestDaemonDispatchEndToEnd(t *testing.T) {
	conn, state := newFakeDaemonConn(nil, func(event string) daemonDecision {
		switch event {
		case string(PreToolUse):
			return daemonDecision{Verdict: "deny", Message: "daemon blocked", AdditionalContext: "extra note"}
		default:
			return daemonDecision{Message: "seen " + event}
		}
	}, nil)
	var warn strings.Builder
	path := writeTempHookDoc(t, trustedDaemonDoc)
	trust := trustedDaemonTrust(path)
	d := &Dispatcher{}
	if err := attachDaemon(d, path, trust, &warn, func(*daemonSpec) (*daemonConn, error) {
		return conn, nil
	}); err != nil {
		t.Fatalf("挂载失败：%v", err)
	}
	defer StopDaemon(d)

	// 一次会话分发三类事件，常驻进程只启动一次且全部收到。
	for _, event := range []Event{PreToolUse, PostToolUse, UserPromptSubmit} {
		result, err := d.Dispatch(context.Background(), &Config{}, Payload{Event: string(event), Tool: "bash"})
		if err != nil {
			t.Fatalf("%s 分发失败：%v", event, err)
		}
		if event == PreToolUse {
			if !result.Blocked {
				t.Fatal("deny 裁决应阻断工具")
			}
			if !strings.Contains(result.Message, "daemon blocked") {
				t.Fatalf("拦截理由应含可读消息，得到 %q", result.Message)
			}
			if len(result.AdditionalContexts) != 1 || result.AdditionalContexts[0] != "extra note" {
				t.Fatalf("附加上下文未传播：%+v", result.AdditionalContexts)
			}
		}
	}
	got := state.events()
	if len(got) != 3 {
		t.Fatalf("应收到 3 类事件，得到 %v", got)
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit"} {
		found := false
		for _, name := range got {
			if name == event {
				found = true
			}
		}
		if !found {
			t.Fatalf("未收到事件 %s：%v", event, got)
		}
	}
	if warn.Len() != 0 {
		t.Fatalf("正常路径不应告警：%q", warn.String())
	}
}

func TestDaemonUnsubscribedEventSkipsProcess(t *testing.T) {
	conn, state := newFakeDaemonConn(nil, func(string) daemonDecision { return daemonDecision{} }, nil)
	d := &Dispatcher{}
	path := writeTempHookDoc(t, trustedDaemonDoc)
	if err := attachDaemon(d, path, trustedDaemonTrust(path), io.Discard, func(*daemonSpec) (*daemonConn, error) {
		return conn, nil
	}); err != nil {
		t.Fatal(err)
	}
	defer StopDaemon(d)

	if _, err := d.Dispatch(context.Background(), &Config{}, Payload{Event: string(SessionEnd)}); err != nil {
		t.Fatal(err)
	}
	if len(state.events()) != 0 {
		t.Fatalf("未订阅事件不应发给守护进程：%v", state.events())
	}
}

func TestDaemonCrashDegradesToMainLoop(t *testing.T) {
	// 伪进程在第一次 event 时直接关闭 stdout，制造 EOF 崩溃。
	toDaemonR, toDaemonW := io.Pipe()
	fromDaemonR, fromDaemonW := io.Pipe()
	conn := &daemonConn{
		stdin:  toDaemonW,
		stdout: fromDaemonR,
		reader: bufio.NewReader(fromDaemonR),
		stderr: &limitedBuffer{},
	}
	go func() {
		scanner := bufio.NewScanner(toDaemonR)
		for scanner.Scan() {
			var req rpcRequest
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.Method == "initialize" {
				response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
				response.Result, _ = json.Marshal(struct{}{})
				line, _ := json.Marshal(response)
				_, _ = fmt.Fprintln(fromDaemonW, string(line))
				continue
			}
			_ = fromDaemonW.Close() // 崩溃
			return
		}
	}()
	var warn strings.Builder
	d := &Dispatcher{}
	path := writeTempHookDoc(t, trustedDaemonDoc)
	if err := attachDaemon(d, path, trustedDaemonTrust(path), &warn, func(*daemonSpec) (*daemonConn, error) {
		return conn, nil
	}); err != nil {
		t.Fatal(err)
	}
	defer StopDaemon(d)

	for i := 0; i < 2; i++ {
		result, err := d.Dispatch(context.Background(), &Config{}, Payload{Event: string(PreToolUse)})
		if err != nil {
			t.Fatalf("扩展崩溃不得让主循环失败：%v", err)
		}
		if result.Blocked {
			t.Fatal("降级后不得继续拦截工具")
		}
	}
	if !strings.Contains(warn.String(), "degraded") {
		t.Fatalf("降级应写告警：%q", warn.String())
	}
}

func TestAttachDaemonStartsOnce(t *testing.T) {
	path := writeTempHookDoc(t, trustedDaemonDoc)
	starts := 0
	launch := func(*daemonSpec) (*daemonConn, error) {
		starts++
		conn, _ := newFakeDaemonConn(nil, func(string) daemonDecision { return daemonDecision{} }, nil)
		return conn, nil
	}
	d := &Dispatcher{}
	for i := 0; i < 3; i++ {
		if err := attachDaemon(d, path, trustedDaemonTrust(path), io.Discard, launch); err != nil {
			t.Fatal(err)
		}
	}
	if starts != 1 {
		t.Fatalf("一次会话只应启动一次，实际 %d 次", starts)
	}
	StopDaemon(d)
}

func TestDefaultDispatchUnchanged(t *testing.T) {
	// opt-in 关闭时，任意事件的聚合结果与未引入守护进程的实现一致。
	runner := func(_ context.Context, command Command, _ Payload) Outcome {
		return Outcome{Event: command.Event, Command: command.Command, ExitCode: 0, Stdout: `{"additionalContext":"ctx"}`}
	}
	config := &Config{Commands: []Command{{Event: PreToolUse, Command: "echo", OnError: "block"}}}
	base := &Dispatcher{Runner: runner}
	withEnv := &Dispatcher{Runner: runner}
	if err := AttachDaemon(withEnv, func(string) string { return "" }, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	want, err := base.Dispatch(context.Background(), config, Payload{Event: string(PreToolUse)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := withEnv.Dispatch(context.Background(), config, Payload{Event: string(PreToolUse)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Blocked != want.Blocked || got.Message != want.Message || len(got.AdditionalContexts) != len(want.AdditionalContexts) {
		t.Fatalf("默认路径发生改变：got %+v want %+v", got, want)
	}
}
