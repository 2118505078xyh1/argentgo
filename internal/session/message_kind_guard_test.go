package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 本文件是“记录 kind 守卫”测试：随着 R33/R38 等轮次引入 stats/usage/condensation
// 等新 kind，messageEntries()/Messages() 必须继续把它们当作元数据忽略，绝不把它们的
// 文本当作对话内容喂给模型。summary 与 compaction 是例外：它们会被有意投影为
// Role="system" 的消息（见 TestCompactionAndSummaryKindsProjection），因此单独验证。

// kindMarker 描述一种非对话 kind 及其独有的、绝不应当出现在模型可见对话中的文本。
// 文本包含 CJK、日文假名、emoji 与字形组合，用于同时守住多字节内容不被破坏。
type kindMarker struct {
	kind   string
	marker string
}

// nonConversationMarkers 列出当前所有已知的非对话 kind。新增 kind 时应当把它加入本表，
// 这样守卫测试会立即覆盖它。
func nonConversationMarkers(prefix string) []kindMarker {
	return []kindMarker{
		{KindHeader, prefix + "头部元数据【不该出现】"},
		{KindSchema, prefix + "模式头【不该出现】"},
		{KindCheckpoint, prefix + "检查点【不该出现】"},
		{KindAttempt, prefix + "重试标记【不该出现】"},
		{KindTitle, prefix + "标题元数据【不该出现】"},
		{kindStats, prefix + "统计汇总📊【不该出现】"},
		{kindUsage, prefix + "用量记录📈【不该出现】"},
		{kindProgress, prefix + "进度检查点⏳【不该出现】"},
		{KindCondensation, prefix + "压缩事件🗜️【不该出现】"},
		{"custom_unknown_kind", prefix + "未知开放 kind🌍【不该出现】"},
	}
}

// guardData 为元数据记录构造载荷：把标记塞进 messageEntry 可能读取的所有文本键，
// 使任何意外的投影都会把标记带进对话，从而被断言捕获。
func guardData(marker string) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"content": marker,
		"text":    marker,
		"summary": marker,
		"output":  marker,
		"message": marker,
		"result":  marker,
	})
	if err != nil {
		panic(err)
	}
	return data
}

// guardConversation 描述一条应当出现在对话中的记录。
type guardConversation struct {
	id      string
	kind    string
	role    string // 仅 KindMessage 使用；为空时由 messageEntry 推断为 user
	content string
}

// conversationMarkers 是混入元数据中的正常对话，用于验证顺序与多字节内容都保持正确。
func conversationMarkers(prefix string) []guardConversation {
	return []guardConversation{
		{"c1", KindUser, "", prefix + "用户一：你好，世界 🌏"},
		{"c2", KindAssistant, "", prefix + "助手一：收到 ✅"},
		{"c3", KindMessage, "user", prefix + "消息：こんにちは"},
		{"c4", KindTool, "", prefix + "工具结果：文件已读 📄"},
		{"c5", KindAssistant, "", prefix + "助手二：完成 🎉"},
		{"c6", KindUser, "", prefix + "用户二：教師 emulation で終了"},
	}
}

// buildGuardRecords 把元数据与对话交替排成一条 id/parent 链，返回记录与期望的对话 ID。
// 交替排列能捕获“只忽略开头/结尾元数据”这类部分实现。
func buildGuardRecords(prefix string) ([]Record, []string) {
	meta := nonConversationMarkers(prefix)
	conv := conversationMarkers(prefix)
	records := make([]Record, 0, len(meta)+len(conv))
	wantIDs := make([]string, 0, len(conv))
	parent := ""
	idSeq := 0
	nextID := func() string {
		idSeq++
		return fmt.Sprintf("g%02d", idSeq)
	}
	add := func(kind string, data json.RawMessage) {
		id := nextID()
		records = append(records, Record{ID: id, Parent: parent, Kind: kind, Data: data})
		parent = id
	}
	// 交替添加元数据与对话；元数据数量可能多于对话，多出的部分会落在尾部，
	// 因而同时覆盖“元数据出现在对话之前、之间、之后”三种位置。
	for i := 0; i < len(meta) || i < len(conv); i++ {
		if i < len(meta) {
			add(meta[i].kind, guardData(meta[i].marker))
		}
		if i < len(conv) {
			c := conv[i]
			data, err := json.Marshal(map[string]any{"content": c.content, "role": c.role})
			if err != nil {
				panic(err)
			}
			add(c.kind, data)
			wantIDs = append(wantIDs, records[len(records)-1].ID)
		}
	}
	// 再放一条带不同文本的元数据，确保尾部元数据同样被忽略。
	last := meta[len(meta)-1]
	add(last.kind, guardData(last.marker+"-尾部"))
	return records, wantIDs
}

