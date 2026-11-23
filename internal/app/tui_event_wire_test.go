package app

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventToolStart 证明 tool_start 保留了 TUI 提升待审批卡片和渲染运行中任务行所需的字段。
// （接上行）
func TestAgentToTUIEventToolStart(t *testing.T) {
	in := agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call-7",
		ToolName:   "bash",
		Args:       `{"command":"ls -la"}`,
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("tool_start must have a TUI mapping")
	}
	want := tui.Event{
		Kind:       tui.EventToolStart,
		ToolCallID: "call-7",
		ToolName:   "bash",
		Args:       `{"command":"ls -la"}`,
	}
	if got != want {
		t.Fatalf("mapped event = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventToolEnd 证明 tool_end 携带 TUI 关闭任务行并标记失败工具条目所需的结果字段。
// （接上行）
func TestAgentToTUIEventToolEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
	}{
		{name: "ok", isError: false},
		{name: "error", isError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := agent.Event{
				Kind:       agent.EventToolEnd,
				ToolCallID: "call-9",
				ToolName:   "write",
				Args:       `{"path":"a.go"}`,
				Output:     "done",
				IsError:    tc.isError,
				DurationMS: 42,
			}
			got, ok := AgentToTUIEvent(in)
			if !ok {
				t.Fatal("tool_end must have a TUI mapping")
			}
			want := tui.Event{
				Kind:       tui.EventToolEnd,
				ToolCallID: "call-9",
				ToolName:   "write",
				Args:       `{"path":"a.go"}`,
				Output:     "done",
				IsError:    tc.isError,
				DurationMS: 42,
			}
			if got != want {
				t.Fatalf("mapped event = %+v, want %+v", got, want)
			}
		})
	}
}

