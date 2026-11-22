package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// recordOneRound 在 workspace 上记录一个轮次：先捕获轮次前快照，写入新内容，
// 再补全轮次后状态并落地检查点。返回该检查点的记录 id。
func recordOneRound(t *testing.T, sess *session.Session, workspace string, paths []string, writes map[string]string) string {
	t.Helper()
	session.ClearCheckpointHashCache()
	cp, err := session.CaptureCheckpoint(workspace, paths)
	if err != nil {
		t.Fatal(err)
	}
	for rel, content := range writes {
		target := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.CompleteCheckpoint(workspace, &cp); err != nil {
		t.Fatal(err)
	}
	record, err := sess.RecordCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	return record.ID
}

// newStepSession 创建一个用于检查点步数测试的空会话与工作区。
func newStepSession(t *testing.T) (*session.Session, string, string) {
	t.Helper()
	storage := t.TempDir()
	workspace := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	return sess, storage, workspace
}

func readWorkspaceFile(t *testing.T, workspace, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSessionsUndoStepsRestoresTwoRounds 验证记录三个检查点后 --steps 2
// 一次性还原最近两轮的改动，工作区回到第一个检查点的状态。
func TestSessionsUndoStepsRestoresTwoRounds(t *testing.T) {
	sess, storage, workspace := newStepSession(t)
	path := "a.txt"
	if err := os.WriteFile(filepath.Join(workspace, path), []byte("原始内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第一轮改动"})
	second := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第二轮改动更长"})
	recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第三轮改动最长的内容"})
	if got := readWorkspaceFile(t, workspace, path); got != "第三轮改动最长的内容" {
		t.Fatalf("撤销前内容 = %q", got)
	}

	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "undo", sess.ID(), "--steps", "2", "-C", workspace,
	})
	if code != 0 {
		t.Fatalf("undo --steps 2 exit = %d, err %q", code, errOut.String())
	}
	// 两轮改动都被还原，且只剩第一个检查点对应的状态。
	if got := readWorkspaceFile(t, workspace, path); got != "第一轮改动" {
		t.Fatalf("撤销后内容 = %q, 应为第一个检查点的状态 %q", got, "第一轮改动")
	}
	if !strings.Contains(out.String(), "restored 1 file(s)") ||
		!strings.Contains(out.String(), second) ||
		strings.Contains(out.String(), first) {
		t.Fatalf("undo 输出应标识还原到倒数第二个检查点 %s，实际 = %q", second, out.String())
	}
}

// TestSessionsUndoStepsMergesDistinctFiles 验证两轮触及不同文件时，
// 合并清单仍把两轮改动都还原（各自回到轮次前状态）。
func TestSessionsUndoStepsMergesDistinctFiles(t *testing.T) {
	sess, storage, workspace := newStepSession(t)
	a, b := "a.txt", "b.txt"
	if err := os.WriteFile(filepath.Join(workspace, a), []byte("a0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, b), []byte("b0"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordOneRound(t, sess, workspace, []string{a, b}, map[string]string{a: "a-第一轮", b: "b-第一轮"})
	recordOneRound(t, sess, workspace, []string{a}, map[string]string{a: "a-第二轮改动"})
	recordOneRound(t, sess, workspace, []string{b}, map[string]string{b: "b-第三轮改动"})

	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "undo", sess.ID(), "--steps=2", "-C", workspace,
	})
	if code != 0 {
		t.Fatalf("undo --steps=2 exit = %d, err %q", code, errOut.String())
	}
	if got := readWorkspaceFile(t, workspace, a); got != "a-第一轮" {
		t.Fatalf("a.txt = %q, want %q", got, "a-第一轮")
	}
	if got := readWorkspaceFile(t, workspace, b); got != "b-第一轮" {
		t.Fatalf("b.txt = %q, want %q", got, "b-第一轮")
	}
	if !strings.Contains(out.String(), "restored 2 file(s)") {
		t.Fatalf("undo 输出 = %q", out.String())
	}
}

// TestSessionsUndoWithoutStepsBaseline 验证无 --steps 时输出逐字节保持原格式，
// 且 --steps 1 与基线完全一致（同一份精确输出）。
func TestSessionsUndoWithoutStepsBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no-steps", []string{"-C", "WORKSPACE"}},
		{"steps-one", []string{"--steps", "1", "-C", "WORKSPACE"}},
		{"steps-one-inline", []string{"--steps=1", "-C", "WORKSPACE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, storage, workspace := newStepSession(t)
			path := "a.txt"
			if err := os.WriteFile(filepath.Join(workspace, path), []byte("before"), 0o644); err != nil {
				t.Fatal(err)
			}
			recordID := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "after"})

			args := make([]string, len(tc.args))
			for i, arg := range tc.args {
				args[i] = strings.ReplaceAll(arg, "WORKSPACE", workspace)
			}
			full := append([]string{"sessions", "undo", sess.ID()}, args...)
			stdio, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio, StorageDir: storage}, full)
			if code != 0 {
				t.Fatalf("exit = %d, err %q", code, errOut.String())
			}
			want := fmt.Sprintf("restored 1 file(s) from checkpoint %s in %s\n", recordID, sess.ID())
			if out.String() != want {
				t.Fatalf("输出逐字节不一致:\n got = %q\nwant = %q", out.String(), want)
			}
			if got := readWorkspaceFile(t, workspace, path); got != "before" {
				t.Fatalf("文件 = %q, want before", got)
			}
		})
	}
}

