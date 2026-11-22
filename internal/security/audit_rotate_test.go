package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var auditTestBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func auditTestPath(dir string) string { return filepath.Join(dir, "2026-01-02.jsonl") }

// auditFixedNow 固定日期，保证所有记录落到同一个测试文件。
func auditFixedNow() func() time.Time {
	return func() time.Time { return auditTestBase }
}

// auditIncreasingNow 每次调用前进一秒，便于按时间戳验证顺序。
func auditIncreasingNow() func() time.Time {
	var n int64
	return func() time.Time {
		n++
		return auditTestBase.Add(time.Duration(n) * time.Second)
	}
}

// auditRotationFiles 按 base, .1, .2, ... 的顺序列出归档文件。
func auditRotationFiles(t *testing.T, base string) []string {
	t.Helper()
	paths := []string{base}
	for i := 1; i < 1000; i++ {
		p := fmt.Sprintf("%s.%d", base, i)
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			t.Fatalf("stat %s: %v", p, err)
		}
		paths = append(paths, p)
	}
	return paths
}

func setGlobalRotationForTest(t *testing.T, cfg *AuditRotationConfig) {
	t.Helper()
	old := defaultAuditRotation.Load()
	if cfg == nil {
		defaultAuditRotation.Store(nil)
	} else {
		defaultAuditRotation.Store(cfg)
	}
	t.Cleanup(func() { defaultAuditRotation.Store(old) })
}

