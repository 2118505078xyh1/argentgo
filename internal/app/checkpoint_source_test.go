package app

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

// TestCheckpointSourceDiffLatestDefault 守护阻断修复：空 id（"最近检查点差异"）
// 必须映射到 CheckpointBack(1)，而非法步数 0 必须被拒绝，不能让工具恒定失败。
func TestCheckpointSourceDiffLatestDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	tracker := newCheckpointTracker(root, sess)
	tracker.capture(agent.Checkpoint{Paths: []string{"a.txt"}}, "prompt")
	tracker.finish(false)

	src := &checkpointSource{sess: sess, root: root}
	got, err := src.Diff("")
	if err != nil {
		t.Fatalf("Diff(\"\") 应取最近检查点: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.txt" {
		t.Fatalf("最近检查点差异不对: %+v", got)
	}
	if _, err := src.Diff("latest"); err != nil {
		t.Fatalf("Diff(\"latest\") 应取最近检查点: %v", err)
	}
	if _, err := src.Diff("0"); err == nil {
		t.Fatal("Diff(\"0\") 应拒绝非法步数 0")
	}
}
