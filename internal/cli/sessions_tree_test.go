package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// treeRun 在给定 storage 下运行 `sessions tree <args...>`，返回退出码与两段输出。
func treeRun(t *testing.T, storage string, args ...string) (int, string, string) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, append([]string{"sessions", "tree"}, args...))
	return code, out.String(), errOut.String()
}

// TestSessionsTreeSingleChain 验证单链渲染为一条缩进链，且只有末尾记录被标记为 active。
func TestSessionsTreeSingleChain(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-20T00:00:00Z"
	writeJSONLSession(t, storage, "chain", []map[string]any{
		record("n1", "", stamp, "header", map[string]any{"title": "chain"}),
		record("n2", "n1", stamp, "user", map[string]any{"text": "问"}),
		record("n3", "n2", stamp, "assistant", map[string]any{"text": "答"}),
	})
	code, out, errOut := treeRun(t, storage, "chain")
	if code != 0 {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	want := "n1\n└── n2\n    └── n3  ← active\n"
	if out != "会话树: chain\n"+want {
		t.Fatalf("tree out = %q, want suffix %q", out, want)
	}
}

// TestSessionsTreeForkBranch 验证分叉树用 ├/└ 表示兄弟，active 落在最后一条记录。
func TestSessionsTreeForkBranch(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-20T00:00:00Z"
	writeJSONLSession(t, storage, "fork", []map[string]any{
		record("r", "", stamp, "header", map[string]any{"title": "fork"}),
		record("a", "r", stamp, "user", map[string]any{"text": "根问题"}),
		record("b", "a", stamp, "assistant", map[string]any{"text": "分支一"}),
		record("c", "a", stamp, "assistant", map[string]any{"text": "分支二"}),
	})
	code, out, errOut := treeRun(t, storage, "fork")
	if code != 0 {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	want := "r\n└── a\n    ├── b\n    └── c  ← active\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("tree out = %q, want suffix %q", out, want)
	}
}

// TestSessionsTreeCJK 验证中文 id / 记录 id 原样输出，不被按字节截断。
func TestSessionsTreeCJK(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-20T00:00:00Z"
	writeJSONLSession(t, storage, "会话树", []map[string]any{
		record("根-设置", "", stamp, "header", map[string]any{"title": "中文会话"}),
		record("甲-用户", "根-设置", stamp, "user", map[string]any{"text": "你好"}),
		record("乙-助手", "根-设置", stamp, "assistant", map[string]any{"text": "世界"}),
	})
	code, out, errOut := treeRun(t, storage, "会话树")
	if code != 0 {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	for _, want := range []string{"会话树", "根-设置", "甲-用户", "乙-助手", "← active"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree out missing %q: %q", want, out)
		}
	}
	// 末条记录是“乙-助手”（输入顺序第二个孩子），标记必须跟它同行。
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "甲-用户") && strings.Contains(line, "active") {
			t.Errorf("wrong active line: %q", out)
		}
	}
}

// TestSessionsTreeInvalidID 验证非法/缺失会话 id 返回可读错误且退出码为 1。
func TestSessionsTreeInvalidID(t *testing.T) {
	storage := t.TempDir()
	writeJSONLSession(t, storage, "ok", []map[string]any{
		record("n1", "", "2026-11-20T00:00:00Z", "header", map[string]any{"title": "ok"}),
	})
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"path escape", "../outside"},
		{"illegal chars", "a/b"},
		{"missing", "no-such-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := treeRun(t, storage, tc.id)
			if code != 1 {
				t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
			}
			if strings.TrimSpace(errOut) == "" {
				t.Fatalf("tree error not readable: %q", errOut)
			}
			if out != "" {
				t.Fatalf("tree wrote stdout on error: %q", out)
			}
		})
	}
}

// TestSessionsTreeNoSession 验证空目录下省略 id 时给出可读错误，而不是 panic。
func TestSessionsTreeNoSession(t *testing.T) {
	code, out, errOut := treeRun(t, t.TempDir())
	if code != 1 || out != "" {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "没有可用的会话") {
		t.Fatalf("tree error not readable: %q", errOut)
	}
}

// TestSessionsTreeCurrentSession 验证省略 id 时渲染最近更新的会话。
func TestSessionsTreeCurrentSession(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-20T00:00:00Z"
	writeJSONLSession(t, storage, "old", []map[string]any{
		record("old1", "", stamp, "header", map[string]any{"title": "old"}),
	})
	writeJSONLSession(t, storage, "new", []map[string]any{
		record("new1", "", stamp, "header", map[string]any{"title": "new"}),
		record("new2", "new1", stamp, "user", map[string]any{"text": "最近"}),
	})
	dir := filepath.Join(storage, "sessions")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "old.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "new.jsonl"), newer, newer); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := treeRun(t, storage)
	if code != 0 {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(out, "会话树: new") || !strings.Contains(out, "new2  ← active") {
		t.Fatalf("current session tree = %q", out)
	}
}

// TestSessionsTreeDepthBound 验证深度上界：默认裁深层节点，--depth 可收窄，非法值报错。
func TestSessionsTreeDepthBound(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-20T00:00:00Z"
	records := make([]map[string]any, 0, 12)
	records = append(records, record("d0", "", stamp, "header", map[string]any{"title": "deep"}))
	for i := 1; i <= 10; i++ {
		records = append(records, record(
			fmt.Sprintf("d%d", i), fmt.Sprintf("d%d", i-1), stamp, "user",
			map[string]any{"text": "深"},
		))
	}
	writeJSONLSession(t, storage, "deep", records)

	// 显式 --depth 3：只剩 d0..d3 四层，其余 7 个节点被省略。
	code, out, errOut := treeRun(t, storage, "deep", "--depth", "3")
	if code != 0 {
		t.Fatalf("tree exit = %d, out %q err %q", code, out, errOut)
	}
	if !strings.Contains(out, "d3") || strings.Contains(out, "d4") {
		t.Fatalf("depth-bound tree = %q", out)
	}
	// 活动叶在 d10，超出深度范围后不在视图内，不应出现 active 标记。
	if strings.Contains(out, "← active") {
		t.Fatalf("out-of-view active leaf should not be marked: %q", out)
	}
	if !strings.Contains(out, "深度超过 3，已省略 7 个节点") {
		t.Fatalf("depth note missing: %q", out)
	}

	// --depth=N 形式等价。
	code, out, errOut = treeRun(t, storage, "deep", "--depth=2")
	if code != 0 || !strings.Contains(out, "d2") || strings.Contains(out, "d3") {
		t.Fatalf("--depth=2 exit = %d, out %q err %q", code, out, errOut)
	}

	// 非法值返回退出码 2 与可读错误。
	for _, arg := range []string{"--depth=abc", "--depth=0", "--depth=100000"} {
		code, _, errOut = treeRun(t, storage, "deep", arg)
		if code != 2 || !strings.Contains(errOut, "--depth") {
			t.Fatalf("%s exit = %d, err %q", arg, code, errOut)
		}
	}
}

// TestSessionsTreeDocumentedInUsage 验证 tree 同时出现在 sessions 与顶层 usage 中。
func TestSessionsTreeDocumentedInUsage(t *testing.T) {
	var sessions bytes.Buffer
	sessionsUsage(&sessions)
	var top bytes.Buffer
	writeUsage(&top)
	if !strings.Contains(sessions.String(), "sessions tree") {
		t.Errorf("sessions usage does not mention tree: %q", sessions.String())
	}
	if !strings.Contains(top.String(), "tree") {
		t.Errorf("top-level usage does not mention tree: %q", top.String())
	}
}
