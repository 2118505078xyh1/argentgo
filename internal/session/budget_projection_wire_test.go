package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// appendWireToolCall 追加一条声明工具调用的 assistant 记录（activity 真实转录载荷）。
func appendWireToolCall(t *testing.T, sess *Session, content string, ids ...string) {
	t.Helper()
	type call struct {
		ID string `json:"id"`
	}
	calls := make([]call, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, call{ID: id})
	}
	data, err := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    content,
		"tool_calls": calls,
	})
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	// 必须显式转成 json.RawMessage：[]byte 会被 json.Marshal 编码成 base64 字符串。
	if _, err := sess.AppendRecord(KindAssistant, json.RawMessage(data)); err != nil {
		t.Fatalf("AppendRecord(assistant tool call): %v", err)
	}
}

// appendWireToolResult 追加一条工具结果记录。
func appendWireToolResult(t *testing.T, sess *Session, callID, content string) {
	t.Helper()
	if err := sess.Append(KindTool, map[string]any{
		"content":      content,
		"tool_call_id": callID,
	}); err != nil {
		t.Fatalf("Append(tool result): %v", err)
	}
}

// wireRawEntries 取出尚未投影的转录条目，供直接断言 Project 的动作与告警。
func wireRawEntries(t *testing.T, sess *Session) []Entry {
	t.Helper()
	records, err := sess.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	entries := make([]Entry, 0, len(records))
	for _, record := range records {
		if entry, ok := messageEntry(record); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// TestBudgetProjectionOffIsByteForByteBaseline 验证 opt-in 关闭时 Messages() 逐字节保持旧行为：
// 思考文本/签名原样保留、工具配对照旧协调、合成结果按 settle 规则生成。
func TestBudgetProjectionOffIsByteForByteBaseline(t *testing.T) {
	for _, value := range []string{"", "0", "false", "yes"} {
		t.Run("off_"+value, func(t *testing.T) {
			t.Setenv(budgetProjectionEnv, value)
			sess := newTestSession(t, t.TempDir(), "proj-off")
			if err := sess.Append(KindUser, "hello"); err != nil {
				t.Fatal(err)
			}
			if err := sess.Append(KindAssistant, AssistantRecordWithSignature("reply", "secret", "sig")); err != nil {
				t.Fatal(err)
			}
			appendWireToolCall(t, sess, "calling", "c1")
			appendWireToolResult(t, sess, "c1", "result")

			got := sess.Messages()
			if len(got) != 4 {
				t.Fatalf("len(Messages) = %d, want 4: %+v", len(got), got)
			}
			wantRoles := []string{"user", "assistant", "assistant", "tool"}
			wantContent := []string{"hello", "reply", "calling", "result"}
			for i := range got {
				if got[i].Role != wantRoles[i] || got[i].Content != wantContent[i] {
					t.Errorf("entry #%d = {%s, %q}, want {%s, %q}",
						i, got[i].Role, got[i].Content, wantRoles[i], wantContent[i])
				}
			}
			if got[1].Reasoning != "secret" || got[1].ReasoningSignature != "sig" {
				t.Fatalf("opt-in off must keep reasoning verbatim: %+v", got[1])
			}
			if got[3].ToolCallID != "c1" {
				t.Fatalf("tool result pairing changed: %+v", got[3])
			}
			// 重复读取必须完全一致，且底层 JSONL 不被改写。
			again := sess.Messages()
			if !reflect.DeepEqual(got, again) {
				t.Fatalf("repeated Messages() differs:\nfirst  %+v\nsecond %+v", got, again)
			}
		})
	}
}

// TestBudgetProjectionEnvOptInValues 验证只有 "1"（允许首尾空白）开启投影，
// 与其它 opt-in 变量的语义保持一致。
func TestBudgetProjectionEnvOptInValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"true", false}, {"yes", false},
	} {
		t.Setenv(budgetProjectionEnv, tc.value)
		if got := budgetProjectionEnabled(); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestBudgetProjectionSource 明确报告预算来源：拿不到 contextWindow/配置时，
// 预算来自固定环境变量或保守默认 32000，来源字符串可被审计。
func TestBudgetProjectionSource(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv(budgetProjectionBudgetEnv, "")
		budget, source := budgetProjectionBudget()
		if budget != defaultBudgetProjectionBudget || source != "default:32000" {
			t.Fatalf("budget/source = %d/%q, want %d/%q",
				budget, source, defaultBudgetProjectionBudget, "default:32000")
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv(budgetProjectionBudgetEnv, "4096")
		budget, source := budgetProjectionBudget()
		if budget != 4096 || source != "env:ARGENT_BUDGET_PROJECTION_BUDGET" {
			t.Fatalf("budget/source = %d/%q, want 4096/env", budget, source)
		}
	})
	for _, invalid := range []string{"0", "-5", "abc"} {
		t.Run("invalid_"+invalid, func(t *testing.T) {
			t.Setenv(budgetProjectionBudgetEnv, invalid)
			budget, source := budgetProjectionBudget()
			if budget != defaultBudgetProjectionBudget || source != "default:32000" {
				t.Fatalf("invalid budget %q -> %d/%q, want default", invalid, budget, source)
			}
		})
	}
}

