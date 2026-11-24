package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// recvTyped 从订阅通道读取一个事件，超时则判定失败。
func recvTyped(t *testing.T, ch <-chan agent.Event) agent.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("等待事件超时")
		return agent.Event{}
	}
}

// expectNoTypedEvent 断言通道在短时间内没有多余事件，用于验证过滤确实丢弃了不关心的类型。
func expectNoTypedEvent(t *testing.T, ch <-chan agent.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("收到不应投递的事件: kind=%q text=%q", ev.Kind, ev.Text)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubscribeTypedSingleKind 验证单类型过滤：只接收指定 Kind，其它类型（含终态事件）均被丢弃。
func TestSubscribeTypedSingleKind(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{string(agent.EventTextDelta)})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "first"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, Text: "skip-tool"})
	hub.Publish(agent.Event{Kind: agent.EventStop, Text: "skip-terminal"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "second"})

	for _, want := range []string{"first", "second"} {
		ev := recvTyped(t, ch)
		if ev.Kind != agent.EventTextDelta || ev.Text != want {
			t.Fatalf("事件 = (kind=%q, text=%q), want (text_delta, %q)", ev.Kind, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedMultipleKinds 验证多类型过滤：命中集合中任一类型即投递，且保持发布顺序。
func TestSubscribeTypedMultipleKinds(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{
		string(agent.EventToolStart),
		string(agent.EventToolEnd),
	})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventTurnStart, Text: "skip-turn"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolName: "read"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "skip-text"})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolName: "write"})

	first := recvTyped(t, ch)
	if first.Kind != agent.EventToolStart || first.ToolName != "read" {
		t.Fatalf("首个事件 = (kind=%q, tool=%q), want (tool_start, read)", first.Kind, first.ToolName)
	}
	second := recvTyped(t, ch)
	if second.Kind != agent.EventToolEnd || second.ToolName != "write" {
		t.Fatalf("次个事件 = (kind=%q, tool=%q), want (tool_end, write)", second.Kind, second.ToolName)
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedAllKinds 验证显式列出全部已知类型时，所有事件都会被投递。
// 清单来自源码扫描而非硬编码：新增 EventKind 常量会自动进入本测试，
// 再由 TestEngineEventKindsRegistered 检查它是否已在三个消费层登记。
func TestSubscribeTypedAllKinds(t *testing.T) {
	defs, _ := discoverEngineEventKinds(t)
	types := make([]string, len(defs))
	for i, def := range defs {
		types[i] = def.Value
	}

	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped(types)
	defer stop()

	for i, def := range defs {
		kind := agent.EventKind(def.Value)
		hub.Publish(agent.Event{Kind: kind, Text: def.Value})
		if got := recvTyped(t, ch); got.Kind != kind {
			t.Fatalf("第 %d 个事件 kind = %q, want %q", i, got.Kind, kind)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedPreservesOrder 验证过滤后事件的相对顺序与发布顺序严格一致。
func TestSubscribeTypedPreservesOrder(t *testing.T) {
	hub := NewEventHub()
	const wantKind = agent.EventKind("target")
	ch, stop := hub.SubscribeTyped([]string{string(wantKind)})
	defer stop()

	const total = 100
	for i := 0; i < total; i++ {
		kind := wantKind
		if i%2 == 1 {
			kind = agent.EventNotice // 夹在中间的干扰类型
		}
		hub.Publish(agent.Event{Kind: kind, Text: string(rune('a' + i%26))})
	}

	// 只对偶数序号发布 wantKind，读取顺序必须与发布顺序相同。
	for i := 0; i < total; i += 2 {
		ev := recvTyped(t, ch)
		if ev.Kind != wantKind {
			t.Fatalf("第 %d 次读取 kind = %q, want %q", i, ev.Kind, wantKind)
		}
		if want := string(rune('a' + i%26)); ev.Text != want {
			t.Fatalf("第 %d 次读取 text = %q, want %q", i, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedStopIsIdempotent 验证退订：stop 幂等、可并发调用，退订后通道关闭且不再投递。
func TestSubscribeTypedStopIsIdempotent(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{string(agent.EventNotice)})

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "first"})
	if ev := recvTyped(t, ch); ev.Text != "first" {
		t.Fatalf("事件 text = %q, want first", ev.Text)
	}

	// 并发调用 stop，借助 -race 检测数据竞争；重复调用不得 panic。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("退订后通道仍保持打开")
		}
	case <-time.After(time.Second):
		t.Fatal("退订后通道未关闭")
	}

	// 退订后再发布不应引发向已关闭通道写入的 panic。
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "after-stop"})
}

// TestSubscribeTypedNoTypesReceivesAll 验证 types 为 nil 或空切片时不过滤，行为与 Subscribe 一致。
func TestSubscribeTypedNoTypesReceivesAll(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []string
	}{
		{name: "nil", types: nil},
		{name: "empty", types: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewEventHub()
			ch, stop := hub.SubscribeTyped(tc.types)
			defer stop()

			kinds := []agent.EventKind{
				agent.EventNotice,
				agent.EventTextDelta,
				agent.EventStop,
				agent.EventToolStart,
			}
			for _, kind := range kinds {
				hub.Publish(agent.Event{Kind: kind})
			}
			for i, kind := range kinds {
				if got := recvTyped(t, ch); got.Kind != kind {
					t.Fatalf("第 %d 个事件 kind = %q, want %q", i, got.Kind, kind)
				}
			}
			expectNoTypedEvent(t, ch)
		})
	}
}

