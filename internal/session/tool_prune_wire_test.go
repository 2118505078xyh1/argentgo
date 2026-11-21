package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestToolPruneEnvOptInValues 验证只有 "1"（允许首尾空白）开启裁剪，
// 与其它 opt-in 变量的语义保持一致。
func TestToolPruneEnvOptInValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"true", false}, {"yes", false},
	} {
		t.Setenv(toolPruneEnv, tc.value)
		if got := toolPruneEnabled(); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestToolPruneOffIsByteForByteBaseline 验证 opt-in 关闭时 Messages() 逐字节保持旧行为：
// 超长工具结果原样保留（无裁剪标记）、思考文本原样保留、工具配对照旧协调。
// 同时确认底层 JSONL 不被改写、重复读取完全一致。
func TestToolPruneOffIsByteForByteBaseline(t *testing.T) {
	long := strings.Repeat("A", 40) + strings.Repeat("M", 40) + strings.Repeat("Z", 40)
	for _, value := range []string{"", "0", "false", "yes"} {
		t.Run("off_"+value, func(t *testing.T) {
			t.Setenv(toolPruneEnv, value)
			t.Setenv(budgetProjectionEnv, "0")
			t.Setenv(budgetProjectionBudgetEnv, "20") // 若误开裁剪，必然触发
			sess := newTestSession(t, t.TempDir(), "prune-off")
			if err := sess.Append(KindUser, "hello"); err != nil {
				t.Fatal(err)
			}
			if err := sess.Append(KindAssistant, AssistantRecordWithSignature("reply", "secret", "sig")); err != nil {
				t.Fatal(err)
			}
			appendWireToolCall(t, sess, "calling", "c1")
			appendWireToolResult(t, sess, "c1", long)

			got := sess.Messages()
			if len(got) != 4 {
				t.Fatalf("len(Messages) = %d, want 4: %+v", len(got), got)
			}
			if got[3].Role != "tool" || got[3].Content != long {
				t.Fatalf("opt-in off must keep tool result verbatim: %+v", got[3])
			}
			if strings.Contains(got[3].Content, pruneMarkerToken) {
				t.Fatal("prune marker present while opt-in off")
			}
			if got[1].Reasoning != "secret" || got[1].ReasoningSignature != "sig" {
				t.Fatalf("opt-in off must keep reasoning verbatim: %+v", got[1])
			}
			// 重复读取一致，且读取不会改写持久化的工具结果。
			again := sess.Messages()
			if !reflect.DeepEqual(got, again) {
				t.Fatalf("repeated Messages() differs:\nfirst  %+v\nsecond %+v", got, again)
			}
			assertPersistedToolContent(t, sess, "c1", long)
		})
	}
}

// TestToolPruneAloneShortensLongToolResult 验证单独开启裁剪（投影关闭）时，
// 超长工具结果被缩短并带上幂等标记，而投影特有的思考清理不发生——两个 opt-in 独立。
func TestToolPruneAloneShortensLongToolResult(t *testing.T) {
	t.Setenv(toolPruneEnv, "1")
	t.Setenv(budgetProjectionEnv, "0")
	t.Setenv(budgetProjectionBudgetEnv, "20")
	content := strings.Repeat("A", 20) + strings.Repeat("M", 40) + strings.Repeat("Z", 20)
	sess := newTestSession(t, t.TempDir(), "prune-alone")
	if err := sess.Append(KindAssistant, AssistantRecordWithSignature("reply", "secret", "sig")); err != nil {
		t.Fatal(err)
	}
	appendWireToolCall(t, sess, "calling", "c1")
	appendWireToolResult(t, sess, "c1", content)

	got := sess.Messages()
	var toolEntry *Entry
	for i := range got {
		if got[i].Role == "tool" && got[i].Content != "" && !isSyntheticAborted(got[i]) {
			toolEntry = &got[i]
		}
	}
	if toolEntry == nil {
		t.Fatalf("no real tool result in Messages(): %+v", got)
	}
	want := strings.Repeat("A", 10) + pruneMarkerText(60) + strings.Repeat("Z", 10)
	if toolEntry.Content != want {
		t.Fatalf("pruned tool content = %q, want %q", toolEntry.Content, want)
	}
	// 投影关闭：思考文本不应被清理，证明两个 opt-in 互不影响。
	foundReasoning := false
	for _, entry := range got {
		if entry.Role == "assistant" && entry.Reasoning == "secret" && entry.ReasoningSignature == "sig" {
			foundReasoning = true
		}
	}
	if !foundReasoning {
		t.Fatalf("projection-off reasoning was altered: %+v", got)
	}
	// 裁剪只作用于返回的对话视图，不触碰底层 JSONL。
	assertPersistedToolContent(t, sess, "c1", content)
}

// TestToolPruneStackedOrderPruneBeforeProject 验证两个 opt-in 叠加时顺序为
// prune → project → ReconcileToolPairs，而非 project → prune。
//
// 构造：一条超长的工具结果排在其调用者之前，调用者同时声明一个未配对的活尾 id，
// 于是 Project 的 KeepLiveToolTail 会把「工具结果 + 调用者」整对标记为受保护而保留，
// 再因超预算从最老消息（工具结果）头部做 rune 截断。两种顺序的结果不同：
//   - prune→project：工具结果先带裁剪标记，投影再截断其头部，标记仍留在结果中；
//   - project→prune：投影先把工具结果从头截短到不超过预算，prune 便不再触发，标记消失。
func TestToolPruneStackedOrderPruneBeforeProject(t *testing.T) {
	t.Setenv(toolPruneEnv, "1")
	t.Setenv(budgetProjectionEnv, "1")
	const budget = 60
	t.Setenv(budgetProjectionBudgetEnv, "60")

	long := strings.Repeat("A", 100)
	sess := newTestSession(t, t.TempDir(), "prune-project-order")
	// 工具结果在前，调用者在后（调用者声明 c1 与未配对的 open，是活尾）。
	appendWireToolResult(t, sess, "c1", long)
	appendWireToolCall(t, sess, "x", "c1", "open")

	raw := wireRawEntries(t, sess)
	// 先裁剪再投影的期望管线。
	pruned, prunedCount := PruneToolResults(raw, budget)
	if prunedCount == 0 {
		t.Fatal("prune did not trigger; test setup invalid")
	}
	projected, _, _ := Project(pruned, budget, defaultBudgetProjectionPolicy())
	want := SettleOrphanToolCalls(ReconcileToolPairs(projected.Messages), SettleInterrupted)

	got := sess.Messages()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Messages() != prune→project→reconcile:\ngot  %+v\nwant %+v", got, want)
	}
	// 标记确实存活：若顺序颠倒（先投影后裁剪），投影会先把结果截短到预算内，prune 不再触发。
	markerSurvived := false
	for _, entry := range got {
		if entry.Role == "tool" && strings.Contains(entry.Content, pruneMarkerToken) {
			markerSurvived = true
		}
	}
	if !markerSurvived {
		t.Fatalf("prune marker lost after stacking (wrong order?): %+v", got)
	}
	// 直接算一次逆序管线，确认在本场景下两者确实不同，从而证明上面的顺序断言有效。
	projectedFirst, _, _ := Project(raw, budget, defaultBudgetProjectionPolicy())
	prunedAfter, _ := PruneToolResults(projectedFirst.Messages, budget)
	wrong := SettleOrphanToolCalls(ReconcileToolPairs(prunedAfter), SettleInterrupted)
	if reflect.DeepEqual(got, wrong) {
		t.Fatal("prune→project and project→prune coincide; order test is vacuous")
	}
	assertPersistedToolContent(t, sess, "c1", long)
}