// TestMessagesBudgetProjectionDropsWholePair 验证 opt-in 开启时 Messages() 按配对边界整对丢弃：
// assistant 调用与全部工具结果要么同留、要么同弃，切点不落在孤立 tool_result 上。
func TestMessagesBudgetProjectionDropsWholePair(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "1")
	t.Setenv(budgetProjectionBudgetEnv, "6") // 只够最后的 user "new"（3 rune）
	sess := newTestSession(t, t.TempDir(), "proj-pair")
	if err := sess.Append(KindUser, "old"); err != nil {
		t.Fatal(err)
	}
	appendWireToolCall(t, sess, "calling", "c1")
	appendWireToolResult(t, sess, "c1", "r1")
	appendWireToolResult(t, sess, "c1", "r2")
	if err := sess.Append(KindUser, "new"); err != nil {
		t.Fatal(err)
	}

	got := sess.Messages()
	if len(got) != 1 || got[0].Role != "user" || got[0].Content != "new" {
		t.Fatalf("projected Messages() = %+v, want only user/new", got)
	}
	// 配对的三条消息必须整对消失：既没有调用，也没有任何结果残留。
	for _, entry := range got {
		if entry.Role == "tool" || entry.Content == "calling" {
			t.Fatalf("pair member survived projection: %+v", entry)
		}
	}

	// 直接检查 Project 的动作：丢弃配对记为 drop_pair，而非逐条 drop_message。
	_, actions, _ := projectBudgetMessages(wireRawEntries(t, sess))
	if !hasProjectionActionKind(actions, ProjectionDropPair) {
		t.Fatalf("missing drop_pair action: %+v", actions)
	}
}

// TestMessagesBudgetProjectionKeepsLiveToolTail 验证未配对的活尾工具调用在超预算时仍被保留。
func TestMessagesBudgetProjectionKeepsLiveToolTail(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "1")
	t.Setenv(budgetProjectionBudgetEnv, "5")
	sess := newTestSession(t, t.TempDir(), "proj-tail")
	if err := sess.Append(KindUser, "u"); err != nil {
		t.Fatal(err)
	}
	appendWireToolCall(t, sess, "a", "done")
	appendWireToolResult(t, sess, "done", "res")
	appendWireToolCall(t, sess, "live", "open") // 未配对活尾

	got := sess.Messages()
	liveKept := false
	for _, entry := range got {
		if entry.Role == "assistant" && entry.Content == "live" {
			liveKept = true
		}
	}
	if !liveKept {
		t.Fatalf("live tool tail was dropped: %+v", got)
	}
	// 协调仍为活尾补齐合成结果，且结算写入原因；投影没有破坏 settle 链。
	foundSettled := false
	for _, entry := range got {
		if entry.Role == "tool" && entry.ToolCallID == "open" && isSyntheticAborted(entry) {
			foundSettled = true
		}
	}
	if !foundSettled {
		t.Fatalf("live tail lost its settled result: %+v", got)
	}
}