// TestAgentToTUIEventError 证明失败的请求通过 tui.EventError 的 Detail 字段呈现其脱敏错误文本。
// （接上行）
func TestAgentToTUIEventError(t *testing.T) {
	got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventError, Err: "stream reset"})
	if !ok {
		t.Fatal("error must have a TUI mapping")
	}
	want := tui.Event{Kind: tui.EventError, Detail: "stream reset"}
	if got != want {
		t.Fatalf("mapped event = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventEveryKindRegistered 穷举所有引擎事件种类：每个都必须有
// TUI 映射期望（含"有意不映射"）。新增 EventKind 但没在 tuiMappingExpectations
// 登记时，本测试失败，而不是让事件静默穿过桥接器。
func TestAgentToTUIEventEveryKindRegistered(t *testing.T) {
	kinds := discoverAppEventKinds(t)
	if len(kinds) == 0 {
		t.Fatal("没有扫描到 EventKind 常量，穷举守卫失效")
	}
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		seen[kind] = true
		want, ok := tuiMappingExpectations[kind]
		if !ok {
			t.Errorf("事件 %q 未登记 TUI 映射期望：请补 tuiMappingExpectations", kind)
			continue
		}
		got, mapped := AgentToTUIEvent(want.in)
		if mapped != want.mapped || got != want.want {
			t.Errorf("事件 %q 映射 = (%+v, %v), want (%+v, %v)", kind, got, mapped, want.want, want.mapped)
		}
	}
	for kind := range tuiMappingExpectations {
		if !seen[kind] {
			t.Errorf("tuiMappingExpectations 登记了已不存在的事件 %q", kind)
		}
	}
}

// tuiMappingExpectation 描述一种事件在 TUI 桥接器上的预期行为。
// mapped=false 且 want 为零值表示"有意不映射"，而不是漏映射。
type tuiMappingExpectation struct {
	in     agent.Event
	mapped bool
	want   tui.Event
}

// tuiMappingExpectations 是 TUI 消费层的登记清单。新增事件时必须在此补一行，
// 否则 TestAgentToTUIEventEveryKindRegistered 会失败。
var tuiMappingExpectations = map[string]tuiMappingExpectation{
	"turn_start":       {plainTUIEvent("turn_start"), true, tui.Event{Kind: "turn_start"}},
	"turn_end":         {plainTUIEvent("turn_end"), true, tui.Event{Kind: "turn_end"}},
	"checkpoint_ready": {plainTUIEvent("checkpoint_ready"), false, tui.Event{}},
	"text_delta":       {plainTUIEvent("text_delta"), false, tui.Event{}}, // 文本走 onText 回调
	"reasoning_delta":  {plainTUIEvent("reasoning_delta"), false, tui.Event{}},
	"tool_start":       {plainTUIEvent("tool_start"), true, tui.Event{Kind: tui.EventToolStart}},
	"tool_end":         {plainTUIEvent("tool_end"), true, tui.Event{Kind: tui.EventToolEnd}},
	"usage":            {plainTUIEvent("usage"), true, tui.Event{Kind: tui.TuiEventUsage}},
	"error":            {plainTUIEvent("error"), true, tui.Event{Kind: tui.EventError}},
	"notice":           {plainTUIEvent("notice"), false, tui.Event{}}, // 普通通知不刷屏
	"stop":             {plainTUIEvent("stop"), false, tui.Event{}},
	"subagent_start":   {plainTUIEvent("subagent_start"), false, tui.Event{}},
	"subagent_end":     {plainTUIEvent("subagent_end"), false, tui.Event{}},
	"retry":            {plainTUIEvent("retry"), false, tui.Event{}}, // 无负载的 retry 不伪造倒计时
	"budget_reminder": {
		plainTUIEvent("budget_reminder"), true,
		tui.Event{Kind: tui.TuiEventNotice, Detail: "payload"},
	},
	"partial": {plainTUIEvent("partial"), false, tui.Event{}},
	"tool_arg_delta": {
		agent.Event{Kind: agent.EventToolArgDelta, ToolCallID: "c1", ArgDelta: `{"a":`},
		false, tui.Event{},
	}, // live-only：TUI 的 tool_start/tool_end 已带完整参数
	"goal_status": {
		agent.Event{Kind: goalStatusEventKind, Text: `{"state":"running","iteration":3,"max_iterations":10}`},
		true,
		tui.Event{Kind: tui.TuiEventGoalStatus, GoalState: "running", GoalIteration: 3, GoalMaxIterations: 10},
	},
}

func plainTUIEvent(kind string) agent.Event {
	return agent.Event{Kind: agent.EventKind(kind), Text: "payload"}
}

// TestAgentToTUIEventGoalStatusMalformedJSON 证明畸形状态负载被丢弃而不是把
// 原始 JSON 倒进 transcript；状态事件本身不能成为任意文本的上线通道。
func TestAgentToTUIEventGoalStatusMalformedJSON(t *testing.T) {
	for _, text := range []string{"", "not-json", `{"state":`} {
		got, ok := AgentToTUIEvent(agent.Event{Kind: goalStatusEventKind, Text: text})
		if ok || got != (tui.Event{}) {
			t.Fatalf("畸形 goal_status %q = (%+v, %v), want unmapped", text, got, ok)
		}
	}
}

// TestAgentToTUIEventGoalStatusIsCritical 证明目标循环状态走不可丢弃队列：
// 1000 次循环的每一轮状态都是用户理解进度的关键反馈，慢消费者也不能丢。
func TestAgentToTUIEventGoalStatusIsCritical(t *testing.T) {
	got, ok := AgentToTUIEvent(tuiMappingExpectations["goal_status"].in)
	if !ok {
		t.Fatal("goal_status must map to a TUI event")
	}
	if !isCriticalTUIEvent(got) {
		t.Fatal("goal_status must be routed through the non-droppable queue")
	}
}

// TestTUIEventPipelineDeliversGoalStatus 是端到端证明：/goal 状态事件确实
// 穿过 pipeline 的 hub → 桥接器 → TUI 通道，而不只是单元测试里的直接映射。
func TestTUIEventPipelineDeliversGoalStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTUIEventPipeline(ctx)
	defer p.Stop()

	EmitEvent(withEventEmit(context.Background(), p.Emit), agent.Event{
		Kind: goalStatusEventKind,
		Text: `{"state":"complete","iteration":4,"max_iterations":10}`,
	})
	select {
	case got := <-p.Events:
		want := tui.Event{Kind: tui.TuiEventGoalStatus, GoalState: "complete", GoalIteration: 4, GoalMaxIterations: 10}
		if got != want {
			t.Fatalf("pipelined goal_status = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goal_status did not reach the TUI event channel")
	}
}

// discoverAppEventKinds 用 go/parser 扫描 agent 的 EventKind 常量与 app 的
// goalStatusEventKind，返回全部事件种类。Go 常量无法运行时反射枚举，
// 因此用源码扫描作为穷举守卫的输入。
func discoverAppEventKinds(t *testing.T) []string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var root string
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到包含 go.mod 的仓库根目录")
		}
		dir = parent
	}
	var out []string
	for _, rel := range []string{"internal/agent/events.go", "internal/app/goal_loop_wire.go"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", rel, err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
		if err != nil {
			t.Fatalf("解析 %s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					continue
				}
				switch typ := vs.Type.(type) {
				case *ast.Ident:
					if typ.Name != "EventKind" {
						continue
					}
				case *ast.SelectorExpr:
					id, ok := typ.X.(*ast.Ident)
					if !ok || id.Name != "agent" || typ.Sel.Name != "EventKind" {
						continue
					}
				default:
					continue
				}
				for i := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err == nil {
						out = append(out, value)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestAgentToTUIEventApprovalNotice 记录审批通知会被识别（通过 ParseApprovalEvent），但有意没有 TUI 事件：
// 审批卡片由 Model.Approve 创建，并由之后的 tool_start 提升。
// 在这里发出会使其在回答之前显示为运行中。
// （接上行）
func TestAgentToTUIEventApprovalNotice(t *testing.T) {
	notices := []agent.Event{
		agent.ApprovalRequestedEvent("bash", "digest", "call-1"),
		agent.ApprovalResolvedEvent("bash", "call-1", true),
		agent.ApprovalResolvedEvent("bash", "call-1", false),
	}
	for i, e := range notices {
		kind, tool, id, _, ok := agent.ParseApprovalEvent(e)
		if !ok || kind == "" || tool != "bash" || id != "call-1" {
			t.Fatalf("notice %d not recognised as an approval: kind=%q tool=%q id=%q ok=%v", i, kind, tool, id, ok)
		}
		got, mapped := AgentToTUIEvent(e)
		if mapped {
			t.Fatalf("notice %d must not synthesize a TUI event, got %+v", i, got)
		}
		if got != (tui.Event{}) {
			t.Fatalf("notice %d returned a non-zero event: %+v", i, got)
		}
	}
}

// TestStartTUIEventBridgeDeliversInOrder 证明桥接器只按发布顺序转发映射后的事件，并过滤其余事件。
// （接上行）
func TestStartTUIEventBridgeDeliversInOrder(t *testing.T) {
	prev := tuiUsageVisible.Swap(true)
	defer tuiUsageVisible.Store(prev)
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartTUIEventBridge(ctx, hub)

	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolCallID: "a", ToolName: "bash", Args: "ls"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "thinking"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolCallID: "b", ToolName: "read", Args: "a.go"})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "a", ToolName: "bash", IsError: true, DurationMS: 5})
	hub.Publish(agent.Event{Kind: agent.EventUsage, Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 20, CachedTokens: 5}})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "b", ToolName: "read", DurationMS: 2})

	want := []tui.Event{
		{Kind: tui.EventToolStart, ToolCallID: "a", ToolName: "bash", Args: "ls"},
		{Kind: tui.EventToolStart, ToolCallID: "b", ToolName: "read", Args: "a.go"},
		{Kind: tui.EventToolEnd, ToolCallID: "a", ToolName: "bash", IsError: true, DurationMS: 5},
		{Kind: tui.TuiEventUsage, PromptTokens: 10, CompletionTokens: 20, CachedTokens: 5},
		{Kind: tui.EventToolEnd, ToolCallID: "b", ToolName: "read", DurationMS: 2},
	}
	for i, expected := range want {
		select {
		case got, open := <-ch:
			if !open {
				t.Fatalf("channel closed before event %d", i)
			}
			if got != expected {
				t.Fatalf("event %d = %+v, want %+v", i, got, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d was not delivered", i)
		}
	}
}

// TestStartTUIEventBridgeClosesOnCancel 证明取消会关闭通道和 hub 订阅，即使没有在途事件。
// （接上行）
func TestStartTUIEventBridgeClosesOnCancel(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := StartTUIEventBridge(ctx, hub)
	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("bridge must close its channel on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge channel did not close after cancel")
	}
}

