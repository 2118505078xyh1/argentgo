package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// wireEvent/wireRecord 只保留与"回灌内容逐字节不变"相关的字段，
// 刻意丢弃计时字段（DurationMS/StartedAtMS/FinishedAtMS），使开/关两次运行的
// 事件与记录可以直接逐字节对比。
type wireEvent struct {
	kind   EventKind
	output string
}

type wireRecord struct {
	kind   string
	output string
}

type wireRecorder struct {
	mu   sync.Mutex
	rows []wireRecord
}

func (r *wireRecorder) append(kind string, payload any) error {
	out := ""
	if m, ok := payload.(map[string]string); ok {
		out = m["output"]
	}
	r.mu.Lock()
	r.rows = append(r.rows, wireRecord{kind: kind, output: out})
	r.mu.Unlock()
	return nil
}

func (r *wireRecorder) snapshot() []wireRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wireRecord(nil), r.rows...)
}

// wrapScenario 标识一条会抵达 appendTool 的路径，用于覆盖全部 6 个调用点。
type wrapScenario int

const (
	scenarioSerial    wrapScenario = iota // runTools L279
	scenarioError                         // runTools L279（错误输出）
	scenarioBlocked                       // runTools L268（重复防护阻断）
	scenarioCancelled                     // runTools L261（取消）
	scenarioFillUnrun                     // fillUnrun L570
	scenarioParallel                      // runParallelCalls L343/L345
)

// runWrapScenario 按给定路径执行一次工具批次，返回工具消息、事件快照、记录快照及本次调用列表。
// wrapping 参数仅决定 appendTool 是否封装消息内容；其余输入完全一致。
func runWrapScenario(wrapping bool, scenario wrapScenario) ([]llm.Message, []wireEvent, []wireRecord, []llm.ToolCall) {
	SetToolResultWrapping(wrapping)

	tools := &fakeTools{run: func(_ context.Context, name, _ string) (string, error) {
		if name == "fail" {
			return "", errors.New("boom")
		}
		return "payload-from-" + name, nil
	}}

	var calls []llm.ToolCall
	var maxParallel int
	switch scenario {
	case scenarioSerial:
		calls = []llm.ToolCall{toolCall("c1", "read", `{}`)}
	case scenarioError:
		calls = []llm.ToolCall{toolCall("c1", "fail", `{}`)}
	case scenarioBlocked:
		// 两个完全相同的调用：防护器在第二次命中阻断（L268）。
		calls = []llm.ToolCall{toolCall("c1", "read", `{}`), toolCall("c2", "read", `{}`)}
	case scenarioCancelled, scenarioFillUnrun:
		calls = []llm.ToolCall{toolCall("c1", "read", `{}`), toolCall("c2", "search", `{}`)}
	case scenarioParallel:
		// 两个只读工具 + maxParallel=2 → 走 runParallelCalls（L343/L345）。
		calls = []llm.ToolCall{toolCall("c1", "read", `{}`), toolCall("c2", "search", `{}`)}
		maxParallel = 2
	}

	var events []wireEvent
	emit := func(e Event) { events = append(events, wireEvent{kind: e.Kind, output: e.Output}) }
	rec := &wireRecorder{}
	s := newRunState(Options{Tools: tools, Record: rec, MaxParallel: maxParallel}, "prompt", emit)

	switch scenario {
	case scenarioBlocked:
		s.guard = newDedupeWindow(1, 10)
		s.runTools(context.Background(), 1, calls)
	case scenarioCancelled:
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.runTools(ctx, 1, calls)
	case scenarioFillUnrun:
		s.fillUnrun(calls, "not run")
	default: // scenarioSerial / scenarioError / scenarioParallel
		s.runTools(context.Background(), 1, calls)
	}

	var toolMsgs []llm.Message
	for _, m := range s.messages {
		if m.Role == "tool" {
			toolMsgs = append(toolMsgs, m)
		}
	}
	return toolMsgs, events, rec.snapshot(), calls
}

// toolNameByID 建立 ToolCallID → 工具名的映射，供 Unwrap 后的来源校验使用。
func toolNameByID(calls []llm.ToolCall) map[string]string {
	out := make(map[string]string, len(calls))
	for _, c := range calls {
		out[c.ID] = c.Function.Name
	}
	return out
}

