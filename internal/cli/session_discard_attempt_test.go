package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discardAttemptLine 构造一条 JSONL 记录，data 必须是合法的 JSON 片段。
func discardAttemptLine(id, parent, kind, data string) string {
	line, err := json.Marshal(map[string]any{
		"id":     id,
		"parent": parent,
		"ts":     "2026-11-21T09:00:00Z",
		"kind":   kind,
		"data":   json.RawMessage(data),
	})
	if err != nil {
		panic(err)
	}
	return string(line)
}

// discardAttemptFixture 把记录写成真实 JSONL 转录，供 CLI 端到端测试使用。
type discardAttemptFixture struct {
	storage string
	path    string
	before  []byte
}

func writeDiscardAttemptFixture(t *testing.T, id string, lines []string, trailingNewline bool) discardAttemptFixture {
	t.Helper()
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for i, line := range lines {
		buf.WriteString(line)
		if i < len(lines)-1 || trailingNewline {
			buf.WriteByte('\n')
		}
	}
	path := filepath.Join(sessionsDir, id+".jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return discardAttemptFixture{
		storage: storage,
		path:    path,
		before:  append([]byte(nil), buf.Bytes()...),
	}
}

func (f discardAttemptFixture) assertSourceUnchanged(t *testing.T) {
	t.Helper()
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(after, f.before) {
		t.Fatalf("源会话字节被修改：\nbefore=%q\nafter =%q", f.before, after)
	}
}

// productionFailureLines 构造生产式转录：失败证据写在 KindAttempt 记录之后，
// 分叉点 n000003 在活动路径上，丢弃应当成功。
func productionFailureLines() []string {
	return []string{
		discardAttemptLine("n000001", "", "header", `{"title":"生产会话"}`),
		discardAttemptLine("n000002", "n000001", "title", `{"title":"生产会话"}`),
		discardAttemptLine("n000003", "n000002", "user", `"继续任务"`),
		discardAttemptLine("n000004", "n000003", "attempt", `{"reason":"initial"}`),
		discardAttemptLine("n000005", "n000004", "tool", `{"name":"bash","output":"boom","is_error":true}`),
		discardAttemptLine("n000006", "n000005", "assistant", `"收尾"`),
	}
}

func runDiscardAttempt(t *testing.T, storage string, args ...string) (int, string, string) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	full := append([]string{"sessions", "discard-attempt"}, args...)
	return RunWith(App{IO: stdio, StorageDir: storage}, full), out.String(), errOut.String()
}

func TestSessionsDiscardAttemptForksAndPreservesSource(t *testing.T) {
	f := writeDiscardAttemptFixture(t, "discard", productionFailureLines(), true)
	code, out, errOut := runDiscardAttempt(t, f.storage, "discard", "n000004")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	forkID := strings.TrimSpace(out)
	if forkID == "" || forkID == "discard" {
		t.Fatalf("stdout = %q, want a fresh fork id", out)
	}
	if !strings.Contains(errOut, "--session "+forkID+" --resume") {
		t.Fatalf("stderr 缺少续用提示: %q", errOut)
	}
	f.assertSourceUnchanged(t)

	forkPath := filepath.Join(f.storage, "sessions", forkID+".jsonl")
	data, err := os.ReadFile(forkPath)
	if err != nil {
		t.Fatalf("read fork: %v", err)
	}
	if bytes.Contains(data, []byte(`"kind":"tool"`)) {
		t.Fatalf("失败工具记录被带进 fork: %q", data)
	}
	if !bytes.Contains(data, []byte(`"reason":"retried"`)) {
		t.Fatalf("fork 缺少重试血缘: %q", data)
	}
}

