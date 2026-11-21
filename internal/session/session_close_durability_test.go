package session

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

// TestClosePersistsDataForReopen 验证 Close 之后重新打开会话读到的记录与关闭前
// 完全一致：默认路径的磁盘字节不被 Close 改动，批量写模式下暂存数据先落盘再同步。
func TestClosePersistsDataForReopen(t *testing.T) {
	payloads := []string{"first", "second", "last assistant turn"}
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			dir := t.TempDir()
			sess := newTestSession(t, dir, "persist")
			if buffered {
				if err := sess.SetBufferedWrite(true); err != nil {
					t.Fatalf("SetBufferedWrite: %v", err)
				}
			}
			path := sess.path()
			for _, payload := range payloads {
				if err := sess.Append(KindAssistant, payload); err != nil {
					t.Fatalf("Append(%q): %v", payload, err)
				}
			}

			before, err := sess.Records()
			if err != nil {
				t.Fatalf("Records before Close: %v", err)
			}
			beforeBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile before Close: %v", err)
			}

			if err := sess.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := sess.PendingBytes(); got != 0 {
				t.Fatalf("PendingBytes after Close = %d, want 0", got)
			}
			afterBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile after Close: %v", err)
			}
			if !buffered && !bytes.Equal(beforeBytes, afterBytes) {
				t.Fatal("Close changed bytes on the default path")
			}

			// 模拟干净退出后重新打开：记录必须与关闭前逐字段一致。
			reopened, err := OpenSession(dir, sess.ID())
			if err != nil {
				t.Fatalf("OpenSession after Close: %v", err)
			}
			after, err := reopened.Records()
			if err != nil {
				t.Fatalf("Records after reopen: %v", err)
			}
			if fmt.Sprint(recordShapes(before)) != fmt.Sprint(recordShapes(after)) {
				t.Fatalf("records changed after reopen:\n before = %v\n after  = %v",
					recordShapes(before), recordShapes(after))
			}
			assertLinearChain(t, after)
		})
	}
}

// TestCloseInvokesSync 验证 Close 在关闭前恰好调用一次持久性屏障。
// 通过临时替换 sessionSync 统计次数；测试结束后务必恢复，避免影响其它用例。
func TestCloseInvokesSync(t *testing.T) {
	original := sessionSync
	defer func() { sessionSync = original }()

	var calls atomic.Int32
	sessionSync = func(s *Session) error {
		calls.Add(1)
		return original(s)
	}

	sess := newTestSession(t, t.TempDir(), "close sync")
	if err := sess.Append(KindAssistant, "tail"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Sync calls = %d, want exactly 1", got)
	}
}

// TestClosePropagatesSyncError 确认 Close 把 Sync 的错误交给调用方，
// 证明 Sync 位于关闭路径上且错误不会被吞掉。
func TestClosePropagatesSyncError(t *testing.T) {
	original := sessionSync
	defer func() { sessionSync = original }()

	wantErr := fmt.Errorf("injected sync failure")
	sessionSync = func(s *Session) error { return wantErr }

	sess := newTestSession(t, t.TempDir(), "close sync error")
	err := sess.Close()
	if err == nil {
		t.Fatal("Close swallowed the Sync error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte(wantErr.Error())) {
		t.Fatalf("Close error = %q, want it to wrap %q", err, wantErr)
	}
	// 即使同步失败也要释放追加 fd（Windows 上会阻塞临时目录清理）。
	if sess.fd != nil {
		t.Fatal("Close left the append fd open after a Sync error")
	}
}

// TestCloseConcurrentSafe 验证多个 goroutine 同时 Close 不会 panic、不会报错，
// 且重新打开后数据仍是一条完整、无重复的链。
func TestCloseConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "concurrent close")
	for i := 0; i < 5; i++ {
		if err := sess.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- sess.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatalf("OpenSession after concurrent Close: %v", err)
	}
	records, err := reopened.Records()
	if err != nil {
		t.Fatalf("Records after concurrent Close: %v", err)
	}
	assertLinearChain(t, records)
	seen := make(map[string]int, 5)
	for _, record := range records[2:] { // 跳过合成 schema 记录与 header
		seen[string(record.Data)]++
	}
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("%q", fmt.Sprintf("m%d", i))
		if seen[key] != 1 {
			t.Fatalf("payload %s appeared %d times, want 1", key, seen[key])
		}
	}
}

// TestCloseDoesNotChangeCrashRecovery 验证新增的 Close 同步不改变崩溃恢复语义：
// 日志未被改写，ValidateAndRepair 报告无损坏，进度恢复结果与关闭前一致。
func TestCloseDoesNotChangeCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "recovery")
	if err := sess.Append(KindUser, "do work"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	want := Progress{Turn: 1, AssistantText: "half streamed"}
	if err := sess.AppendProgress(want); err != nil {
		t.Fatalf("AppendProgress: %v", err)
	}

	// 模拟崩溃：不 Close，直接从磁盘重建会话。
	crashView, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatalf("OpenSession (crash): %v", err)
	}
	crashProgress, crashOK := RecoverProgress(crashView)
	if !crashOK || crashProgress.AssistantText != want.AssistantText {
		t.Fatalf("RecoverProgress (crash) = %+v, %v", crashProgress, crashOK)
	}
	before, err := sess.Records()
	if err != nil {
		t.Fatalf("Records before Close: %v", err)
	}

	// 干净 Close 后再重建，恢复结果与崩溃路径一致。
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closedView, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatalf("OpenSession (closed): %v", err)
	}
	closedProgress, closedOK := RecoverProgress(closedView)
	if !closedOK || closedProgress.AssistantText != want.AssistantText {
		t.Fatalf("RecoverProgress (closed) = %+v, %v", closedProgress, closedOK)
	}
	after, err := closedView.Records()
	if err != nil {
		t.Fatalf("Records after Close: %v", err)
	}
	if fmt.Sprint(recordShapes(before)) != fmt.Sprint(recordShapes(after)) {
		t.Fatalf("Close changed the transcript:\n before = %v\n after  = %v",
			recordShapes(before), recordShapes(after))
	}

	report, err := ValidateAndRepair(sess.path(), false)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || report.Repaired || report.KeptLines != report.PhysicalLines {
		t.Fatalf("Close produced a damaged transcript: %+v", report)
	}
}
