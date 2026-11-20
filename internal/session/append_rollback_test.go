package session

import (
	"os"
	"strings"
	"testing"
)

// 未能写入 JSONL 文件的操作，不能让内存中的
// 追加索引领先于磁盘：生成的 id 保持未使用，活动叶节点
// 仍指向实际写入的最后一条记录，且下一次
// 追加可以复用该 id 并无间隙地延续链条。
func TestAppendRecordRollsBackIndexOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "rollback")

	// 用一次成功的追加预热缓存的追加状态。
	if err := session.Append(KindUser, "before"); err != nil {
		t.Fatalf("warm Append: %v", err)
	}
	before, err := session.Records()
	if err != nil {
		t.Fatalf("Records before: %v", err)
	}
	lastBefore := before[len(before)-1].ID
	nextBefore := nextID(before)

	path := session.path()
	// 在保持追加 fd 打开的平台 上，关闭它而不清除
	// 缓存的 stat 快照会让状态保持"有效"，使失败发生在
	// writeAppend 中（而不是更早的全量读取重建中）。仅 chmod 不会
	// 阻止通过文件变为只读前已打开的句柄写入。
	if keepAppendFD && session.fd != nil {
		if err := session.fd.Close(); err != nil {
			t.Fatalf("close append fd: %v", err)
		}
		session.fd = nil
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if err := session.Append(KindAssistant, "must not be written"); err == nil {
		t.Fatal("Append to a read-only session file succeeded, want write failure")
	}

	afterFail, err := session.Records()
	if err != nil {
		t.Fatalf("Records after failure: %v", err)
	}
	if len(afterFail) != len(before) {
		t.Fatalf("records after failed append = %d, want %d", len(afterFail), len(before))
	}
	for _, record := range afterFail {
		if strings.Contains(string(record.Data), "must not be written") {
			t.Fatalf("failed append leaked into the transcript: %+v", record)
		}
	}
	for i := 1; i < len(afterFail); i++ {
		if afterFail[i].Parent != afterFail[i-1].ID {
			t.Fatalf("record %d parent = %q, want %q", i, afterFail[i].Parent, afterFail[i-1].ID)
		}
	}

	// 恢复写权限。下一次追加必须成功，复用失败追加所生成的
	// id（无间隙），并链接到同一个未改变的叶节点。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod writable: %v", err)
	}
	recovered, err := session.AppendRecord(KindAssistant, "after recovery")
	if err != nil {
		t.Fatalf("AppendRecord after recovery: %v", err)
	}
	if recovered.ID != nextBefore {
		t.Fatalf("recovered id = %q, want %q (gap left by failed append)", recovered.ID, nextBefore)
	}
	if recovered.Parent != lastBefore {
		t.Fatalf("recovered parent = %q, want %q", recovered.Parent, lastBefore)
	}

	final, err := session.Records()
	if err != nil {
		t.Fatalf("final Records: %v", err)
	}
	if len(final) != len(before)+1 {
		t.Fatalf("final records = %d, want %d", len(final), len(before)+1)
	}
	last := final[len(final)-1]
	if last.ID != recovered.ID || last.Parent != lastBefore {
		t.Fatalf("final record = %+v, want id %q parent %q", last, recovered.ID, lastBefore)
	}
}