// assertWrappingParity 校验：开启封装只改变模型可见消息的内容，
// 事件与记录保持原始字节；每条消息都能解封回原始输出，且来源工具正确。
func assertWrappingParity(t *testing.T, scenario wrapScenario) {
	t.Helper()
	SetToolResultWrapping(false)
	rawMsgs, rawEvents, rawRecords, calls := runWrapScenario(false, scenario)
	gotMsgs, gotEvents, gotRecords, _ := runWrapScenario(true, scenario)
	SetToolResultWrapping(false)

	if len(rawMsgs) == 0 {
		t.Fatalf("场景 %d 未产生任何工具消息", scenario)
	}
	if len(gotMsgs) != len(rawMsgs) {
		t.Fatalf("场景 %d 消息数 %d != 基线 %d", scenario, len(gotMsgs), len(rawMsgs))
	}
	names := toolNameByID(calls)

	for i := range rawMsgs {
		raw, got := rawMsgs[i], gotMsgs[i]
		if raw.Role != got.Role || raw.ToolCallID != got.ToolCallID {
			t.Fatalf("场景 %d 消息 %d 的 Role/ToolCallID 被改动: %+v -> %+v", scenario, i, raw, got)
		}
		if IsWrapped(raw.Content) {
			t.Fatalf("场景 %d 基线消息 %d 不应被封装: %q", scenario, i, raw.Content)
		}
		if !IsWrapped(got.Content) {
			t.Fatalf("场景 %d 开启后消息 %d 未被封装: %q", scenario, i, got.Content)
		}
		unwrapped, ok := UnwrapToolResult(got.Content)
		if !ok {
			t.Fatalf("场景 %d 消息 %d 无法解封: %q", scenario, i, got.Content)
		}
		if unwrapped.Content != raw.Content {
			t.Fatalf("场景 %d 消息 %d 解封内容改变:\ngot : %q\nwant: %q", scenario, i, unwrapped.Content, raw.Content)
		}
		if want := names[got.ToolCallID]; unwrapped.Tool != want {
			t.Fatalf("场景 %d 消息 %d 来源工具 = %q, want %q", scenario, i, unwrapped.Tool, want)
		}
		if unwrapped.Trust != untrustedTrustValue {
			t.Fatalf("场景 %d 消息 %d 信任级别 = %q, want %q", scenario, i, unwrapped.Trust, untrustedTrustValue)
		}
	}

	// 事件与记录是给前端/session 的持久化字节，必须与基线完全相同。
	if !reflect.DeepEqual(gotEvents, rawEvents) {
		t.Fatalf("场景 %d 事件字节改变:\ngot : %+v\nwant: %+v", scenario, gotEvents, rawEvents)
	}
	if !reflect.DeepEqual(gotRecords, rawRecords) {
		t.Fatalf("场景 %d 记录字节改变:\ngot : %+v\nwant: %+v", scenario, gotRecords, rawRecords)
	}
}

// TestToolResultWrappingFlag 验证 opt-in 开关的默认值与切换语义。
func TestToolResultWrappingFlag(t *testing.T) {
	SetToolResultWrapping(false)
	defer SetToolResultWrapping(false)
	if toolResultWrappingEnabled() {
		t.Fatal("默认应关闭工具结果封装")
	}
	SetToolResultWrapping(true)
	if !toolResultWrappingEnabled() {
		t.Fatal("设置后应开启工具结果封装")
	}
}

// TestToolResultWrappingCoversAllAppendPoints 表驱动覆盖 appendTool 的全部 6 个调用点：
// L261（取消）、L268（阻断）、L279（正常与错误）、L343/L345（并行）、L570（fillUnrun）。
func TestToolResultWrappingCoversAllAppendPoints(t *testing.T) {
	cases := []struct {
		name     string
		scenario wrapScenario
	}{
		{"serial success L279", scenarioSerial},
		{"serial error L279", scenarioError},
		{"blocked L268", scenarioBlocked},
		{"cancelled L261", scenarioCancelled},
		{"fillUnrun L570", scenarioFillUnrun},
		{"parallel L343/L345", scenarioParallel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertWrappingParity(t, tc.scenario)
		})
	}
}