// TestSubscribeTypedCJKKind 验证 CJK 类型名与内置 EventKind 一样按字符串精确匹配。
func TestSubscribeTypedCJKKind(t *testing.T) {
	hub := NewEventHub()
	const target = "中文事件"
	ch, stop := hub.SubscribeTyped([]string{target})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventKind(target), Text: "one"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "skip"})
	hub.Publish(agent.Event{Kind: agent.EventKind("另一种类型"), Text: "skip-too"})
	hub.Publish(agent.Event{Kind: agent.EventKind(target), Text: "two"})

	for _, want := range []string{"one", "two"} {
		ev := recvTyped(t, ch)
		if ev.Kind != agent.EventKind(target) || ev.Text != want {
			t.Fatalf("事件 = (kind=%q, text=%q), want (中文事件, %q)", ev.Kind, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedDoesNotAffectNormalSubscribers 验证类型过滤仅作用于该订阅者，
// 普通 Subscribe 订阅者仍接收全部事件。
func TestSubscribeTypedDoesNotAffectNormalSubscribers(t *testing.T) {
	hub := NewEventHub()
	typed, stopTyped := hub.SubscribeTyped([]string{string(agent.EventTextDelta)})
	defer stopTyped()
	normal, stopNormal := hub.Subscribe(0)
	defer stopNormal()

	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "keep"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, Text: "only-normal"})

	if ev := recvTyped(t, typed); ev.Text != "keep" {
		t.Fatalf("过滤订阅者事件 text = %q, want keep", ev.Text)
	}
	expectNoTypedEvent(t, typed)

	for _, want := range []string{"keep", "only-normal"} {
		if ev := recvTyped(t, normal); ev.Text != want {
			t.Fatalf("普通订阅者事件 text = %q, want %q", ev.Text, want)
		}
	}
}

// ---- 事件种类穷举守卫 ----------------------------------------------------
//
// Go 常量无法在运行时反射枚举，因此这里用 go/parser 扫描源码里的
// EventKind 常量声明：新增一种事件而没在 engineEventRegistry 登记、
// 或在 TUI / native 消费层漏掉分支时，TestEngineEventKindsRegistered 会失败。
// 这样"发了但 UI 收不到"的沉默丢失在测试阶段就暴露。

// eventKindDef 是一个从源码发现的事件种类定义。
type eventKindDef struct {
	Value string // 线上 kind 字符串
	Name  string // 常量标识符
	File  string // 相对仓库根的路径
}

// eventGuardRepoRoot 从测试工作目录向上找到 go.mod。
func eventGuardRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到包含 go.mod 的仓库根目录")
		}
		dir = parent
	}
}

