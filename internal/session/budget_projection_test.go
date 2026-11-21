package session

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

// projAssistantWithCalls 构造一条携带 tool_calls 载荷的 assistant 消息。
func projAssistantWithCalls(content string, ids ...string) Entry {
	type call struct {
		ID string `json:"id"`
	}
	calls := make([]call, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, call{ID: id})
	}
	data, err := json.Marshal(struct {
		ToolCalls []call `json:"tool_calls"`
	}{calls})
	if err != nil {
		panic(err)
	}
	return Entry{Role: "assistant", Content: content, Data: data}
}

func hasProjectionWarningCode(warnings []ProjectionWarning, code ProjectionWarningCode) bool {
	for _, w := range warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func hasProjectionActionKind(actions []ProjectionAction, kind ProjectionActionKind) bool {
	for _, a := range actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// TestProjectDropsWholeToolPair 验证切分按工具配对边界整对丢弃：assistant 调用与它的
// 全部（含并行）tool 结果要么全留、要么全弃，且切点不会落在 tool_result 上。
func TestProjectDropsWholeToolPair(t *testing.T) {
	messages := []Entry{
		{Role: "user", Content: "old"},                  // #0
		projAssistantWithCalls("calling", "c1"),         // #1
		{Role: "tool", ToolCallID: "c1", Content: "r1"}, // #2
		{Role: "tool", ToolCallID: "c1", Content: "r2"}, // #3
		{Role: "user", Content: "new"},                  // #4
	}
	policy := ProjectionPolicy{KeepRecentUser: true}

	// 预算只够最新的 user 消息（"new" = 3 rune）。
	result, actions, _ := Project(messages, 3, policy)
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}
	if got := result.Messages[0]; got.Role != "user" || got.Content != "new" {
		t.Fatalf("kept = {%s, %q}, want user/new", got.Role, got.Content)
	}
	// 结果首条不是 tool_result，说明切点没有落在孤立结果上。
	if result.Messages[0].Role == "tool" {
		t.Fatal("cut landed on a tool_result-only message")
	}

	pairDropped := false
	for _, a := range actions {
		if a.Kind == ProjectionDropPair {
			pairDropped = true
			if a.Index != 1 {
				t.Errorf("drop_pair index = %d, want 1", a.Index)
			}
		}
		if a.Kind == ProjectionDropMessage && (a.Index == 2 || a.Index == 3) {
			t.Errorf("tool result #%d was dropped individually, breaking the pair", a.Index)
		}
	}
	if !pairDropped {
		t.Fatal("missing drop_pair action")
	}
	// 配对的三条消息均不应出现在结果中。
	for _, kept := range result.Messages {
		if kept.Role == "tool" || kept.Content == "calling" {
			t.Fatalf("pair member survived: %+v", kept)
		}
	}

	// 预算充足时整对保留。
	full, _, _ := Project(messages, 1000, policy)
	if len(full.Messages) != len(messages) {
		t.Fatalf("full projection len = %d, want %d", len(full.Messages), len(messages))
	}
}

// TestProjectKeepsLiveToolTail 验证未配对的活尾 tool_use 在超预算时仍被保护。
func TestProjectKeepsLiveToolTail(t *testing.T) {
	messages := []Entry{
		{Role: "user", Content: "u"},                       // #0
		projAssistantWithCalls("a", "done"),                // #1 已配对
		{Role: "tool", ToolCallID: "done", Content: "res"}, // #2
		projAssistantWithCalls("live", "open"),             // #3 未配对活尾
	}
	// 预算只够活尾内容（"live" = 4 rune）。
	result, _, _ := Project(messages, 4, ProjectionPolicy{KeepLiveToolTail: true})
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}
	if got := result.Messages[0]; got.Role != "assistant" || got.Content != "live" {
		t.Fatalf("kept = {%s, %q}, want assistant/live", got.Role, got.Content)
	}
}

// TestProjectDropsThinking 验证 DropThinking 清除思考文本、记录动作，且不改入参。
func TestProjectDropsThinking(t *testing.T) {
	messages := []Entry{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "ans", Reasoning: "secret", ReasoningSignature: "sig"},
	}
	result, actions, _ := Project(messages, 100, ProjectionPolicy{DropThinking: true})
	if len(result.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(result.Messages))
	}
	got := result.Messages[1]
	if got.Reasoning != "" || got.ReasoningSignature != "" {
		t.Fatalf("thinking not dropped: %+v", got)
	}
	if !hasProjectionActionKind(actions, ProjectionDropThinking) {
		t.Fatal("missing drop_thinking action")
	}
	if messages[1].Reasoning != "secret" || messages[1].ReasoningSignature != "sig" {
		t.Fatal("Project mutated input Reasoning")
	}
}