func TestSessionsDiscardAttemptFailClosedNotDiscardable(t *testing.T) {
	lines := productionFailureLines()
	lines = append(lines,
		discardAttemptLine("n000007", "n000006", "tool", `{"name":"bash","output":"ok","is_error":false}`),
	)
	f := writeDiscardAttemptFixture(t, "discard", lines, true)
	code, out, errOut := runDiscardAttempt(t, f.storage, "discard", "n000004")
	if code == 0 {
		t.Fatalf("含成功结果时应 fail-closed，stdout = %q", out)
	}
	if out != "" {
		t.Fatalf("拒绝时 stdout 必须为空，got %q", out)
	}
	if !strings.Contains(errOut, "不满足安全丢弃条件") {
		t.Fatalf("稳定的中文提示缺失: %q", errOut)
	}
	f.assertSourceUnchanged(t)
	entries, err := os.ReadDir(filepath.Join(f.storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("拒绝时不应产生新会话文件，got %d entries", len(entries))
	}
}

func TestSessionsDiscardAttemptFailClosedBranchOffPath(t *testing.T) {
	lines := []string{
		discardAttemptLine("n000001", "", "header", `{"title":"跨分支"}`),
		discardAttemptLine("n000002", "n000001", "title", `{"title":"跨分支"}`),
		discardAttemptLine("n000003", "n000002", "user", `"分支 A"`),
		discardAttemptLine("n000004", "n000003", "attempt", `{"reason":"initial"}`),
		discardAttemptLine("n000005", "n000004", "tool", `{"is_error":true,"output":"boom"}`),
		discardAttemptLine("n000006", "n000002", "user", `"分支 B"`),
		discardAttemptLine("n000007", "n000006", "assistant", `"当前活动叶"`),
	}
	f := writeDiscardAttemptFixture(t, "discard", lines, true)
	code, out, errOut := runDiscardAttempt(t, f.storage, "discard", "n000004")
	if code == 0 {
		t.Fatalf("跨分支时应 fail-closed，stdout = %q", out)
	}
	if !strings.Contains(errOut, "不在当前活动路径上") {
		t.Fatalf("稳定的中文提示缺失: %q", errOut)
	}
	f.assertSourceUnchanged(t)
}

// TestSessionsDiscardAttemptDefaultID 在活动分支上有两次失败尝试时，
// 缺省参数应选择最后一个 reason=failed 的尝试；第一次尝试混有成功结果，
// 若选错会 fail-closed，因此本测试能区分选择是否正确。
func TestSessionsDiscardAttemptDefaultID(t *testing.T) {
	lines := []string{
		discardAttemptLine("n000001", "", "header", `{"title":"默认尝试"}`),
		discardAttemptLine("n000002", "n000001", "title", `{"title":"默认尝试"}`),
		discardAttemptLine("n000003", "n000002", "user", `"继续"`),
		discardAttemptLine("n000004", "n000003", "attempt", `{"reason":"failed"}`),
		discardAttemptLine("n000005", "n000004", "tool", `{"is_error":true,"output":"boom"}`),
		discardAttemptLine("n000006", "n000005", "tool", `{"is_error":false,"output":"ok"}`),
		discardAttemptLine("n000007", "n000006", "attempt", `{"reason":"failed"}`),
		discardAttemptLine("n000008", "n000007", "tool", `{"is_error":true,"output":"boom2"}`),
		discardAttemptLine("n000009", "n000008", "assistant", `"收尾"`),
	}
	f := writeDiscardAttemptFixture(t, "discard", lines, true)
	code, out, errOut := runDiscardAttempt(t, f.storage, "discard")
	if code != 0 {
		t.Fatalf("缺省 attempt id 未选中最后一次失败尝试: stderr = %q", errOut)
	}
	forkID := strings.TrimSpace(out)
	if forkID == "" || forkID == "discard" {
		t.Fatalf("stdout = %q, want a fresh fork id", out)
	}
	f.assertSourceUnchanged(t)
	data, err := os.ReadFile(filepath.Join(f.storage, "sessions", forkID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"sourceRecord":"n000007"`)) {
		t.Fatalf("血缘未指向最后一次失败尝试 n000007: %q", data)
	}
}

func TestSessionsDiscardAttemptNoTrailingNewlinePreservesSource(t *testing.T) {
	f := writeDiscardAttemptFixture(t, "discard", productionFailureLines(), false)
	if bytes.HasSuffix(f.before, []byte("\n")) {
		t.Fatal("fixture 应当没有末尾换行")
	}
	code, out, errOut := runDiscardAttempt(t, f.storage, "discard", "n000004")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("stdout 应输出 fork id")
	}
	f.assertSourceUnchanged(t)
}

func TestSessionsDiscardAttemptMissingArgs(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: t.TempDir()}, []string{"sessions", "discard-attempt"}); code != 2 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "discard-attempt <id>") {
		t.Fatalf("usage 未列出新子命令: %q", errOut.String())
	}
}
