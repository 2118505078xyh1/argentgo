package tools

// 对通过 Registry.Run 使用 checkpoint_diff 的端到端覆盖。数据源通过
// SetCheckpointSource 注入（与 internal/app 使用的接线方式相同），因此
// 这个假对象会走真实的注册、输出上限和只读路径。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// 场景 1：已接线数据源的新增/修改/删除文件会经历完整的
// Run 路径，并带有正确的状态、哈希、行数和确定性顺序。
func TestCheckpointDiffIntegrationRegistryRunRendersChanges(t *testing.T) {
	added := FileChangeFromBytes("a-added.go", nil, []byte("hello\nworld\n"))
	deleted := FileChangeFromBytes("b-deleted.go", []byte("bye\n"), nil)
	modified := FileChange{
		Path:         "c-modified.go",
		Status:       "modified",
		BeforeSHA256: strings.Repeat("a", 64),
		AfterSHA256:  strings.Repeat("b", 64),
		Added:        5,
		Removed:      2,
	}
	source := &fakeCheckpointSource{changes: []FileChange{modified, deleted, added}}

	registry := New(t.TempDir(), false)
	registry.SetCheckpointSource(source)

	out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":""}`)
	if err != nil {
		t.Fatalf("registry.Run: %v", err)
	}
	if source.calls != 1 || source.lastID != "" {
		t.Fatalf("source got id %q, %d call(s); want empty id, 1 call", source.lastID, source.calls)
	}

	var decoded checkpointDiffOutput
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not checkpoint JSON: %v (%s)", err, out)
	}
	if decoded.Count != 3 || len(decoded.Files) != 3 {
		t.Fatalf("output = %+v, want 3 files", decoded)
	}
	for i, want := range []string{"a-added.go", "b-deleted.go", "c-modified.go"} {
		if decoded.Files[i].Path != want {
			t.Fatalf("file[%d] = %q, want sorted path %q", i, decoded.Files[i].Path, want)
		}
	}

	byPath := make(map[string]FileChange, len(decoded.Files))
	for _, file := range decoded.Files {
		byPath[file.Path] = file
	}
	if got := byPath["a-added.go"]; got.Status != "added" || got.BeforeSHA256 != "" ||
		got.AfterSHA256 != added.AfterSHA256 || got.Added != 2 || got.Removed != 0 {
		t.Fatalf("added file = %+v, want status=added, no before hash, 2 added lines", got)
	}
	if got := byPath["b-deleted.go"]; got.Status != "deleted" || got.AfterSHA256 != "" ||
		got.BeforeSHA256 != deleted.BeforeSHA256 || got.Added != 0 || got.Removed != 1 {
		t.Fatalf("deleted file = %+v, want status=deleted, no after hash, 1 removed line", got)
	}
	if got := byPath["c-modified.go"]; got.Status != "modified" ||
		got.BeforeSHA256 != modified.BeforeSHA256 || got.AfterSHA256 != modified.AfterSHA256 ||
		got.Added != 5 || got.Removed != 2 {
		t.Fatalf("modified file = %+v, want both hashes and 5 added / 2 removed lines", got)
	}
}

// 场景 2：未接线的注册表不会公布该工具；一旦接线上 nil 或
// 会失败的数据源，工具存在但降级为 "unavailable" 结果。
// 观察到的行为：接线前不存在（未知工具），接线后给出优雅的提示
// —— 两者都在此断言。
func TestCheckpointDiffIntegrationUnavailableWithoutUsableSource(t *testing.T) {
	registry := New(t.TempDir(), false)
	for _, name := range registry.Names() {
		if name == "checkpoint_diff" {
			t.Fatal("fresh registry unexpectedly advertises checkpoint_diff")
		}
	}
	if out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":""}`); err == nil ||
		!strings.Contains(err.Error(), `unknown tool "checkpoint_diff"`) {
		t.Fatalf("unwired Run = (%q, %v), want unknown-tool error", out, err)
	}

	registry.SetCheckpointSource(nil)
	out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":""}`)
	if err != nil {
		t.Fatalf("nil-source Run returned an error: %v", err)
	}
	if !strings.Contains(out, "checkpoint diff unavailable") {
		t.Fatalf("nil-source output = %q, want unavailable message", out)
	}

	registry.SetCheckpointSource(&fakeCheckpointSource{err: fmt.Errorf("no such session")})
	out, err = registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":"missing"}`)
	if err != nil {
		t.Fatalf("failing-source Run returned an error: %v", err)
	}
	if !strings.Contains(out, "checkpoint diff unavailable") || !strings.Contains(out, "no such session") {
		t.Fatalf("failing-source output = %q, want unavailable message plus cause", out)
	}
}

// 场景 3：checkpoint_diff 是只读的 —— 它在 yolo=false 的注册表中
// 运行，而同一注册表中的变更性工具使用 --yolo 时会被拒绝。
func TestCheckpointDiffIntegrationReadOnlyWithoutYolo(t *testing.T) {
	registry := New(t.TempDir(), false)
	registry.SetCheckpointSource(&fakeCheckpointSource{changes: []FileChange{{Path: "f.go", Status: "modified"}}})

	tool, ok := registry.tools["checkpoint_diff"]
	if !ok || tool.Mutating() {
		t.Fatalf("registered checkpoint_diff = %v, mutating=%v; want read-only tool", ok, ok && tool.Mutating())
	}

	out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":"latest"}`)
	if err != nil {
		t.Fatalf("checkpoint_diff in yolo=false registry: %v", err)
	}
	if !strings.Contains(out, `"path":"f.go"`) {
		t.Fatalf("output = %q, want the source's file", out)
	}

	if _, err := registry.Run(context.Background(), "memory_retain", `{"text":"x"}`); err == nil ||
		!strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("mutating tool was not gated; registry not really yolo=false: %v", err)
	}
}

// 场景 4：渲染了 5000 个文件，但输出保持在
// 注册表的上限之内，并带有 rune 安全的截断标记。发出的计数
// 仍证明数据源产出了全部 5000 条记录。
func TestCheckpointDiffIntegrationBounds5000Files(t *testing.T) {
	changes := make([]FileChange, 5000)
	for i := range changes {
		changes[i] = FileChange{
			Path:         fmt.Sprintf("pkg/dir-%04d/file-with-a-long-name-%04d.go", i, i),
			Status:       "modified",
			BeforeSHA256: strings.Repeat("a", 64),
			AfterSHA256:  strings.Repeat("b", 64),
			Added:        12,
			Removed:      7,
		}
	}
	const capBytes = 1024
	source := &fakeCheckpointSource{changes: changes}
	registry := NewWithLimit(t.TempDir(), false, capBytes)
	registry.SetCheckpointSource(source)

	out, err := registry.Run(context.Background(), "checkpoint_diff", `{"checkpoint_id":""}`)
	if err != nil {
		t.Fatalf("registry.Run: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("source called %d times, want 1", source.calls)
	}
	if len(out) > capBytes {
		t.Fatalf("output is %d bytes, want <= %d", len(out), capBytes)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("output was not clipped: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("clipped output is not valid UTF-8: %q", out)
	}
	if !strings.Contains(out, `"count":5000`) {
		t.Fatalf("output lost the full source count: %q", out)
	}
}