// TestToolPruneKeepsLiveToolTail 验证裁剪不会破坏未配对的活尾工具调用：
// 活尾 assistant 原样保留，协调仍为其补齐合成中止结果，只有真实工具结果被裁剪。
func TestToolPruneKeepsLiveToolTail(t *testing.T) {
	t.Setenv(toolPruneEnv, "1")
	t.Setenv(budgetProjectionEnv, "0")
	t.Setenv(budgetProjectionBudgetEnv, "20")
	long := strings.Repeat("A", 15) + strings.Repeat("M", 40) + strings.Repeat("Z", 15)
	sess := newTestSession(t, t.TempDir(), "prune-tail")
	if err := sess.Append(KindUser, "u"); err != nil {
		t.Fatal(err)
	}
	appendWireToolCall(t, sess, "calling", "c1")
	appendWireToolResult(t, sess, "c1", long)
	appendWireToolCall(t, sess, "live", "open") // 未配对活尾

	got := sess.Messages()
	liveKept := false
	syntheticKept := false
	realPruned := false
	for _, entry := range got {
		if entry.Role == "assistant" && entry.Content == "live" {
			liveKept = true
		}
		if entry.Role == "tool" && entry.ToolCallID == "open" && isSyntheticAborted(entry) {
			syntheticKept = true
		}
		if entry.Role == "tool" && entry.ToolCallID == "c1" &&
			strings.Contains(entry.Content, pruneMarkerToken) {
			realPruned = true
		}
	}
	if !liveKept {
		t.Fatalf("live tool tail was broken by prune: %+v", got)
	}
	if !syntheticKept {
		t.Fatalf("live tail lost its settled result: %+v", got)
	}
	if !realPruned {
		t.Fatalf("long tool result was not pruned: %+v", got)
	}
}

