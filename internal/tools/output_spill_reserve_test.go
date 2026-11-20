package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// longSpillDir 建一个绝对路径明显超过旧固定预留量（256 字节）的 spill 目录，
// 用来复现“恢复路径比预留量还长，返回结果重新超过阈值”的问题。
func longSpillDir(t *testing.T, segments, width int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < segments; i++ {
		name := strings.Repeat(string(rune('a'+i%26)), width)
		dir = filepath.Join(dir, name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(long dir): %v", err)
	}
	if len(dir) <= 256 {
		t.Fatalf("测试目录路径长度 %d 不足以复现长路径问题", len(dir))
	}
	return dir
}

func TestSpillLargeOutputLongPathKeepsResultWithinThreshold(t *testing.T) {
	dir := longSpillDir(t, 4, 90)
	big := strings.Repeat("abcdefghij", 1000) // 10000 字节
	const threshold = 1024

	result, path, err := SpillLargeOutput(big, threshold, dir)
	if err != nil {
		t.Fatalf("SpillLargeOutput: %v", err)
	}
	if path == "" {
		t.Fatal("长路径下仍应写出工件")
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("工件路径 %q 不在目录 %q 下", path, dir)
	}
	// 这是本修复的核心断言：标记含长路径时，结果仍必须不超过调用方阈值。
	if len(result) > threshold {
		t.Fatalf("结果 %d 字节超过阈值 %d 字节（路径长度 %d），标记预留不足", len(result), threshold, len(path))
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != big {
		t.Fatalf("工件未保存完整输出: err=%v", err)
	}
	if !strings.HasPrefix(result, big[:64]) || !strings.HasSuffix(result, big[len(big)-64:]) {
		t.Fatal("长路径下丢失了头部或尾部窗口")
	}
	if !strings.Contains(result, path) {
		t.Fatal("结果缺少恢复链接")
	}
	if !utf8.ValidString(result) {
		t.Fatal("结果不是合法 UTF-8")
	}
}

func TestSpillLargeOutputPathTooLongDropsRecoveryLink(t *testing.T) {
	dir := longSpillDir(t, 6, 120)
	big := strings.Repeat("abcdefghij", 1000)
	const threshold = 400

	result, path, err := SpillLargeOutput(big, threshold, dir)
	if err != nil {
		t.Fatalf("SpillLargeOutput: %v", err)
	}
	if path != "" {
		t.Fatalf("路径占满预算时不应再通告恢复链接，得到 %q", path)
	}
	if strings.Contains(result, "full output:") {
		t.Fatalf("结果仍含有放不下的恢复链接: %q", result)
	}
	if !strings.Contains(result, "bytes elided") || len(result) > threshold {
		t.Fatalf("退化结果应保留截断标记且不超阈值: %d 字节, %q", len(result), result)
	}
}

func TestSpillWindowsReservesFullMarker(t *testing.T) {
	output := strings.Repeat("x", 1000)
	for _, reserve := range []int{0, 10, 90, 256, 1024} {
		head, tail := spillWindows(output, 1024, reserve)
		if len(head)+len(tail)+reserve > 1024 {
			t.Fatalf("reserve=%d: 窗口 %d + 标记 %d 超过阈值", reserve, len(head)+len(tail), reserve)
		}
		if len(head)+len(tail) > len(output) {
			t.Fatalf("reserve=%d: 窗口重叠，总长 %d 超过输出 %d", reserve, len(head)+len(tail), len(output))
		}
	}

	// 标记边界落在多字节字形中间时，切分后的两端仍须是合法 UTF-8。
	cjk := strings.Repeat("中文测试", 500)
	head, tail := spillWindows(cjk, 200, 130)
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatal("带预留量的切分破坏了 UTF-8 边界")
	}
}
