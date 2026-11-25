package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/session"
)

// healthRun 在给定 storage 下运行 `sessions health <args...>`，返回退出码与两段输出。
func healthRun(t *testing.T, storage string, args ...string) (int, string, string) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, append([]string{"sessions", "health"}, args...))
	return code, out.String(), errOut.String()
}

// TestSessionsHealthHealthySession 验证健康会话输出中文报告、无转录问题且包含磁盘占用。
func TestSessionsHealthHealthySession(t *testing.T) {
	storage := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("健康会话")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "第一段文本"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindAssistant, "回复"); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := healthRun(t, storage, sess.ID())
	if code != 0 {
		t.Fatalf("health exit = %d, out %q err %q", code, out, errOut)
	}
	for _, want := range []string{
		"会话健康检查", "指定会话", "转录问题: 共 0 处", "磁盘占用:", "字节",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("health output missing %q: %q", want, out)
		}
	}
}

// TestSessionsHealthCorruptedSession 验证损坏会话按 kind/count 汇总问题，
// 且绝不把问题 Detail 中的原始 id / 文本泄漏到输出（脱敏），CJK 内容不受影响。
func TestSessionsHealthCorruptedSession(t *testing.T) {
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretID := "机密_ID_XYZ"
	secretParent := "不存在的父_机密"
	// 两条记录：第二条 id 重复且 parent 悬空，共产生 duplicate_id + dangling_parent 两类问题。
	data := fmt.Sprintf("{\"id\":%q,\"parent\":\"\"}\n{\"id\":%q,\"parent\":%q}\n",
		secretID, secretID, secretParent)
	if err := os.WriteFile(filepath.Join(sessionsDir, "corrupt.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := healthRun(t, storage, "corrupt")
	if code != 0 {
		t.Fatalf("health exit = %d, out %q err %q", code, out, errOut)
	}
	for _, want := range []string{
		"转录问题: 共 2 处", "duplicate_id: 1", "dangling_parent: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("health output missing %q: %q", want, out)
		}
	}
	for _, leak := range []string{secretID, secretParent} {
		if strings.Contains(out, leak) {
			t.Errorf("health output leaked raw id/text %q: %q", leak, out)
		}
	}
}

// TestSessionsHealthDiskUsage 验证磁盘占用数字与 MeasureDiskUsage 一致。
func TestSessionsHealthDiskUsage(t *testing.T) {
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"id":"a","parent":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	usage, err := session.MeasureDiskUsage(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}

	code, out, errOut := healthRun(t, storage)
	if code != 0 {
		t.Fatalf("health exit = %d, out %q err %q", code, out, errOut)
	}
	want := fmt.Sprintf("磁盘占用: %d 个文件, %d 字节", usage.Files, usage.Bytes)
	if !strings.Contains(out, want) {
		t.Errorf("health output missing %q: %q", want, out)
	}
	for _, kind := range []string{"session", "archive", "other"} {
		line := fmt.Sprintf("  %s: %d 字节", kind, usage.ByKind[kind])
		if !strings.Contains(out, line) {
			t.Errorf("health output missing %q: %q", line, out)
		}
	}
}

// TestSessionsHealthAllSessions 验证无参数时汇总全部会话的问题数。
func TestSessionsHealthAllSessions(t *testing.T) {
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 一份健康转录，两份各含一个 invalid_json 的转录。
	files := map[string]string{
		"good.jsonl": `{"id":"g","parent":""}`,
		"bad1.jsonl": "not json\n",
		"bad2.jsonl": "also not json\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, out, errOut := healthRun(t, storage)
	if code != 0 {
		t.Fatalf("health exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(out, "会话健康检查: 全部会话") {
		t.Errorf("health output missing all-sessions header: %q", out)
	}
	if !strings.Contains(out, "转录问题: 共 2 处") || !strings.Contains(out, "invalid_json: 2") {
		t.Errorf("health output has wrong aggregate: %q", out)
	}
}

// TestSessionsHealthErrors 验证非法 id、缺失会话、多余参数与目录不存在都给出可读错误。
func TestSessionsHealthErrors(t *testing.T) {
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "s1.jsonl"), []byte(`{"id":"a","parent":""}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"path escape", []string{"../escape"}, 1, "错误:"},
		{"empty id", []string{""}, 1, "错误:"},
		{"missing session", []string{"no-such-session"}, 1, "错误:"},
		{"too many args", []string{"s1", "extra"}, 2, "usage:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := healthRun(t, storage, tc.args...)
			if code != tc.code {
				t.Fatalf("exit = %d, want %d, out %q err %q", code, tc.code, out, errOut)
			}
			combined := out + errOut
			if !strings.Contains(combined, tc.want) {
				t.Errorf("output missing %q: %q", tc.want, combined)
			}
		})
	}

	// 目录不存在时给出可读中文错误，而不是底层英文 stat 错误。
	missingStorage := filepath.Join(storage, "missing-root")
	code, out, errOut := healthRun(t, missingStorage)
	if code != 1 {
		t.Fatalf("missing dir exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "会话目录不存在") {
		t.Errorf("missing dir error not readable: %q", errOut)
	}
}

// TestSessionsHealthCJKIds 验证 CJK id / 文本不会产生误报，非法 id 的中文错误完整。
func TestSessionsHealthCJKIds(t *testing.T) {
	storage := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("中文标题")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "带中文和 emoji 🙂 的消息"); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := healthRun(t, storage, sess.ID())
	if code != 0 {
		t.Fatalf("health exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(out, "转录问题: 共 0 处") {
		t.Errorf("CJK session reported issues: %q", out)
	}
	// 输出不含原始会话 id（脱敏），即使是 CJK id 也不泄漏。
	if strings.Contains(out, sess.ID()) {
		t.Errorf("health output leaked CJK session id %q: %q", sess.ID(), out)
	}
	// 非法输入中的中文不应被按字节截断。
	_, _, errOut = healthRun(t, storage, "../非法路径")
	if !strings.Contains(errOut, "错误:") {
		t.Errorf("CJK illegal id error not readable: %q", errOut)
	}
}

// TestSessionsHealthDocumentedInUsage 验证 health 同时出现在 sessions 与顶层 usage 中。
func TestSessionsHealthDocumentedInUsage(t *testing.T) {
	var sessions bytes.Buffer
	sessionsUsage(&sessions)
	var top bytes.Buffer
	writeUsage(&top)
	if !strings.Contains(sessions.String(), "sessions health") {
		t.Errorf("sessions usage does not mention health: %q", sessions.String())
	}
	if !strings.Contains(top.String(), "health") {
		t.Errorf("top-level usage does not mention health: %q", top.String())
	}
}