// TestToolResultWrappingOffByteIdentical 锁定关闭态：与基线在消息、事件、记录上完全相同，
// 且消息内容不含任何封装边界。
func TestToolResultWrappingOffByteIdentical(t *testing.T) {
	SetToolResultWrapping(false)
	defer SetToolResultWrapping(false)

	for _, scenario := range []wrapScenario{
		scenarioSerial, scenarioError, scenarioBlocked,
		scenarioCancelled, scenarioFillUnrun, scenarioParallel,
	} {
		firstMsgs, firstEvents, firstRecords, _ := runWrapScenario(false, scenario)
		secondMsgs, secondEvents, secondRecords, _ := runWrapScenario(false, scenario)
		if !reflect.DeepEqual(firstMsgs, secondMsgs) ||
			!reflect.DeepEqual(firstEvents, secondEvents) ||
			!reflect.DeepEqual(firstRecords, secondRecords) {
			t.Fatalf("场景 %d 关闭态两次运行不一致", scenario)
		}
		for _, m := range firstMsgs {
			if IsWrapped(m.Content) {
				t.Fatalf("场景 %d 关闭态消息被封装: %q", scenario, m.Content)
			}
		}
	}
}

// TestToolResultWrappingToggleRestoresBaseline 验证开→关切换后回到与初始基线
// 逐字节相同的状态；开启态只应改变消息内容，事件/记录始终不变。
func TestToolResultWrappingToggleRestoresBaseline(t *testing.T) {
	SetToolResultWrapping(false)
	defer SetToolResultWrapping(false)

	baseMsgs, baseEvents, baseRecords, _ := runWrapScenario(false, scenarioSerial)

	onMsgs, onEvents, onRecords, _ := runWrapScenario(true, scenarioSerial)
	if reflect.DeepEqual(onMsgs, baseMsgs) {
		t.Fatal("开启态消息内容未变化")
	}
	if !reflect.DeepEqual(onEvents, baseEvents) || !reflect.DeepEqual(onRecords, baseRecords) {
		t.Fatal("开启态事件/记录不应变化")
	}
	for i := range onMsgs {
		if !IsWrapped(onMsgs[i].Content) {
			t.Fatalf("开启态消息 %d 未被封装", i)
		}
	}

	backMsgs, backEvents, backRecords, _ := runWrapScenario(false, scenarioSerial)
	if !reflect.DeepEqual(backMsgs, baseMsgs) ||
		!reflect.DeepEqual(backEvents, baseEvents) ||
		!reflect.DeepEqual(backRecords, baseRecords) {
		t.Fatal("关闭后未恢复到与初始基线逐字节相同的状态")
	}
}

// TestToolResultWrappingParallelPairing 验证并行批次中每条封装消息都与正确的调用配对，
// 且解封后的内容取回各自工具的原始输出（不会串位）。
func TestToolResultWrappingParallelPairing(t *testing.T) {
	SetToolResultWrapping(true)
	defer SetToolResultWrapping(false)

	msgs, _, _, calls := runWrapScenario(true, scenarioParallel)
	names := toolNameByID(calls)
	if len(msgs) != len(calls) {
		t.Fatalf("并行消息数 %d != 调用数 %d", len(msgs), len(calls))
	}
	seen := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if seen[m.ToolCallID] {
			t.Fatalf("重复的工具调用消息 id=%q", m.ToolCallID)
		}
		seen[m.ToolCallID] = true
		wantTool := names[m.ToolCallID]
		unwrapped, ok := UnwrapToolResult(m.Content)
		if !ok {
			t.Fatalf("消息 id=%q 无法解封", m.ToolCallID)
		}
		if unwrapped.Tool != wantTool {
			t.Fatalf("消息 id=%q 来源工具 = %q, want %q", m.ToolCallID, unwrapped.Tool, wantTool)
		}
		if want := "payload-from-" + wantTool; unwrapped.Content != want {
			t.Fatalf("消息 id=%q 内容 = %q, want %q", m.ToolCallID, unwrapped.Content, want)
		}
	}
}