// parseEventGuardFile 解析一个仓库内的 Go 源文件。
func parseEventGuardFile(t *testing.T, root, rel string) *ast.File {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", rel, err)
	}
	return f
}

// isEventKindType 报告 VS 的类型是不是 EventKind（本包）或 agent.EventKind。
func isEventKindType(expr ast.Expr) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return typ.Name == "EventKind"
	case *ast.SelectorExpr:
		id, ok := typ.X.(*ast.Ident)
		return ok && id.Name == "agent" && typ.Sel.Name == "EventKind"
	}
	return false
}

// discoverEngineEventKinds 扫描 agent 的事件常量块与 app 的 goalStatusEventKind，
// 返回全部引擎事件种类及「常量名 → kind 值」映射。新增常量会自动进入结果。
func discoverEngineEventKinds(t *testing.T) ([]eventKindDef, map[string]string) {
	t.Helper()
	root := eventGuardRepoRoot(t)
	var defs []eventKindDef
	names := map[string]string{}
	for _, rel := range []string{"internal/agent/events.go", "internal/app/goal_loop_wire.go"} {
		f := parseEventGuardFile(t, root, rel)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || !isEventKindType(vs.Type) {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					defs = append(defs, eventKindDef{Value: value, Name: name.Name, File: rel})
					names[name.Name] = value
				}
			}
		}
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Value < defs[j].Value })
	return defs, names
}

// caseKindsInFile 收集一个源文件 switch case 中出现的引擎事件 kind。
// 支持字符串字面量（native）、agent.Xxx 选择器与本地常量标识符（app）。
func caseKindsInFile(t *testing.T, root, rel string, names map[string]string, known map[string]bool) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	add := func(v string) {
		if known[v] {
			out[v] = true
		}
	}
	ast.Inspect(parseEventGuardFile(t, root, rel), func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range cc.List {
			switch x := expr.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if v, err := strconv.Unquote(x.Value); err == nil {
						add(v)
					}
				}
			case *ast.Ident:
				if v, ok := names[x.Name]; ok {
					add(v)
				}
			case *ast.SelectorExpr:
				id, ok := x.X.(*ast.Ident)
				if ok && id.Name == "agent" {
					if v, ok := names[x.Sel.Name]; ok {
						add(v)
					}
				}
			}
		}
		return true
	})
	return out
}

