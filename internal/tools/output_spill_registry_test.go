package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegistryRunBashSpillViaExecContext 验证注册表把 tools 的 opt-in 溢出
// 能力通过 ExecContext 注入到已迁移到 internal/bash 的 bash 工具：
// 打开开关时结果带恢复标记且产物落盘；关闭时无标记且与旧 clipTo 路径逐字节一致。
func TestRegistryRunBashSpillViaExecContext(t *testing.T) {
	// 显式清空 env 开关，避免外部环境影响"关闭"分支。
	t.Setenv(spillDirEnv, "")
	t.Setenv(spillThresholdEnv, "")
	// 强制走系统 shell 路径，避免内建 shell 改变命令输出格式。
	// 内建 shell 已默认开启，只有显式 0 才是紧急关闭（空值=默认开）。
	t.Setenv("ARGENT_BUILTIN_SHELL", "0")

	const threshold = 1024
	payload := strings.Repeat("0123456789", 200) // 2000 bytes, one line
	args, err := json.Marshal(map[string]string{"command": "echo " + payload})
	if err != nil {
		t.Fatal(err)
	}
	r := New(t.TempDir(), true) // bash 是变更类工具，需要 yolo

	// 1) opt-in 开启：shell 内部的 clip 必须经由回调把完整输出落盘。
	dir := t.TempDir()
	ctx := WithOutputSpill(context.Background(), threshold, dir)
	out, err := r.Run(ctx, "bash", string(args))
	if err != nil {
		t.Fatalf("Run(bash) with spill: %v", err)
	}
	if !strings.Contains(out, "[output-spill:") {
		t.Fatalf("enabled spill produced no recovery marker: %q", out[:min(len(out), 120)])
	}
	artifacts := spillArtifacts(t, dir)
	if len(artifacts) != 1 {
		t.Fatalf("got %d spill artifacts, want 1", len(artifacts))
	}
	path := filepath.Join(dir, artifacts[0].Name())
	if !strings.Contains(out, path) {
		t.Fatalf("result is missing the recovery path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if !strings.Contains(string(data), payload) {
		t.Fatal("artifact does not contain the full command output")
	}

	// 2) 开关关闭：无恢复标记，且输出与旧的 clipTo 路径逐字节一致。
	plain, err := r.Run(context.Background(), "bash", string(args))
	if err != nil {
		t.Fatalf("Run(bash) without spill: %v", err)
	}
	if strings.Contains(plain, "[output-spill:") {
		t.Fatalf("disabled spill still produced a recovery marker: %q", plain[:min(len(plain), 120)])
	}
	// 默认上限 40000 高于输出体量，旧的 clipTo 会原样返回完整文本；
	// 工件保存的正是该完整文本，因此它是逐字节比较的可靠基线。
	if want := clipTo(string(data), maxToolOutput); plain != want {
		t.Fatalf("disabled spill output differs from legacy clipTo: got %d bytes, want %d bytes", len(plain), len(want))
	}
}
