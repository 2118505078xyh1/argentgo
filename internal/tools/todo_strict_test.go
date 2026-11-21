package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runTodoItems 用给定的原始任务项走一遍 todoTool.Run，与 agent 使用同一路径。
func runTodoItems(t *testing.T, root string, items []map[string]string) (string, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"todos": items})
	if err != nil {
		t.Fatal(err)
	}
	return (todoTool{}).Run(context.Background(), root, raw)
}

// todoStatePath 返回给定工作区下 todo 状态文件的路径。
func todoStatePath(root string) string {
	return filepath.Join(root, ".argent", todoFileName)
}

func TestTodoStrictOffPreservesBaselineBytes(t *testing.T) {
	// opt-in 关闭时，即使状态集违反全部三条严格不变量，也必须原样写入，
	// 且落盘字节与旧版逐字节一致。这里用单项 completed（缺验证）作基线。
	t.Setenv(todoStrictEnv, "0")
	root := t.TempDir()
	out, err := runTodoItems(t, root, []map[string]string{
		{"id": "1", "text": "x", "status": "completed"},
	})
	if err != nil {
		t.Fatalf("opt-in 关闭时写入被拒: %v", err)
	}
	if !strings.Contains(out, "x") {
		t.Fatalf("out = %q, want rendered todo", out)
	}

	data, err := os.ReadFile(todoStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\n  \"todos\": [\n    {\n      \"id\": \"1\",\n      \"text\": \"x\",\n      \"status\": \"completed\"\n    }\n  ]\n}\n"
	if string(data) != want {
		t.Fatalf("opt-in 关闭时落盘字节与基线不符:\n got: %q\nwant: %q", data, want)
	}
}

func TestTodoStrictRejectsMultipleInProgress(t *testing.T) {
	t.Setenv(todoStrictEnv, "1")
	root := t.TempDir()
	_, err := runTodoItems(t, root, []map[string]string{
		{"id": "1", "text": "A", "status": "in_progress"},
		{"id": "2", "text": "B", "status": "in_progress"},
	})
	if err == nil {
		t.Fatal("严格模式开启时，多个 in_progress 应被拒绝")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Fatalf("错误信息未指出 in_progress 问题: %q", err)
	}
	if _, statErr := os.Stat(todoStatePath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("校验失败仍写入了状态文件: %v", statErr)
	}
}

func TestTodoStrictRejectsInvalidStatus(t *testing.T) {
	t.Setenv(todoStrictEnv, "1")
	root := t.TempDir()
	_, err := runTodoItems(t, root, []map[string]string{
		{"id": "1", "text": "A", "status": "bogus"},
	})
	if err == nil {
		t.Fatal("严格模式开启时，非法状态应被拒绝")
	}
	if !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("错误信息未指出非法状态: %q", err)
	}
	if _, statErr := os.Stat(todoStatePath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("校验失败仍写入了状态文件: %v", statErr)
	}
}

func TestTodoStrictRejectsMissingVerification(t *testing.T) {
	t.Setenv(todoStrictEnv, "1")
	root := t.TempDir()
	_, err := runTodoItems(t, root, []map[string]string{
		{"id": "1", "text": "A", "status": "completed"},
		{"id": "2", "text": "B", "status": "completed"},
	})
	if err == nil {
		t.Fatal("严格模式开启时，全部完成但缺少验证记录应被拒绝")
	}
	if !strings.Contains(err.Error(), "verification") {
		t.Fatalf("错误信息未指出缺少验证记录: %q", err)
	}
	if _, statErr := os.Stat(todoStatePath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("校验失败仍写入了状态文件: %v", statErr)
	}
}

func TestTodoStrictAcceptsValidState(t *testing.T) {
	t.Setenv(todoStrictEnv, "1")
	root := t.TempDir()
	out, err := runTodoItems(t, root, []map[string]string{
		{"id": "1", "text": "A", "status": "in_progress"},
		{"id": "2", "text": "B", "status": "pending"},
	})
	if err != nil {
		t.Fatalf("合法状态在严格模式下被拒: %v", err)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Fatalf("out = %q, want rendered todos", out)
	}
	if _, statErr := os.Stat(todoStatePath(root)); statErr != nil {
		t.Fatalf("合法状态未落盘: %v", statErr)
	}
}

func TestTodoStrictFlagFalsyValuesStayOff(t *testing.T) {
	root := t.TempDir()
	for _, raw := range []string{"", "0", "false", "no", "off"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(todoStrictEnv, raw)
			if todoStrictEnabled() {
				t.Fatalf("todoStrictEnabled(%q) = true, want false", raw)
			}
			_, err := runTodoItems(t, root, []map[string]string{
				{"id": "1", "text": "x", "status": "completed"},
			})
			if err != nil {
				t.Fatalf("opt-in=%q 应保持写路径不变，却被拒: %v", raw, err)
			}
		})
	}
}
