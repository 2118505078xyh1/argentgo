package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// fidelityFixture 把若干压缩摘要写成真实 JSONL 转录，并记录源文件原始字节。
type fidelityFixture struct {
	storage string
	id      string
	path    string
	before  []byte
}

func writeFidelityFixture(t *testing.T, id string, summaries ...string) fidelityFixture {
	t.Helper()
	storage := t.TempDir()
	dir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writeLine := func(recordID, parent, kind string, data any) {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(map[string]any{
			"id": recordID, "parent": parent,
			"ts": "2026-11-21T09:00:00Z", "kind": kind, "data": json.RawMessage(raw),
		})
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	writeLine("n000001", "", "header", map[string]any{"title": "保真度诊断"})
	parent := "n000001"
	for i, summary := range summaries {
		recordID := fmt.Sprintf("n%06d", i+2)
		writeLine(recordID, parent, session.KindCompaction, session.Compaction{
			Summary: summary, Dropped: 4, Keep: 1,
		})
		parent = recordID
	}
	if len(summaries) == 0 {
		writeLine("n000002", parent, "user", "继续任务")
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return fidelityFixture{storage: storage, id: id, path: path, before: bytes.Clone(buf.Bytes())}
}

func (f fidelityFixture) assertSourceUnchanged(t *testing.T) {
	t.Helper()
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(after, f.before) {
		t.Fatalf("源会话字节被修改：\nbefore=%q\nafter =%q", f.before, after)
	}
}

func runFidelity(t *testing.T, storage string, args ...string) (int, string, string) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	full := append([]string{"sessions", "fidelity"}, args...)
	return RunWith(App{IO: stdio, StorageDir: storage}, full), out.String(), errOut.String()
}

const fullFidelitySummary = "## 目标\n- 修复中文路径编码\n" +
	"## 改动文件\n- internal/cli/session_fidelity.go\n- docs/说明.md\n" +
	"## 失败\n- bash — 第一次构建失败\n" +
	"## 下一步\n- 跑全量验证\n"

func TestSessionsFidelityFullSummaryCJK(t *testing.T) {
	f := writeFidelityFixture(t, "full", fullFidelitySummary)
	code, out, errOut := runFidelity(t, f.storage, f.id)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	want := "压缩摘要保真度诊断\n" +
		"目标（Goal）：有\n" +
		"改动文件（ChangedFiles）：有（2 项）\n" +
		"失败（FailureCount）：有（1 项）\n" +
		"下一步（NextSteps）：有（1 项）\n" +
		"Score：1\n" +
		"Complete：是\n"
	if out != want {
		t.Fatalf("报告不符：\ngot =%q\nwant=%q", out, want)
	}
	f.assertSourceUnchanged(t)
}

func TestSessionsFidelityPartialSummary(t *testing.T) {
	f := writeFidelityFixture(t, "partial", "## Compaction\n- dropped 4 message(s), kept the last 1\n"+
		"## Goal\n- 继续任务\n"+
		"## Files changed\n- (none)\n"+
		"## Failures\n- (none)\n"+
		"## Next steps\n- 待办：运行测试\n")
	code, out, errOut := runFidelity(t, f.storage, f.id)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	want := "压缩摘要保真度诊断\n" +
		"目标（Goal）：有\n" +
		"改动文件（ChangedFiles）：无（0 项）\n" +
		"失败（FailureCount）：无（0 项）\n" +
		"下一步（NextSteps）：有（1 项）\n" +
		"Score：0.5\n" +
		"Complete：否\n"
	if out != want {
		t.Fatalf("报告不符：\ngot =%q\nwant=%q", out, want)
	}
	f.assertSourceUnchanged(t)
}

func TestSessionsFidelityWithoutCompaction(t *testing.T) {
	f := writeFidelityFixture(t, "none")
	code, out, errOut := runFidelity(t, f.storage, f.id)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "该会话尚无压缩摘要\n" {
		t.Fatalf("out = %q", out)
	}
	f.assertSourceUnchanged(t)
}

func TestSessionsFidelityUsesLatestCompaction(t *testing.T) {
	f := writeFidelityFixture(t, "latest", fullFidelitySummary, "## Goal\n- 仅剩目标\n")
	code, out, errOut := runFidelity(t, f.storage, f.id)
	if code != 0 || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "Score：0.25\n") || !strings.Contains(out, "Complete：否\n") {
		t.Fatalf("应使用最近一条摘要，out = %q", out)
	}
	f.assertSourceUnchanged(t)
}

func TestSessionsFidelityUsageAndArgValidation(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"sessions", "-h"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "argent sessions fidelity <id>") {
		t.Fatalf("sessions usage 缺少 fidelity: %q", out.String())
	}
	stdio, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(out.String(), "sessions list|show|fidelity") {
		t.Fatalf("顶层 usage 缺少 fidelity: %q", out.String())
	}
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"sessions", "fidelity"}); code != 2 {
		t.Fatalf("缺少 id 的 exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "fidelity <id>") {
		t.Fatalf("stderr 缺少用法提示: %q", errOut.String())
	}
	stdio, _, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio}, []string{"sessions", "fidelity", "x", "extra"}); code != 2 {
		t.Fatalf("多余参数的 exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unexpected fidelity argument") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestSessionsFidelityLeavesOtherCommandsUnchanged(t *testing.T) {
	f := writeFidelityFixture(t, "guard", fullFidelitySummary)
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: f.storage}, []string{"sessions", "list"}); code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, errOut.String())
	}
	fields := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\t")
	if len(fields) != 3 || fields[0] != "guard" {
		t.Fatalf("list 输出变化: %q", out.String())
	}
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: f.storage}, []string{"sessions", "show", f.id}); code != 0 {
		t.Fatalf("show exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"kind":"compaction"`) {
		t.Fatalf("show 输出变化: %q", out.String())
	}
	f.assertSourceUnchanged(t)
}