// TestProjectRuneSafeCJKEmoji 验证按 rune 计费与 rune 安全截断：CJK/emoji 不被切坏，
// 且预算按字符数而非字节数计算。
func TestProjectRuneSafeCJKEmoji(t *testing.T) {
	content := "你好世界😀🎉" // 6 个 rune，20 字节
	messages := []Entry{{Role: "user", Content: content}}
	policy := ProjectionPolicy{KeepRecentUser: true}

	// 预算 6 = 6 个 rune：应完整保留，证明按 rune 而非字节计费。
	fit, actions, warnings := Project(messages, 6, policy)
	if fit.Messages[0].Content != content || fit.Runes != 6 || fit.Truncated {
		t.Fatalf("rune budget misfit: %+v", fit)
	}
	if hasProjectionActionKind(actions, ProjectionTruncate) || len(warnings) != 0 {
		t.Fatal("unexpected truncation/warning for rune-exact budget")
	}

	// 预算 3：rune 安全截断到前 3 个字符。
	cut, actions, warnings := Project(messages, 3, policy)
	if len(cut.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(cut.Messages))
	}
	got := cut.Messages[0].Content
	if !utf8.ValidString(got) {
		t.Fatalf("truncated content is invalid UTF-8: %q", got)
	}
	for _, r := range got {
		if r == utf8.RuneError {
			t.Fatalf("truncated content split a rune: %q", got)
		}
	}
	if want := string([]rune(content)[:3]); got != want {
		t.Fatalf("truncated = %q, want %q", got, want)
	}
	if !cut.Truncated || !hasProjectionActionKind(actions, ProjectionTruncate) {
		t.Fatal("missing truncation marker")
	}
	if !hasProjectionWarningCode(warnings, ProjectionBudgetUnreachable) {
		t.Fatal("missing unreachable warning when protected message is truncated")
	}
	if messages[0].Content != content {
		t.Fatal("Project mutated input Content")
	}
}

// TestProjectBudgetUnreachableWarnings 验证非正预算给出显式告警，不 panic、不静默。
func TestProjectBudgetUnreachableWarnings(t *testing.T) {
	messages := []Entry{{Role: "user", Content: "hello"}}
	policy := ProjectionPolicy{KeepRecentUser: true}
	for _, budget := range []int{0, -5} {
		result, _, warnings := Project(messages, budget, policy)
		if len(result.Messages) != 1 {
			t.Fatalf("budget %d: len(Messages) = %d, want 1 (best effort)", budget, len(result.Messages))
		}
		if !hasProjectionWarningCode(warnings, ProjectionBudgetNotPositive) {
			t.Errorf("budget %d: missing budget_not_positive warning", budget)
		}
		if !hasProjectionWarningCode(warnings, ProjectionBudgetUnreachable) {
			t.Errorf("budget %d: missing budget_unreachable warning", budget)
		}
	}
}

// TestProjectDoesNotMutateInput 验证截断、思考丢弃、丢弃消息都不会写回入参切片。
func TestProjectDoesNotMutateInput(t *testing.T) {
	messages := []Entry{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", Reasoning: "secret", Data: projAssistantWithCalls("", "c1").Data},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}
	snapshot := make([]Entry, len(messages))
	copy(snapshot, messages)

	// 触发截断 + 思考丢弃。
	Project(messages, 2, ProjectionPolicy{DropThinking: true, KeepRecentUser: true})
	// 触发配对保留 + 思考丢弃。
	Project(messages, 100, ProjectionPolicy{DropThinking: true})

	if !reflect.DeepEqual(messages, snapshot) {
		t.Fatalf("Project mutated input:\n got: %+v\nwant: %+v", messages, snapshot)
	}
}

// TestProjectDeterministic 验证同样的输入与策略得到完全一致的结果、动作和告警。
func TestProjectDeterministic(t *testing.T) {
	messages := []Entry{
		{Role: "user", Content: "old"},
		projAssistantWithCalls("calling", "c1"),
		{Role: "tool", ToolCallID: "c1", Content: "r1"},
		{Role: "tool", ToolCallID: "c1", Content: "r2"},
		{Role: "assistant", Content: "思考😀", Reasoning: "s"},
		{Role: "user", Content: "最新问题你好"},
	}
	policy := ProjectionPolicy{KeepRecentUser: true, KeepLiveToolTail: true, DropThinking: true}

	r1, a1, w1 := Project(messages, 7, policy)
	r2, a2, w2 := Project(messages, 7, policy)
	if !reflect.DeepEqual(r1, r2) {
		t.Fatalf("result not deterministic:\n%v\n%v", r1, r2)
	}
	if !reflect.DeepEqual(a1, a2) {
		t.Fatalf("actions not deterministic:\n%v\n%v", a1, a2)
	}
	if !reflect.DeepEqual(w1, w2) {
		t.Fatalf("warnings not deterministic:\n%v\n%v", w1, w2)
	}
}
