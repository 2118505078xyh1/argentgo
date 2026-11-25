package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/tools"
)

// spillTestEnv 从一个稀疏的映射构建 stdio 风格的 env 函数。
func spillTestEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func writeSpillTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
}

// spillTestPayload 生成多行大输出。read 工具会先按 2000 rune 的上限截断超长单行
// （#38 单行预检），单行 fixture 永远到不了 clip/spill 路径；多行内容才能让测试
// 真正断言「默认裁剪」与「启用落盘」两条分支。返回的文本无尾随换行，便于逐字节比对产物。
func spillTestPayload(unit string, lines int) string {
	return strings.Repeat(unit+"\n", lines-1) + unit
}

// spillArtifactIn 返回 dir 下唯一的规范产物（app 约定的目录），
// 若不是恰好一个则失败。
func spillArtifactIn(t *testing.T, dir string) (string, string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d artifacts in %q, want 1", len(entries), dir)
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "tool-output-") || !strings.HasSuffix(name, ".log") {
		t.Fatalf("unexpected artifact name %q", name)
	}
	return filepath.Join(dir, name), name
}

func TestOutputSpillEnabledTruthyValues(t *testing.T) {
	for raw, want := range map[string]bool{
		"1": true, "true": true, "YES": true, " on ": true,
		"": false, "0": false, "false": false, "no": false, "off": false, "junk": false,
	} {
		if got := outputSpillEnabled(raw); got != want {
			t.Errorf("outputSpillEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}

// 未启用时（nil env 和空 env 都一样）必须保持 ctx 不变：大型工具
// 输出走普通的 clipTo 路径，不会出现产物目录。
func TestConfigureOutputSpillDefaultLeavesOutputUnchanged(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := spillTestPayload(strings.Repeat("abcdefghij", 30), 1000) // 多行 ~300 KiB，超过默认的落盘阈值
	writeSpillTestFile(t, root, "big.txt", big)
	reg := tools.New(root, true)

	for name, env := range map[string]func(string) string{
		"nil env":   nil,
		"empty env": spillTestEnv(nil),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := configureOutputSpill(context.Background(), env, storage)
			out, err := reg.Run(ctx, "read", `{"path":"big.txt"}`)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(out, "bytes elided") {
				t.Fatal("default run produced a spill marker")
			}
			if !strings.Contains(out, "[truncated]") {
				t.Fatal("default run did not use the ordinary truncation path")
			}
		})
	}
	if _, err := os.Stat(outputSpillArtifactDir(storage)); !os.IsNotExist(err) {
		t.Fatalf("default run created an artifact directory: stat err = %v", err)
	}
}

// 启用后，大型工具输出会落盘到存储下的约定目录，返回有界的视图，
// 且完整文本可恢复。
func TestConfigureOutputSpillOptInSpillsToConventionDir(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := spillTestPayload(strings.Repeat("0123456789", 30), 1000) // 多行 300 KiB
	writeSpillTestFile(t, root, "big.txt", big)

	ctx := configureOutputSpill(context.Background(),
		spillTestEnv(map[string]string{outputSpillEnableEnv: "1"}), storage)
	out, err := tools.New(root, true).Run(ctx, "read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "bytes elided") || !strings.Contains(out, "full output:") {
		t.Fatalf("output was not spilled: %.120q", out)
	}
	if len(out) >= len(big) {
		t.Fatalf("spilled output is %d bytes, want it bounded from %d", len(out), len(big))
	}

	dir := outputSpillArtifactDir(storage)
	path, _ := spillArtifactIn(t, dir)
	if !strings.Contains(out, path) {
		t.Fatalf("spilled output is missing the recovery path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(artifact): %v", err)
	}
	if string(data) != big {
		t.Fatal("artifact does not contain the full, recoverable output")
	}
}

// 启用时，组装步骤会运行保留清理：约定目录中的过期产物消失，
// 新的产物保留。
func TestConfigureOutputSpillCleansExpiredArtifacts(t *testing.T) {
	storage := t.TempDir()
	dir := outputSpillArtifactDir(storage)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "tool-output-0000000000000001.log")
	fresh := filepath.Join(dir, "tool-output-0000000000000002.log")
	for _, path := range []string{old, fresh} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	_ = configureOutputSpill(context.Background(),
		spillTestEnv(map[string]string{outputSpillEnableEnv: "1"}), storage)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired artifact survived assembly: stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh artifact was removed: %v", err)
	}
}

// 阈值覆盖会被遵守：当组装设置 1 KiB 阈值时，5 KiB 的输出会落盘，
// 而不是落回普通的裁剪上限。
func TestConfigureOutputSpillThresholdOverride(t *testing.T) {
	storage := t.TempDir()
	root := t.TempDir()
	big := strings.Repeat("qwerty", 900) // 5,400 bytes
	writeSpillTestFile(t, root, "big.txt", big)

	ctx := configureOutputSpill(context.Background(), spillTestEnv(map[string]string{
		outputSpillEnableEnv:    "1",
		outputSpillThresholdEnv: "1024",
	}), storage)
	out, err := tools.New(root, true).Run(ctx, "read", `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "bytes elided") {
		t.Fatalf("threshold override was ignored: %.120q", out)
	}
	spillArtifactIn(t, outputSpillArtifactDir(storage))
}
