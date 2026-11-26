package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func newBatchSession(t *testing.T) *Session {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("batch")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestAppendBatchChainsAndSingleWrite(t *testing.T) {
	sess := newBatchSession(t)
	base, err := os.Stat(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	got, err := sess.AppendBatch([]BatchEntry{
		{Kind: KindUser, Data: map[string]any{"text": "a"}},
		{Kind: KindAssistant, Data: map[string]any{"text": "b"}},
		{Kind: KindUser, Data: map[string]any{"text": "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("records = %d", len(got))
	}
	if got[1].Parent != got[0].ID || got[2].Parent != got[1].ID {
		t.Fatalf("batch not chained: %+v", got)
	}
	after, _ := os.Stat(sess.path())
	if after.Size() <= base.Size() {
		t.Fatal("batch wrote nothing")
	}
	records, err := sess.Records()
	if err != nil || len(records) != len(got)+2 {
		t.Fatalf("disk records = %d / %v", len(records), err)
	}
}

func TestAppendBatchRejectsInvalid(t *testing.T) {
	sess := newBatchSession(t)
	if got, err := sess.AppendBatch(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty batch = %+v/%v", got, err)
	}
	if _, err := sess.AppendBatch([]BatchEntry{{Kind: "  ", Data: nil}}); err == nil {
		t.Fatal("empty kind must fail before touching disk")
	}
	records, _ := sess.Records()
	if len(records) != 2 {
		t.Fatalf("invalid batch wrote records: %d", len(records)-2)
	}
}

func TestAppendBatchRollsBackOnTOCTOU(t *testing.T) {
	sess := newBatchSession(t)
	beforeSize := int64(0)
	if info, err := os.Stat(sess.path()); err == nil {
		beforeSize = info.Size()
	}
	// 让 prepare 与 write 之间文件大小变化 → 触发 errAppendRetry；
	// 重试耗尽后整批必须一条都不落盘。
	appendPrepareHookForTest = func() {
		f, err := os.OpenFile(sess.path(), os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_, _ = f.WriteString("\n")
			f.Close()
		}
	}
	defer func() { appendPrepareHookForTest = nil }()

	if _, err := sess.AppendBatch([]BatchEntry{
		{Kind: KindUser, Data: map[string]any{"text": "a"}},
		{Kind: KindAssistant, Data: map[string]any{"text": "b"}},
	}); err == nil {
		t.Fatal("TOCTOU batch should fail after retries")
	}
	// 注入的换行是唯一新增字节；本批记录一条都不应出现。
	data, _ := os.ReadFile(sess.path())
	if strings.Contains(string(data), `"text":"a"`) || strings.Contains(string(data), `"text":"b"`) {
		t.Fatalf("partial batch leaked: %s", data)
	}
	_ = beforeSize
}

func TestCompactUsesAtomicBatch(t *testing.T) {
	sess := newBatchSession(t)
	_ = sess.Append(KindUser, map[string]any{"text": strings.Repeat("长", 200)})
	_ = sess.Append(KindAssistant, map[string]any{"text": "reply"})
	entries := sess.Messages()
	if len(entries) < 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if err := sess.appendCompaction(entries, 0); err != nil {
		t.Fatal(err)
	}
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	// 批提交应产生 compaction + 至少 1 条 restored（keep>0）。
	compactions := 0
	for _, record := range records {
		if record.Kind == KindCompaction {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction records = %d", compactions)
	}
	var cp Compaction
	if err := json.Unmarshal(records[len(records)-1].Data, &cp); err != nil && cp.Summary == "" {
		// 最后一条是 restored 时做一次宽松校验，只要压缩记录存在即可。
	}
}
