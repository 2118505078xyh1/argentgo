package agent

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/hooks"
	"example.com/argentgo/internal/llm"
)

// TestHookContextInjectedBeforeFirstRequest 确认单条 hook 附加上下文在下轮请求构造前作为用户轮次注入。
func TestHookContextInjectedBeforeFirstRequest(t *testing.T) {
	q := &HookContextQueue{}
	q.Add("注意：只改 loop.go")
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if !containsUser(messages, "注意：只改 loop.go") {
			t.Fatal("hook context was not injected before the first request")
		}
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}
	if _, err := runner.Run(WithHookContextQueue(context.Background(), q), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestAddHookOutcomesReadsOutcomeField 确认队列直接消费 hooks.Outcome.AdditionalContext 字段，
// 空字段被忽略，非空字段按事件顺序保留。
func TestAddHookOutcomesReadsOutcomeField(t *testing.T) {
	q := &HookContextQueue{}
	q.AddHookOutcomes([]hooks.Outcome{
		{AdditionalContext: "第一条"},
		{AdditionalContext: ""},
		{AdditionalContext: "第二条"},
	})
	got := q.drain()
	want := []string{"第一条", "第二条"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contexts = %v, want %v", got, want)
	}
}

// TestHookContextMergeDedupAndBound 确认多 hook 合并按事件顺序稳定、去重，且条数有上限防膨胀。
func TestHookContextMergeDedupAndBound(t *testing.T) {
	q := &HookContextQueue{}
	for _, s := range []string{"a", "b", "a", "c"} {
		if !q.Add(s) && s != "a" {
			t.Fatalf("Add(%q) should accept first occurrence", s)
		}
	}
	// 重复项应被丢弃。
	if q.Add("a") {
		t.Fatal("duplicate context should be dropped")
	}
	// 远超条数上限；超出部分应被静默丢弃。
	for i := 0; i < 50; i++ {
		q.Add("extra-" + strconv.Itoa(i))
	}
	got := q.drain()
	if len(got) != hookContextMaxItems {
		t.Fatalf("contexts count = %d, want bound %d", len(got), hookContextMaxItems)
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("merge order = %v, want a,b,c first", got[:3])
	}
	seen := make(map[string]bool, len(got))
	for _, s := range got {
		if seen[s] {
			t.Fatalf("duplicate survived merge: %q", s)
		}
		seen[s] = true
	}
}

// TestHookContextByteBoundCJKSafe 确认总字节上限生效且只整条丢弃，CJK 文本不会被切成半个 rune。
func TestHookContextByteBoundCJKSafe(t *testing.T) {
	q := &HookContextQueue{}
	huge := strings.Repeat("中", hookContextMaxBytes/3+1) // 超过总字节上限
	if q.Add(huge) {
		t.Fatal("oversized context should be dropped")
	}
	if !q.Add("短上下文") {
		t.Fatal("small context should still be accepted after a drop")
	}
	got := q.drain()
	if len(got) != 1 || got[0] != "短上下文" {
		t.Fatalf("contexts = %v, want only the small one", got)
	}
	if !utf8.ValidString(got[0]) {
		t.Fatalf("injected context is not valid UTF-8: %q", got[0])
	}
}

// TestHookContextAbsentByteIdentical 确认无附加上下文（无队列或空队列）时结果逐字节不变。
func TestHookContextAbsentByteIdentical(t *testing.T) {
	type outcome struct {
		res    Result
		events []Event
	}
	drive := func(ctx context.Context) outcome {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		var events []Event
		runner := &Runner{Opts: Options{
			Model: "m", Chatter: chat, Tools: &fakeTools{},
			Emit: func(e Event) { events = append(events, e) },
		}}
		res, err := runner.Run(ctx, "hi")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return outcome{res: res, events: events}
	}
	baseline := drive(context.Background())
	// 空队列与完全不带队列都应逐字节相同（默认绝大多数运行走这里）。
	for _, ctx := range []context.Context{
		context.Background(),
		WithHookContextQueue(context.Background(), &HookContextQueue{}),
	} {
		got := drive(ctx)
		if !reflect.DeepEqual(baseline.res, got.res) {
			t.Fatalf("absent hook context changed result:\n got=%+v\nwant=%+v", got.res, baseline.res)
		}
		if !reflect.DeepEqual(baseline.events, got.events) {
			t.Fatalf("absent hook context changed events:\n got=%+v\nwant=%+v", got.events, baseline.events)
		}
	}
}

// TestHookContextCoexistsWithSteer 确认 steering 与 hook 附加上下文共存，且按 steering 先、hook 后的顺序注入。
func TestHookContextCoexistsWithSteer(t *testing.T) {
	steer := make(chan string, 1)
	steer <- "用户纠正"
	q := &HookContextQueue{}
	q.Add("hook 上下文")
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			var users []string
			for _, m := range messages {
				if m.Role == "user" && m.Content != "hi" {
					users = append(users, m.Content)
				}
			}
			if len(users) != 2 || users[0] != "用户纠正" || users[1] != "hook 上下文" {
				t.Fatalf("users = %v, want [用户纠正 hook 上下文]", users)
			}
		}
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Steer: steer}}
	if _, err := runner.Run(WithHookContextQueue(context.Background(), q), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestHookContextDuringToolsWaitsForTurnBoundary 确认工具批次期间到达的 hook 上下文等到轮次边界才注入，
// 不会插进 tool_use/tool_result 之间。
func TestHookContextDuringToolsWaitsForTurnBoundary(t *testing.T) {
	q := &HookContextQueue{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(context.Context, string, string) (string, error) {
		q.Add("批次后注入")
		return "ok", nil
	}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		toolIdx, ctxIdx := -1, -1
		for i, m := range messages {
			if m.Role == "tool" && m.ToolCallID == "c1" {
				toolIdx = i
			}
			if m.Role == "user" && m.Content == "批次后注入" {
				ctxIdx = i
			}
		}
		if toolIdx < 0 || ctxIdx < 0 || ctxIdx < toolIdx {
			t.Fatalf("hook context entered tool batch: tool=%d context=%d", toolIdx, ctxIdx)
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}
	if _, err := runner.Run(WithHookContextQueue(context.Background(), q), "inspect"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestHookContextAfterAnswerStartsAnotherTurn 确认模型给出最终答案后到达的 hook 上下文仍触发一轮，
// 使该上下文不会在 StopComplete 时被丢弃。
func TestHookContextAfterAnswerStartsAnotherTurn(t *testing.T) {
	q := &HookContextQueue{}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			q.Add("稍后补充的上下文")
			return textReply("first answer"), nil
		}
		if !containsUser(messages, "稍后补充的上下文") {
			t.Fatal("late hook context was not injected into the follow-up turn")
		}
		return textReply("second answer"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}
	res, err := runner.Run(WithHookContextQueue(context.Background(), q), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "second answer" || res.Turns != 2 {
		t.Fatalf("res = %+v, want second answer after 2 turns", res)
	}
}