// engineEventRegistry 是每种事件在三个消费层的登记。
//   - TUI：`map:`/`conditional:` 表示 app 桥接器有 case，`case-ignore:` 表示
//     有 case 但有意不映射，`skip:` 表示刻意没有 case；
//   - RPC：传输层全透传（server/rpc.go），由 SubscribeTyped 行为验证；
//   - Native：`case:` 表示 applyEngineEvent 有 case，`other:` 表示走别的 RPC
//     路径（文本/错误/完成），`skip:` 表示有意忽略。
//
// 新增 EventKind 必须在此登记，否则 TestEngineEventKindsRegistered 失败。
var engineEventRegistry = map[string]struct {
	TUI    string
	RPC    bool
	Native string
	Note   string
}{
	"turn_start":       {"map:turn_start", true, "skip:轮次边界只用于 TUI 统计行", ""},
	"turn_end":         {"map:turn_end", true, "skip:同上", ""},
	"checkpoint_ready": {"skip:检查点由 session 层持久化，无 UI 展示位", true, "skip:对 UI 无可见变化", ""},
	"text_delta":       {"skip:通过 Execute 的 onText 回调直达 TUI", true, "other:通过 prompt/delta RPC 直达 assistant 气泡", ""},
	"reasoning_delta":  {"map:reasoning", true, "other:同文本流", "推理块曾断链不可见，现已映射到 TUI 主 transcript 的可展开推理条目"},
	"tool_start":       {"map:tool_start", true, "case:tool_start", ""},
	"tool_end":         {"map:tool_end", true, "case:tool_end", ""},
	"usage":            {"map:usage", true, "case:usage", ""},
	"error":            {"map:error", true, "other:引擎错误经 engineError RPC 呈现", ""},
	"notice":           {"conditional:审批/工具进度/压缩预算通知有映射", true, "case:notice", "普通 notice 仍被两层忽略"},
	"stop":             {"skip:运行终结由完成回调呈现", true, "other:done/停止 RPC", ""},
	"subagent_start":   {"skip:opt-in 子代理事件刻意不刷屏", true, "skip:opt-in 特性", ""},
	"subagent_end":     {"skip:同上", true, "skip:opt-in 特性", ""},
	"retry":            {"map:retry", true, "skip:退避反馈只在 TUI 展示", ""},
	"budget_reminder":  {"map:notice", true, "skip:提醒已注入模型轮次，native 无独立位", ""},
	"partial":          {"skip:截断前缀不进 transcript", true, "other:同 text_delta 路径", ""},
	"tool_arg_delta":   {"case-ignore:live-only，仅 SSE/native 实时预览参数", true, "case:tool_arg_delta", "TUI 的 tool_start/tool_end 已携带完整参数"},
	"goal_status":      {"map:goal_status", true, "case:goal_status", "此前两层都静默丢弃，本轮补齐"},
}

// registryWantsCase 报告登记是否声明消费层存在显式 case。
func registryWantsCase(status string) bool {
	return strings.HasPrefix(status, "map:") ||
		strings.HasPrefix(status, "conditional:") ||
		strings.HasPrefix(status, "case:") ||
		strings.HasPrefix(status, "case-ignore:")
}

// TestEngineEventKindsRegistered 是穷举守卫：源码里出现的每个事件种类都必须在
// engineEventRegistry 登记，且声明了 case 的种类确实存在于对应消费层。
// 新增事件只改生产者、忘改消费者时，本测试会给出明确失败点。
func TestEngineEventKindsRegistered(t *testing.T) {
	root := eventGuardRepoRoot(t)
	defs, names := discoverEngineEventKinds(t)
	known := make(map[string]bool, len(defs))
	for _, def := range defs {
		known[def.Value] = true
	}
	tuiCases := caseKindsInFile(t, root, "internal/app/tui_event_wire.go", names, known)
	nativeCases := caseKindsInFile(t, root, "native/main.go", names, known)

	if len(defs) == 0 {
		t.Fatal("没有扫描到任何 EventKind 常量，穷举守卫失效")
	}
	for _, def := range defs {
		reg, ok := engineEventRegistry[def.Value]
		if !ok {
			t.Errorf("事件 %q（%s，常量 %s）未登记到 engineEventRegistry：请补 TUI/RPC/native 三列",
				def.Value, def.File, def.Name)
			continue
		}
		if !reg.RPC {
			t.Errorf("事件 %q 的 RPC 列必须为 true（传输层全透传）", def.Value)
		}
		if want := registryWantsCase(reg.TUI); want != tuiCases[def.Value] {
			t.Errorf("事件 %q 的 TUI 登记为 %q，case 存在 = %v，want %v", def.Value, reg.TUI, tuiCases[def.Value], want)
		}
		wantNativeCase := strings.HasPrefix(reg.Native, "case:")
		if wantNativeCase != nativeCases[def.Value] {
			t.Errorf("事件 %q 的 native 登记为 %q，case 存在 = %v，want %v", def.Value, reg.Native, nativeCases[def.Value], wantNativeCase)
		}
	}
	for kind := range engineEventRegistry {
		if !known[kind] {
			t.Errorf("engineEventRegistry 登记了已不存在的事件 %q", kind)
		}
	}
}