// assertMarkersAbsent 断言所有元数据标记都没有泄漏进投影结果的任何字段。
func assertMarkersAbsent(t *testing.T, entries []Entry, markers []kindMarker) {
	t.Helper()
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		for _, m := range markers {
			if strings.Contains(string(encoded), m.marker) {
				t.Errorf("kind %q 的标记 %q 泄漏进对话 Entry: %s", m.kind, m.marker, encoded)
			}
			// 尾部变体也要检查（buildGuardRecords 在末尾追加了一条）。
			if strings.Contains(string(encoded), m.marker+"-尾部") {
				t.Errorf("kind %q 的尾部标记泄漏进对话 Entry: %s", m.kind, encoded)
			}
		}
	}
}

// TestMessageEntriesIgnoreAllNonConversationKinds 直接守卫 messageEntries()：
// 所有非对话 kind 的载荷都必须被丢弃，只留下对话记录，且保持 id/parent 顺序与 CJK 原文。
func TestMessageEntriesIgnoreAllNonConversationKinds(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "")
	t.Setenv(toolPruneEnv, "")

	records, wantIDs := buildGuardRecords("投影-")
	entries := messageEntries(records)

	if len(entries) != len(wantIDs) {
		t.Fatalf("messageEntries 返回 %d 条，期望 %d 条：%+v", len(entries), len(wantIDs), entries)
	}
	for i, wantID := range wantIDs {
		if entries[i].ID != wantID {
			t.Errorf("entry[%d].ID = %q，期望 %q（顺序异常）", i, entries[i].ID, wantID)
		}
	}
	wantContents := conversationMarkers("投影-")
	for i, c := range wantContents {
		if entries[i].Content != c.content {
			t.Errorf("entry[%d].Content = %q，期望 %q（多字节内容被破坏）", i, entries[i].Content, c.content)
		}
		wantRole := c.role
		if wantRole == "" {
			wantRole = "user"
			if c.kind == KindAssistant {
				wantRole = "assistant"
			} else if c.kind == KindTool {
				wantRole = "tool"
			}
		}
		if entries[i].Role != wantRole {
			t.Errorf("entry[%d].Role = %q，期望 %q", i, entries[i].Role, wantRole)
		}
	}
	assertMarkersAbsent(t, entries, nonConversationMarkers("投影-"))
}

// TestMessagesIgnoreNonConversationKinds 通过真实 Session 守卫 Messages()：
// 它经由 activePath 按 id/parent 树取活动分支，再交给 messageEntries，
// 因此元数据不得从这条完整链路上泄漏。
func TestMessagesIgnoreNonConversationKinds(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "")
	t.Setenv(toolPruneEnv, "")

	prefix := "集成-"
	sess := newGuardSession(t, prefix+"标题元数据【不该出现】")
	records, _ := buildGuardRecords(prefix)
	for _, record := range records {
		data := record.Data
		if err := sess.Append(record.Kind, data); err != nil {
			t.Fatalf("Append(%q): %v", record.Kind, err)
		}
	}

	entries := sess.Messages()
	wantContents := conversationMarkers(prefix)
	if len(entries) != len(wantContents) {
		t.Fatalf("Messages 返回 %d 条，期望 %d 条：%+v", len(entries), len(wantContents), entries)
	}
	for i, c := range wantContents {
		if entries[i].Content != c.content {
			t.Errorf("entry[%d].Content = %q，期望 %q", i, entries[i].Content, c.content)
		}
	}
	withTitle := append(nonConversationMarkers(prefix), kindMarker{KindHeader, prefix + "标题元数据【不该出现】"})
	assertMarkersAbsent(t, entries, withTitle)
}

