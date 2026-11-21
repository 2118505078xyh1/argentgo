package session

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// newCompactionEventSession 创建只含 header 与若干 user 消息的会话，供压缩事件接线测试复用。
func newCompactionEventSession(t *testing.T, contents ...string) *Session {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("compaction-event")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range contents {
		if err := s.Append(KindUser, content); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestCompactionEventOffLeavesBytesUnchanged 验证 opt-in 关闭时逐字节基线不变：
// 压缩只在原文件末尾追加一条压缩记录，原有字节一字不改，且不会写入压缩事件记录。
func TestCompactionEventOffLeavesBytesUnchanged(t *testing.T) {
	t.Setenv(compactionEventEnv, "") // 空值视为关闭
	s := newCompactionEventSession(t,
		"目标：构建解析器",
		"补充上下文",
		"第三步",
		"继续",
		"完成",
	)
	before, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(2); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}

	// 原字节必须是 after 的逐字前缀。
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("opt-in 关闭时改动了已有字节：\nbefore %q\nafter  %q", before, after)
	}
	suffix := after[len(before):]
	lines := bytes.Split(bytes.TrimSuffix(suffix, []byte("\n")), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("opt-in 关闭时追加了 %d 行，want 1：%q", len(lines), suffix)
	}
	var rec Record
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Kind != KindCompaction {
		t.Fatalf("opt-in 关闭时追加的记录 kind = %q, want %q", rec.Kind, KindCompaction)
	}

	// 整个文件中都不应出现压缩事件记录。
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind == KindCondensation {
			t.Fatalf("opt-in 关闭时仍写入了 %q 记录", KindCondensation)
		}
	}
}

// TestCompactionEventOnAppendsRecord 验证 opt-in 开启时在压缩记录之后追加事件记录，
// 载荷的 forgotten ids / summary / offset 正确，CJK 原样、可往返，且不进入模型可见消息。
func TestCompactionEventOnAppendsRecord(t *testing.T) {
	t.Setenv(compactionEventEnv, "1")
	s := newCompactionEventSession(t,
		"目标：构建中文解析器",
		"补充上下文",
		"第三步",
		"继续",
		"完成",
	)
	beforeMessages := s.Messages()
	if err := s.Compact(2); err != nil {
		t.Fatal(err)
	}
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}

	// 事件记录必须紧跟在压缩记录之后。
	compactionIdx := -1
	for i, record := range records {
		if record.Kind == KindCompaction {
			compactionIdx = i
		}
	}
	if compactionIdx < 0 {
		t.Fatal("未找到压缩记录")
	}
	if compactionIdx+1 >= len(records) {
		t.Fatal("压缩记录后没有事件记录")
	}
	eventRecord := records[compactionIdx+1]
	if eventRecord.Kind != KindCondensation {
		t.Fatalf("压缩记录后的 kind = %q, want %q", eventRecord.Kind, KindCondensation)
	}

	var compaction Compaction
	if err := json.Unmarshal(records[compactionIdx].Data, &compaction); err != nil {
		t.Fatal(err)
	}
	event, err := ParseCondensationEvent(eventRecord.Data)
	if err != nil {
		t.Fatal(err)
	}

	// summary 与 offset 对齐：offset 取被丢弃前缀长度（摘要在会话中的位置）。
	if event.Summary != compaction.Summary {
		t.Fatalf("event.Summary 与压缩摘要不一致：\nevent  %q\ncompact %q", event.Summary, compaction.Summary)
	}
	if event.Offset != compaction.Dropped {
		t.Fatalf("event.Offset = %d, want %d (compaction.Dropped)", event.Offset, compaction.Dropped)
	}

	// forgotten ids：被丢弃前缀的全部记录 ID（去重排序由 NewCondensationEvent 完成）。
	wantIDs := make([]string, 0, compaction.Dropped)
	for _, message := range beforeMessages[:compaction.Dropped] {
		if message.ID != "" {
			wantIDs = append(wantIDs, message.ID)
		}
	}
	sort.Strings(wantIDs)
	if !reflect.DeepEqual(event.ForgottenIDs, wantIDs) {
		t.Fatalf("ForgottenIDs = %q, want %q", event.ForgottenIDs, wantIDs)
	}

	// CJK 原样输出，不转义为 \uXXXX。
	if !bytes.Contains(eventRecord.Data, []byte("中文解析器")) {
		t.Errorf("落盘事件载荷缺少 CJK 原文：%s", eventRecord.Data)
	}
	if bytes.Contains(eventRecord.Data, []byte(`\u`)) {
		t.Errorf("落盘事件载荷含 \\u 转义，want 原始 UTF-8：%s", eventRecord.Data)
	}

	// 往返：从落盘字节解析后再 Marshal，应与落盘字节逐字一致。
	if !bytes.Equal(event.Marshal(), eventRecord.Data) {
		t.Errorf("事件往返字节不一致：\n got %s\nwant %s", event.Marshal(), eventRecord.Data)
	}

	// messageEntries 忽略该 kind：有/无事件记录，投影出的消息完全一致。
	without := append([]Record(nil), records[:compactionIdx+1]...)
	without = append(without, records[compactionIdx+2:]...)
	if got, want := messageEntries(records), messageEntries(without); !reflect.DeepEqual(got, want) {
		t.Fatalf("压缩事件泄漏进消息投影：\n got %+v\nwant %+v", got, want)
	}
	if entry, ok := messageEntry(eventRecord); ok {
		t.Fatalf("messageEntry 未忽略压缩事件：%+v", entry)
	}
}

// TestCompactionEventDoesNotChangeCompactionRecord 验证 opt-in 开关不影响压缩记录本身的字节。
func TestCompactionEventDoesNotChangeCompactionRecord(t *testing.T) {
	contents := []string{"目标：构建解析器", "补充上下文", "第三步", "继续", "完成"}
	compactionData := func(env string) []byte {
		t.Setenv(compactionEventEnv, env)
		s := newCompactionEventSession(t, contents...)
		if err := s.Compact(2); err != nil {
			t.Fatal(err)
		}
		records, err := s.Records()
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Kind == KindCompaction {
				return append([]byte(nil), record.Data...)
			}
		}
		t.Fatal("未找到压缩记录")
		return nil
	}
	off := compactionData("")
	on := compactionData("1")
	if !bytes.Equal(off, on) {
		t.Fatalf("opt-in 改变了压缩记录字节：\noff %s\non  %s", off, on)
	}
}
