package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"example.com/argentgo/internal/lsp"
)

func TestWriteLSPDiagnosticsSnapshotWritesSortedReports(t *testing.T) {
	dir := t.TempDir()
	reports := []lsp.WorkspaceDiagnosticReport{
		{
			URI: "file:///z.go",
			Diagnostics: []lsp.Diagnostic{
				{Severity: "error", Message: "later", Range: lsp.Range{Start: lsp.Position{Line: 5, Character: 0}}},
				{Severity: "warning", Message: "earlier", Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 2}}},
			},
		},
		{
			URI: "file:///a.go",
			Diagnostics: []lsp.Diagnostic{
				{Severity: "error", Message: "first", Source: "gopls"},
			},
		},
	}

	if err := WriteLSPDiagnosticsSnapshot(dir, reports); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, lspDiagnosticsReportFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot lspDiagnosticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("invalid snapshot JSON: %v\n%s", err, data)
	}
	if len(snapshot.Reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(snapshot.Reports))
	}
	if snapshot.Reports[0].URI != "file:///a.go" || snapshot.Reports[1].URI != "file:///z.go" {
		t.Fatalf("reports not sorted by URI: %+v", snapshot.Reports)
	}
	got := snapshot.Reports[1].Diagnostics
	if len(got) != 2 || got[0].Message != "earlier" || got[1].Message != "later" {
		t.Fatalf("diagnostics not sorted by position: %+v", got)
	}
	// 快照必须以 0600 写入，避免诊断文本被同机其他用户读取。Windows 的
	// 文件系统不暴露 POSIX 权限位（os.WriteFile 的 perm 被忽略），故只在
	// 类 Unix 平台断言实际权限；0600 的写入参数由生产代码固定。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestWriteLSPDiagnosticsSnapshotEmptySkipsWrite(t *testing.T) {
	dir := t.TempDir()
	if err := WriteLSPDiagnosticsSnapshot(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, lspDiagnosticsReportFile)); !os.IsNotExist(err) {
		t.Fatalf("empty reports should not create snapshot, stat err = %v", err)
	}
}

func TestWriteLSPDiagnosticsSnapshotAllEmptySkipsWrite(t *testing.T) {
	dir := t.TempDir()
	reports := []lsp.WorkspaceDiagnosticReport{
		{URI: "file:///a.go", ResultID: "unchanged"},
		{URI: "file:///b.go"},
	}
	if err := WriteLSPDiagnosticsSnapshot(dir, reports); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, lspDiagnosticsReportFile)); !os.IsNotExist(err) {
		t.Fatalf("diagnostic-free reports should not create snapshot, stat err = %v", err)
	}
}

func TestWriteLSPDiagnosticsSnapshotDoesNotMutateInput(t *testing.T) {
	reports := []lsp.WorkspaceDiagnosticReport{
		{
			URI: "file:///b.go",
			Diagnostics: []lsp.Diagnostic{
				{Severity: "error", Message: "b"},
				{Severity: "warning", Message: "a"},
			},
		},
		{URI: "file:///a.go", Diagnostics: []lsp.Diagnostic{{Severity: "error", Message: "x"}}},
	}
	if err := WriteLSPDiagnosticsSnapshot(t.TempDir(), reports); err != nil {
		t.Fatal(err)
	}
	// 排序发生在深拷贝上，调用方的切片必须保持原样。
	if reports[0].URI != "file:///b.go" || reports[1].URI != "file:///a.go" {
		t.Fatalf("input reports were reordered: %+v", reports)
	}
	if reports[0].Diagnostics[0].Message != "b" || reports[0].Diagnostics[1].Message != "a" {
		t.Fatalf("input diagnostics were reordered: %+v", reports[0].Diagnostics)
	}
}

func TestWriteLSPDiagnosticsSnapshotStableBytes(t *testing.T) {
	dir := t.TempDir()
	reports := []lsp.WorkspaceDiagnosticReport{
		{URI: "file:///b.go", Diagnostics: []lsp.Diagnostic{{Severity: "warning", Message: "w"}, {Severity: "error", Message: "e"}}},
		{URI: "file:///a.go", Diagnostics: []lsp.Diagnostic{{Severity: "error", Message: "a"}}},
	}
	if err := WriteLSPDiagnosticsSnapshot(dir, reports); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, lspDiagnosticsReportFile))
	if err != nil {
		t.Fatal(err)
	}
	// 颠倒输入顺序后再次写入，结果必须逐字节一致。
	reversed := []lsp.WorkspaceDiagnosticReport{reports[1], reports[0]}
	otherDir := t.TempDir()
	if err := WriteLSPDiagnosticsSnapshot(otherDir, reversed); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(otherDir, lspDiagnosticsReportFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("snapshot bytes are unstable:\n%s\n%s", first, second)
	}
}