// TestMessagesBudgetProjectionRuneSafe 验证 opt-in 开启时按 rune 计费与 rune 安全截断：
// CJK/emoji 不会被切坏，含多字节字符的最新用户输入被保护但可在 rune 边界截断。
func TestMessagesBudgetProjectionRuneSafe(t *testing.T) {
	content := "你好世界😀🎉" // 6 个 rune，20 字节
	t.Setenv(budgetProjectionEnv, "1")
	t.Setenv(budgetProjectionBudgetEnv, "3")
	sess := newTestSession(t, t.TempDir(), "proj-rune")
	if err := sess.Append(KindUser, content); err != nil {
		t.Fatal(err)
	}

	got := sess.Messages()
	if len(got) != 1 {
		t.Fatalf("len(Messages) = %d, want 1: %+v", len(got), got)
	}
	truncated := got[0].Content
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncated content is invalid UTF-8: %q", truncated)
	}
	for _, r := range truncated {
		if r == utf8.RuneError {
			t.Fatalf("truncated content split a rune: %q", truncated)
		}
	}
	if want := string([]rune(content)[:3]); truncated != want {
		t.Fatalf("truncated = %q, want %q", truncated, want)
	}
	if utf8.RuneCountInString(truncated) != 3 {
		t.Fatalf("rune count = %d, want 3", utf8.RuneCountInString(truncated))
	}
	// 原始 JSONL 内容未被投影改写（读取路径会在最前面加一条 schema 头）。
	records, err := sess.Records()
	persisted := ""
	for _, record := range records {
		if record.Kind == KindUser {
			persisted = textOf(record.Data)
		}
	}
	if err != nil || persisted != content {
		t.Fatalf("projection mutated persisted transcript: persisted=%q err=%v", persisted, err)
	}
}

// TestMessagesBudgetProjectionUnreachableWarning 验证预算小到必须截断受保护消息时，
// Project 返回"预算不可达"告警而不是静默处理或 panic。Messages() 只消费 Messages 字段，
// 故告警通过它与 Messages() 共用的 projectBudgetMessages 入口断言。
func TestMessagesBudgetProjectionUnreachableWarning(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "1")
	t.Setenv(budgetProjectionBudgetEnv, "1") // 远小于受保护的 user 内容
	sess := newTestSession(t, t.TempDir(), "proj-warn")
	if err := sess.Append(KindUser, "hello world"); err != nil {
		t.Fatal(err)
	}

	result, actions, warnings := projectBudgetMessages(wireRawEntries(t, sess))
	if !result.Truncated {
		t.Fatal("expected truncation under unreachable budget")
	}
	if !hasProjectionWarningCode(warnings, ProjectionBudgetUnreachable) {
		t.Fatalf("missing unreachable-budget warning: %+v", warnings)
	}
	if !hasProjectionActionKind(actions, ProjectionTruncate) {
		t.Fatalf("missing truncate action: %+v", actions)
	}
	// 投影后的对话确实被截断，且仍能正常从 Messages() 读出。
	got := sess.Messages()
	if len(got) != 1 || utf8.RuneCountInString(got[0].Content) != 1 {
		t.Fatalf("Messages() under tiny budget = %+v, want one 1-rune user entry", got)
	}
}

// TestBudgetProjectionEnabledUsesProject 验证启用后预算真正参与投影：
// 相同会话在不同预算下得到不同的消息数量，证明接线不是空操作。
func TestBudgetProjectionEnabledUsesProject(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "proj-enabled")
	if err := sess.Append(KindUser, "u"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, strings.Repeat("长", 40)); err != nil {
		t.Fatal(err)
	}

	t.Setenv(budgetProjectionEnv, "1")
	t.Setenv(budgetProjectionBudgetEnv, "1000")
	roomy := len(sess.Messages())
	t.Setenv(budgetProjectionBudgetEnv, "5")
	tight := len(sess.Messages())
	if roomy != 2 || tight == roomy {
		t.Fatalf("budget not applied: roomy=%d tight=%d", roomy, tight)
	}
}