// TestCompactionAndSummaryKindsProjection 记录 summary/compaction 的有意例外：
// summary 在 messageEntries 中投影为 system；compaction 在 messageEntries 中被忽略，
// 但 Messages() 只把最新一条 compaction 投影为 system 摘要。二者都只允许“摘要文本”
// 进入对话，condensation/stats 等元数据仍须完全不可见。
func TestCompactionAndSummaryKindsProjection(t *testing.T) {
	t.Setenv(budgetProjectionEnv, "")
	t.Setenv(toolPruneEnv, "")

	const (
		summaryText      = "摘要正文【有意投影】"
		compactionText   = "系统压缩摘要【有意投影】"
		condenseMarker   = "压缩事件标记🗜️【不该出现】"
		statsMarker      = "统计标记📊【不该出现】"
		oldUserText      = "旧消息【被压缩替换】"
		newAssistantText = "新消息：こんにちは 👋"
	)

	// 1) 直接投影：summary -> system；compaction/condensation/stats -> 忽略。
	summaryData, _ := json.Marshal(map[string]any{"text": summaryText})
	compactionData, _ := json.Marshal(Compaction{Summary: "不应被直接投影", Keep: 0})
	direct := messageEntries([]Record{
		{Kind: KindSummary, Data: summaryData, ID: "s1"},
		{Kind: KindCompaction, Data: compactionData, ID: "k1"},
		{Kind: KindCondensation, Data: guardData(condenseMarker), ID: "d1"},
		{Kind: kindStats, Data: guardData(statsMarker), ID: "t1"},
	})
	if len(direct) != 1 || direct[0].Role != "system" || direct[0].Content != summaryText {
		t.Fatalf("summary 直接投影 = %+v，期望单条 system(%q)", direct, summaryText)
	}

	// 2) 真实会话：最新 compaction 生成 system 摘要，condensation/stats 仍被忽略。
	sess := newGuardSession(t, "例外测试")
	if err := sess.Append(KindUser, oldUserText); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	if err := sess.Append(KindCompaction, Compaction{Summary: compactionText, Keep: 0, Dropped: 1}); err != nil {
		t.Fatalf("Append(compaction): %v", err)
	}
	if err := sess.Append(KindCondensation, guardData(condenseMarker)); err != nil {
		t.Fatalf("Append(condensation): %v", err)
	}
	if err := sess.Append(kindStats, guardData(statsMarker)); err != nil {
		t.Fatalf("Append(stats): %v", err)
	}
	if err := sess.Append(KindAssistant, newAssistantText); err != nil {
		t.Fatalf("Append(assistant): %v", err)
	}

	entries := sess.Messages()
	if len(entries) != 2 {
		t.Fatalf("Messages 返回 %d 条，期望 [system, assistant]：%+v", len(entries), entries)
	}
	if entries[0].Role != "system" || entries[0].Content != compactionText {
		t.Errorf("entry[0] = %+v，期望 system(%q)", entries[0], compactionText)
	}
	if entries[1].Role != "assistant" || entries[1].Content != newAssistantText {
		t.Errorf("entry[1] = %+v，期望 assistant(%q)", entries[1], newAssistantText)
	}
	assertMarkersAbsent(t, entries, []kindMarker{
		{KindCondensation, condenseMarker},
		{kindStats, statsMarker},
	})
	if strings.Contains(string(mustMarshalEntries(t, entries)), oldUserText) {
		t.Error("Keep=0 的压缩摘要仍泄漏了被替换的旧消息")
	}
}

// TestKnownSessionKindValuesDistinct 断言所有已知 kind 的取值互不冲突。
// session 的 kind 是开放字符串常量（见 session.go），不是 iota 枚举，
// 因而无法通过反射枚举；这张显式表就是等价的防冲突守卫。
func TestKnownSessionKindValuesDistinct(t *testing.T) {
	all := []string{
		KindHeader, KindMessage, KindUser, KindAssistant, KindTool,
		KindSummary, KindCompaction, KindCheckpoint, KindAttempt, KindTitle,
		KindSchema, KindCondensation, kindStats, kindUsage, kindProgress,
		KindRequestSnapshot,
	}
	seen := make(map[string]string, len(all))
	for _, kind := range all {
		if kind == "" {
			t.Fatal("存在空 kind 常量")
		}
		if prev, ok := seen[kind]; ok {
			t.Errorf("kind 取值冲突：%q 被多个常量使用（前一个是 %q）", kind, prev)
		}
		seen[kind] = kind
	}

	// R33/R38 新增的元数据 kind 不得与任何对话 kind 撞值。
	conversation := []string{KindMessage, KindUser, KindAssistant, KindTool, KindSummary, KindCompaction}
	for _, newKind := range []string{KindCondensation, kindStats, kindUsage, kindProgress, KindRequestSnapshot} {
		for _, oldKind := range conversation {
			if newKind == oldKind {
				t.Errorf("新 kind %q 与对话 kind %q 冲突", newKind, oldKind)
			}
		}
	}
}

// newGuardSession 创建一个带 CJK 标题的测试会话，标题本身也是一条不该进入对话的元数据。
func newGuardSession(t *testing.T, title string) *Session {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sess, err := store.NewSession(title)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

func mustMarshalEntries(t *testing.T, entries []Entry) []byte {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	return data
}
