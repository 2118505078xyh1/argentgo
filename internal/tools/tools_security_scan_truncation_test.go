package tools

import (
	"fmt"
	"testing"
)

// TestSecurityScanMaxFilesCapMarksResultTruncated 验证 max_files 上限被触发时，
// 结果必须显式声明自己被截断。此前只有在 findings 数量触及独立上限时才会
// 设置 truncated，导致扫描静默丢弃其余文件却向模型谎报“完整扫描”。
func TestSecurityScanMaxFilesCapMarksResultTruncated(t *testing.T) {
	root := t.TempDir()
	// 准备远多于上限的文件；每个文件只产生一条发现，确保 findings
	// 总数远低于 securityScanMaxFindings，排除另一种截断原因的干扰。
	for i := 0; i < 6; i++ {
		writeScanFile(t, root, fmt.Sprintf("d%02d/f.go", i), "var k = \""+scanTestAWSKey+"\"\n")
	}

	const args = `{"max_files":2}`
	result, out := runSecurityScan(t, root, args)
	if result.ScannedFiles != 2 {
		t.Fatalf("scanned_files = %d, want 2", result.ScannedFiles)
	}
	if !result.Truncated {
		t.Fatalf("max_files 上限已触发，结果必须标记 truncated；实际输出: %s", out)
	}
	if result.Marker == "" {
		t.Fatalf("截断结果必须带有 marker；实际输出: %s", out)
	}
}

// TestSecurityScanUnderMaxFilesCapIsNotMarkedTruncated 是反向保护：
// 当文件数恰好等于上限（没有文件被丢弃）时，不得误报截断。
func TestSecurityScanUnderMaxFilesCapIsNotMarkedTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		writeScanFile(t, root, fmt.Sprintf("d%02d/f.go", i), "var k = \""+scanTestAWSKey+"\"\n")
	}

	result, out := runSecurityScan(t, root, `{"max_files":2}`)
	if result.ScannedFiles != 2 {
		t.Fatalf("scanned_files = %d, want 2", result.ScannedFiles)
	}
	if result.Truncated || result.Marker != "" {
		t.Fatalf("文件数未超过上限时不应标记截断；实际输出: %s", out)
	}
}