// TestSessionsUndoStepsInvalid 验证非法步数返回用法错误且不触碰工作区。
func TestSessionsUndoStepsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"zero", []string{"--steps", "0"}},
		{"inline-zero", []string{"--steps=0"}},
		{"negative", []string{"--steps", "-1"}},
		{"non-numeric", []string{"--steps", "abc"}},
		{"float", []string{"--steps", "1.5"}},
		{"missing-value", []string{"--steps"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, storage, workspace := newStepSession(t)
			path := "a.txt"
			if err := os.WriteFile(filepath.Join(workspace, path), []byte("before"), 0o644); err != nil {
				t.Fatal(err)
			}
			recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "after"})

			full := append([]string{"sessions", "undo", sess.ID(), "-C", workspace}, tc.args...)
			stdio, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio, StorageDir: storage}, full)
			if code != 2 {
				t.Fatalf("非法 --steps exit = %d, want 2; err %q", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), "steps") {
				t.Fatalf("错误信息应包含 steps，实际 = %q", errOut.String())
			}
			if out.String() != "" {
				t.Fatalf("非法参数不应有 stdout 输出，实际 = %q", out.String())
			}
			if got := readWorkspaceFile(t, workspace, path); got != "after" {
				t.Fatalf("文件被意外改动 = %q", got)
			}
		})
	}
}

// TestSessionsUndoStepsTooLarge 验证步数超过可用检查点数时返回错误且不改动文件。
func TestSessionsUndoStepsTooLarge(t *testing.T) {
	sess, storage, workspace := newStepSession(t)
	path := "a.txt"
	if err := os.WriteFile(filepath.Join(workspace, path), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "after"})

	stdio, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "undo", sess.ID(), "--steps", "3", "-C", workspace,
	})
	if code != 1 {
		t.Fatalf("超范围 --steps exit = %d, want 1; err %q", code, errOut.String())
	}
	if got := readWorkspaceFile(t, workspace, path); got != "after" {
		t.Fatalf("文件被意外改动 = %q", got)
	}
}

// TestSessionsRetryStepsPicksNthCheckpoint 验证 retry --steps N 从倒数第 N 个
// 检查点分叉；无 --steps 的基线仍从最新检查点分叉。
func TestSessionsRetryStepsPicksNthCheckpoint(t *testing.T) {
	sess, storage, workspace := newStepSession(t)
	path := "a.txt"
	if err := os.WriteFile(filepath.Join(workspace, path), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "one"})
	second := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "two"})
	latest := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "three"})

	// 基线：无 --steps 使用最新检查点。
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "retry", sess.ID()}); code != 0 {
		t.Fatalf("baseline retry exit = %d, err %q", code, errOut.String())
	}
	baselineFork := strings.TrimSpace(out.String())
	if baselineFork == "" || baselineFork == sess.ID() {
		t.Fatalf("baseline fork id = %q", baselineFork)
	}
	if got := forkCheckpointMessage(t, storage, baselineFork); !strings.Contains(got, `"sourceRecord":"`+latest+`"`) {
		t.Fatalf("baseline fork message = %q, want sourceRecord %s", got, latest)
	}

	// --steps 2：使用倒数第二个检查点。
	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "retry", sess.ID(), "--steps", "2",
	}); code != 0 {
		t.Fatalf("retry --steps 2 exit = %d, err %q", code, errOut.String())
	}
	stepFork := strings.TrimSpace(out.String())
	if stepFork == "" || stepFork == sess.ID() {
		t.Fatalf("step fork id = %q", stepFork)
	}
	if got := forkCheckpointMessage(t, storage, stepFork); !strings.Contains(got, `"sourceRecord":"`+second+`"`) {
		t.Fatalf("step fork message = %q, want sourceRecord %s not %s", got, second, latest)
	}

	// 显式检查点 id 与 --steps 冲突应报错。
	stdio, _, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "retry", sess.ID(), "--steps", "2", second,
	}); code != 2 {
		t.Fatalf("steps + explicit id exit = %d, want 2; err %q", code, errOut.String())
	}
}

// forkCheckpointMessage 读取分叉会话 JSONL，返回其中的血缘消息。
func forkCheckpointMessage(t *testing.T, storage, forkID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(storage, "sessions", forkID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSessionsStepsCJK 验证中文路径、中文内容与中文会话标题下，
// undo 与 retry 的 --steps 行为保持正确。
func TestSessionsStepsCJK(t *testing.T) {
	sess, storage, workspace := newStepSession(t)
	path := "中文目录/文档.txt"
	initial := filepath.Join(workspace, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(initial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initial, []byte("初始"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第一版"})
	second := recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第二版内容"})
	recordOneRound(t, sess, workspace, []string{path}, map[string]string{path: "第三版内容更长"})

	stdio, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "name", sess.ID(), "中文会话标题",
	}); code != 0 {
		t.Fatalf("name exit = %d, err %q", code, errOut.String())
	}

	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "undo", sess.ID(), "--steps", "2", "-C", workspace,
	}); code != 0 {
		t.Fatalf("CJK undo exit = %d, err %q", code, errOut.String())
	}
	if got := readWorkspaceFile(t, workspace, path); got != "第一版" {
		t.Fatalf("CJK 撤销后内容 = %q, want 第一版", got)
	}
	if !strings.Contains(out.String(), "restored 1 file(s)") {
		t.Fatalf("CJK undo 输出 = %q", out.String())
	}

	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "retry", sess.ID(), "--steps", "2",
	}); code != 0 {
		t.Fatalf("CJK retry exit = %d, err %q", code, errOut.String())
	}
	forkID := strings.TrimSpace(out.String())
	if got := forkCheckpointMessage(t, storage, forkID); !strings.Contains(got, `"sourceRecord":"`+second+`"`) {
		t.Fatalf("CJK retry fork message = %q, want sourceRecord %s", got, second)
	}
}