// TestToolPruneRuneSafeCJK 验证接线后裁剪仍是 rune 安全的：
// CJK/emoji 不会被切坏，标记中的 N 等于被裁掉的 rune 数。
func TestToolPruneRuneSafeCJK(t *testing.T) {
	t.Setenv(toolPruneEnv, "1")
	t.Setenv(budgetProjectionEnv, "0")
	t.Setenv(budgetProjectionBudgetEnv, "9")
	content := "界世界🙂🚀" + strings.Repeat("中", 20) + "文🙂🚀界" // 29 rune
	sess := newTestSession(t, t.TempDir(), "prune-cjk")
	appendWireToolCall(t, sess, "calling", "c1")
	appendWireToolResult(t, sess, "c1", content)

	got := sess.Messages()
	var pruned string
	for _, entry := range got {
		if entry.Role == "tool" && strings.Contains(entry.Content, pruneMarkerToken) {
			pruned = entry.Content
		}
	}
	if pruned == "" {
		t.Fatalf("no pruned tool result: %+v", got)
	}
	if !utf8.ValidString(pruned) || strings.ContainsRune(pruned, utf8.RuneError) {
		t.Fatalf("pruned content split a rune: %q", pruned)
	}
	runes := []rune(content)
	const max = 9
	head, tail := max/2, max-max/2
	want := string(runes[:head]) + pruneMarkerText(len(runes)-head-tail) + string(runes[len(runes)-tail:])
	if pruned != want {
		t.Fatalf("pruned content = %q, want %q", pruned, want)
	}
	assertPersistedToolContent(t, sess, "c1", content)
}

// assertPersistedToolContent 确认读取路径从未把投影/裁剪结果写回 JSONL。
func assertPersistedToolContent(t *testing.T, sess *Session, callID, want string) {
	t.Helper()
	records, err := sess.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	for _, record := range records {
		if record.Kind != KindTool {
			continue
		}
		if id := firstNonEmpty(fieldString(record.Data, "toolCallId", "tool_call_id"),
			fieldString(record.Data, "id")); id != callID {
			continue
		}
		if got := firstNonEmpty(fieldString(record.Data, "content"),
			fieldString(record.Data, "output"), fieldString(record.Data, "result")); got != want {
			t.Fatalf("persisted tool content mutated: got %q, want %q", got, want)
		}
		return
	}
	t.Fatalf("no persisted tool record found for %q", callID)
}