// TestStartTUIEventBridgeSlowConsumer 证明 out 通道永不阻塞桥接器：当事件多于缓冲区容量且无人读取时，
// 取消仍能展开 goroutine 并关闭通道。
// （接上行）
func TestStartTUIEventBridgeSlowConsumer(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := StartTUIEventBridge(ctx, hub)

	for i := 0; i < tuiEventBridgeCap+64; i++ {
		hub.Publish(agent.Event{
			Kind:       agent.EventToolStart,
			ToolCallID: fmt.Sprintf("call-%d", i),
			ToolName:   "bash",
		})
	}
	if evicted := hub.Evicted(); evicted != 0 {
		t.Fatalf("hub evicted %d subscribers; the bridge must keep draining", evicted)
	}
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("bridge did not unblock and close with a full output buffer")
		}
	}
}

// TestTUIEventPipelineDeliversAgentEvents 证明生产接线：运行 context 上的一次 EmitEvent 调用会到达 TUI 通道，且 Stop 会关闭它。
// （接上行）
func TestTUIEventPipelineDeliversAgentEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTUIEventPipeline(ctx)

	runCtx := withEventEmit(context.Background(), p.Emit)
	EmitEvent(runCtx, agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call-1",
		ToolName:   "bash",
		Args:       `{"command":"echo hi"}`,
	})

	select {
	case got := <-p.Events:
		want := (tui.Event{
			Kind:       tui.EventToolStart,
			ToolCallID: "call-1",
			ToolName:   "bash",
			Args:       `{"command":"echo hi"}`,
		})
		if got != want {
			t.Fatalf("pipelined event = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EmitEvent did not reach the TUI event channel")
	}

	p.Stop()
	select {
	case _, open := <-p.Events:
		if open {
			t.Fatal("pipeline.Events must close after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline.Events did not close after Stop")
	}
}
