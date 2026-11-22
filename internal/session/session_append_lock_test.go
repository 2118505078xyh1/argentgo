package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setAppendPrepareHookForTest 注入 prepare→write 之间的测试接缝，并在测试结束
// 时恢复原值。它与 readRecordsFn/sessionSync 一样只在本包内使用。
func setAppendPrepareHookForTest(t *testing.T, hook func()) {
	t.Helper()
	previous := appendPrepareHookForTest
	appendPrepareHookForTest = hook
	t.Cleanup(func() { appendPrepareHookForTest = previous })
}

// rawAppendRecord 绕过 Session 直接向 transcript 追加一条合法 JSONL 记录，
// 用于模拟"不遵守跨进程锁的旧写入方"在背后改动文件。
func rawAppendRecord(t *testing.T, path, id, parent string) {
	t.Helper()
	record := Record{
		ID:     id,
		Parent: parent,
		TS:     nextRecordTS(),
		Kind:   KindUser,
		Data:   json.RawMessage(`"behind-the-back"`),
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// assertUniqueIDs 断言磁盘上的记录 id 两两不同（跳过 Records 合成的第一条 schema 记录）。
func assertUniqueIDs(t *testing.T, path string) []Record {
	t.Helper()
	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if seen[record.ID] {
			t.Errorf("duplicate id %q on disk", record.ID)
		}
		seen[record.ID] = true
	}
	return records
}

// TestAppendLockFilePath 验证锁路径派生是稳定的纯函数：后缀正确、路径被清理、
// 空白输入返回空字符串（避免生成落到当前目录的意外文件名）。
func TestAppendLockFilePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{filepath.Join("d", "s.jsonl"), filepath.Join("d", "s.jsonl") + ".append.lock"},
		{filepath.Join("d", ".", "s.jsonl"), filepath.Join("d", "s.jsonl") + ".append.lock"},
		{"   ", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := appendLockFilePath(tc.in); got != tc.want {
			t.Errorf("appendLockFilePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAppendRecordRetryOnBehindTheBackRewrite 是跨进程碰撞的红测：
// 同一目录开两个句柄，A 用 bufferedWrite 基于初始快照暂存一条记录（磁盘不变），
// B 基于同一快照准备出相同 id；在 B 落盘前让 A 的 Flush 把那条记录写到磁盘。
// 修复前 B 会把重复 id 写进文件并静默返回成功；修复后 B 检测到背后改写，
// 失效状态、重新 prepare 并使用全新 id。
func TestAppendRecordRetryOnBehindTheBackRewrite(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a, err := store.NewSession("shared")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	b, err := store.OpenSession(a.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if a.path() != b.path() {
		t.Fatalf("two handles point to different files: %q vs %q", a.path(), b.path())
	}

	if err := a.SetBufferedWrite(true); err != nil {
		t.Fatalf("SetBufferedWrite: %v", err)
	}
	staged, err := a.AppendRecord(KindUser, "from-A-staged")
	if err != nil {
		t.Fatalf("AppendRecord(A staged): %v", err)
	}
	// A 暂存后磁盘仍只有 header，所以 B 会用同一快照准备出同一个 id。
	var once sync.Once
	setAppendPrepareHookForTest(t, func() {
		once.Do(func() {
			if err := a.Flush(); err != nil {
				t.Errorf("Flush(A) behind B's back: %v", err)
			}
		})
	})

	got, err := b.AppendRecord(KindUser, "from-B")
	if err != nil {
		t.Fatalf("AppendRecord(B): %v", err)
	}
	if got.ID == staged.ID {
		t.Fatalf("B reused stale id %q after A flushed behind its back", staged.ID)
	}
	records := assertUniqueIDs(t, b.path())
	if len(records) != 4 { // 合成 schema + header + A + B
		t.Fatalf("records on disk = %d, want 4", len(records))
	}
	if records[2].ID != staged.ID || records[3].ID != got.ID {
		t.Fatalf("records = %+v, want ids %q and %q", records, staged.ID, got.ID)
	}
}

// TestAppendRecordRetryExhaustedReturnsError 验证旧"静默成功"路径不再存在：
// 每次写入前都有一个背后写入方改动文件，有限次重试全部失败后必须返回真实错误。
func TestAppendRecordRetryExhaustedReturnsError(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "retry-exhausted")
	var writes int
	setAppendPrepareHookForTest(t, func() {
		writes++
		rawAppendRecord(t, s.path(), fmt.Sprintf("n9%06d", writes), "")
	})

	record, err := s.AppendRecord(KindUser, "must not silently succeed")
	if err == nil {
		t.Fatalf("AppendRecord returned success (%+v) while the file kept changing", record)
	}
	if !errors.Is(err, errAppendRetry) {
		t.Fatalf("error = %v, want wrapped errAppendRetry", err)
	}
	if writes != appendMaxAttempts {
		t.Fatalf("write attempts = %d, want %d", writes, appendMaxAttempts)
	}
}

// TestAppendRecordSingleHandleBytesUnchanged 验证默认单句柄的落盘字节没有变化：
// 追加内容就是 json.Marshal(Record) + "\n"，锁与重试不改变 JSONL 编码。
func TestAppendRecordSingleHandleBytesUnchanged(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "bytes")
	before, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	record, err := s.AppendRecord(KindUser, map[string]string{"role": "user", "content": "hi"})
	if err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := make([]byte, 0, len(before)+len(line)+1)
	want = append(want, before...)
	want = append(want, line...)
	want = append(want, '\n')
	after, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(after, want) {
		t.Fatalf("appended bytes changed:\n got = %s\nwant = %s", after, want)
	}
	if _, err := os.Stat(appendLockFilePath(s.path())); err != nil {
		t.Fatalf("append lock sidecar was not created: %v", err)
	}
}

// TestAppendLockTwoHandlesSerialAppend 验证两个句柄交替追加时 id 仍唯一，
// 且锁文件被复用（不会为每个句柄生成不同的 sidecar）。
func TestAppendLockTwoHandlesSerialAppend(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a, err := store.NewSession("shared")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	b, err := store.OpenSession(a.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	const count = 12
	for i := 0; i < count; i++ {
		target := a
		if i%2 == 1 {
			target = b
		}
		if _, err := target.AppendRecord(KindUser, fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("AppendRecord(%d): %v", i, err)
		}
	}
	records := assertUniqueIDs(t, a.path())
	if len(records) != count+2 { // 合成 schema + header + count 条
		t.Fatalf("records = %d, want %d", len(records), count+2)
	}
	// 两个句柄派生出同一个锁路径。
	if a.appendLockPathLocked() != b.appendLockPathLocked() {
		t.Fatalf("different lock paths for the same file: %q vs %q",
			a.appendLockPathLocked(), b.appendLockPathLocked())
	}
	if !strings.HasSuffix(a.appendLockPathLocked(), appendLockExt) {
		t.Fatalf("lock path %q lacks suffix %q", a.appendLockPathLocked(), appendLockExt)
	}
}