// TestAuditRotationDisabledKeepsBytes 验证默认关闭时逐字节不变、且不产生任何归档。
func TestAuditRotationDisabledKeepsBytes(t *testing.T) {
	setGlobalRotationForTest(t, nil)
	dir := t.TempDir()
	log := NewAuditLog(dir)
	log.now = auditFixedNow()

	entries := []AuditEntry{
		{Tool: "read", Decision: "allow", Reason: "普通原因", Args: `{"path":"a.txt"}`},
		{Tool: "bash", Decision: "deny", Reason: "包含 sk-test-abcdef123456 的原因"},
	}
	for _, e := range entries {
		if err := log.Record(e); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	path := auditTestPath(dir)
	if files := auditRotationFiles(t, path); len(files) != 1 {
		t.Fatalf("rotation off but got files %v", files)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var want []byte
	for _, e := range entries {
		e.TS = auditTestBase.UTC().Format(time.RFC3339)
		e.Reason = Redact(e.Reason)
		if e.Args != "" {
			e.Args = RedactJSON([]byte(e.Args))
		}
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want = append(want, line...)
		want = append(want, '\n')
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes changed with rotation off:\n got=%q\nwant=%q", got, want)
	}
}

// TestAuditRotationTriggersAndKeepsN 验证超阈值触发轮转，且只保留最近 N 个归档。
// 每条记录都超过阈值，因此每次写入前都会轮转，最终只剩最新的 N+1 个文件。
func TestAuditRotationTriggersAndKeepsN(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	log.now = auditIncreasingNow()
	log.SetRotation(AuditRotationConfig{MaxBytes: 120, Keep: 2})

	big := strings.Repeat("x", 200)
	const total = 12
	for i := 0; i < total; i++ {
		if err := log.Record(AuditEntry{Tool: "read", Decision: "allow", Reason: big}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	files := auditRotationFiles(t, auditTestPath(dir))
	if len(files) != 3 { // base + .1 + .2
		t.Fatalf("files = %v, want base + 2 archives", files)
	}
	var all []AuditEntry
	for _, f := range files {
		entries, err := ReadAudit(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		all = append(all, entries...)
	}
	if len(all) != 3 {
		t.Fatalf("retained %d entries, want 3 (newest only)", len(all))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TS < all[j].TS })
	for i, wantSeq := range []int64{10, 11, 12} {
		wantTS := auditTestBase.Add(time.Duration(wantSeq) * time.Second).UTC().Format(time.RFC3339)
		if all[i].TS != wantTS {
			t.Fatalf("entry %d ts = %q, want %q", i, all[i].TS, wantTS)
		}
	}
}

// TestAuditRotationGlobalOptIn 验证 SetAuditRotation 这个全局 opt-in 开关。
func TestAuditRotationGlobalOptIn(t *testing.T) {
	setGlobalRotationForTest(t, &AuditRotationConfig{MaxBytes: 120, Keep: 3})
	dir := t.TempDir()
	log := NewAuditLog(dir) // 没有单独配置，应回落到全局配置
	log.now = auditFixedNow()

	big := strings.Repeat("x", 200)
	for i := 0; i < 3; i++ {
		if err := log.Record(AuditEntry{Tool: "read", Decision: "allow", Reason: big}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if files := auditRotationFiles(t, auditTestPath(dir)); len(files) != 3 { // 3 条记录 -> 2 个归档 + 当前文件
		t.Fatalf("global opt-in did not rotate: %v", files)
	}
}

// TestAuditRotationCJKAndRedaction 验证 CJK 内容在轮转后完整保留，且脱敏先于写入。
func TestAuditRotationCJKAndRedaction(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	log.now = auditFixedNow()
	log.SetRotation(AuditRotationConfig{MaxBytes: 120, Keep: 3})

	const secret = "sk-abcdef1234567890"
	for i := 0; i < 8; i++ {
		err := log.Record(AuditEntry{
			Tool:     "bash",
			Decision: "deny",
			Reason:   "中文原因：" + secret + "；允许中文与 emoji 🚀",
			Args:     `{"路径":"密钥 sk-abcdef1234567890"}`,
		})
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	var raw bytes.Buffer
	for _, f := range auditRotationFiles(t, auditTestPath(dir)) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		raw.Write(b)
		entries, err := ReadAudit(f)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s has no entries", f)
		}
	}
	if bytes.Contains(raw.Bytes(), []byte(secret)) {
		t.Fatalf("secret leaked into rotated logs: %q", raw.String())
	}
	if !bytes.Contains(raw.Bytes(), []byte("中文原因")) || !bytes.Contains(raw.Bytes(), []byte("🚀")) {
		t.Fatalf("CJK content lost after rotation: %q", raw.String())
	}
}

// TestAuditRotationFailureKeepsWriting 验证轮转失败时回退原文件，不丢审计记录。
func TestAuditRotationFailureKeepsWriting(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	log.now = auditFixedNow()
	log.SetRotation(AuditRotationConfig{MaxBytes: 1, Keep: 3})
	log.rotate = func(string, AuditRotationConfig) error { return errors.New("injected rotation failure") }

	if err := log.Record(AuditEntry{Tool: "bash", Decision: "deny", Reason: "失败注入"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if files := auditRotationFiles(t, auditTestPath(dir)); len(files) != 1 {
		t.Fatalf("failed rotation produced archives: %v", files)
	}
	entries, err := ReadAudit(auditTestPath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 || entries[0].Reason != "失败注入" {
		t.Fatalf("record lost on rotation failure: %+v", entries)
	}
}

// TestAuditRotationOrderAndCrashRecovery 验证归档编号语义（越大越旧）以及崩溃后重建当前文件。
func TestAuditRotationOrderAndCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	path := auditTestPath(dir)
	writeLines := func(p, content string) {
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// 模拟一次轮转前的状态：.2 最旧、.1 居中、当前文件最新（脏数据，仅用于验证映射）。
	writeLines(path+".2", `{"ts":"oldest"}`)
	writeLines(path+".1", `{"ts":"middle"}`)
	writeLines(path, strings.Repeat("x", 200))

	log := NewAuditLog(dir)
	log.now = auditFixedNow()
	log.SetRotation(AuditRotationConfig{MaxBytes: 120, Keep: 2})
	if err := log.Record(AuditEntry{Tool: "read", Decision: "allow", Reason: "fresh"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive beyond keep should be dropped: %v", err)
	}
	got1, err := os.ReadFile(path + ".1")
	if err != nil || !strings.Contains(string(got1), strings.Repeat("x", 200)) {
		t.Fatalf(".1 should hold former current file: %q err=%v", got1, err)
	}
	got2, err := os.ReadFile(path + ".2")
	if err != nil || !strings.Contains(string(got2), "middle") {
		t.Fatalf(".2 should hold former .1: %q err=%v", got2, err)
	}
	base, err := ReadAudit(path)
	if err != nil || len(base) != 1 || base[0].Reason != "fresh" {
		t.Fatalf("current file should hold new record: %+v err=%v", base, err)
	}

	// 模拟崩溃在"当前文件改名为 .1 之后、新文件尚未写入"的时刻：当前文件缺失。
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := log.Record(AuditEntry{Tool: "read", Decision: "allow", Reason: "after-crash"}); err != nil {
		t.Fatalf("record after crash: %v", err)
	}
	base, err = ReadAudit(path)
	if err != nil || len(base) != 1 || base[0].Reason != "after-crash" {
		t.Fatalf("current file not rebuilt after crash: %+v err=%v", base, err)
	}
}

// TestAuditRotationConcurrentRecords 验证多 goroutine 写入配合轮转不丢记录。
func TestAuditRotationConcurrentRecords(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	log.now = auditFixedNow()
	// Keep 足够大，避免测试自身的保留策略丢弃旧记录；仍强制多次轮转以覆盖并发路径。
	log.SetRotation(AuditRotationConfig{MaxBytes: 1000, Keep: 100})

	const writers, perWriter = 8, 40
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := log.Log("s", "read", Allow, "", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	total := 0
	for _, f := range auditRotationFiles(t, auditTestPath(dir)) {
		entries, err := ReadAudit(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		total += len(entries)
	}
	if total != writers*perWriter {
		t.Fatalf("got %d entries, want %d", total, writers*perWriter)
	}
}
