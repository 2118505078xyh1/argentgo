package app

// LSP 工作区诊断的落盘接线层，与 MCP 健康快照（mcp.go 的 writeMCPHealthReport）
// 共用同一范式：只提供纯写入函数，不触碰进程级 lspManager，也不调用任何会
// 懒启动语言服务器的查找（anyClient）。因此 doctor 侧读取快照时绝不会派生
// gopls 进程、绝不会联网；生产捕获调用由外部按需接入，本文件不挂接任何流程。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

// lspDiagnosticsReportFile 是 LSP 工作区诊断快照在存储目录下的文件名。
// 读取端（internal/cli 的 doctor）使用同名常量；因为 cli 不能反向 import app
// （app 已依赖 cli，会形成环），两处按约定保持一致。
const lspDiagnosticsReportFile = "lsp-diagnostics.json"

// lspDiagnosticsSnapshot 是快照的顶层结构。有意只保留 reports 一个字段，
// 与读取端 DTO 的 json tag 对齐；未知字段在读取侧会被忽略，便于后续扩展。
type lspDiagnosticsSnapshot struct {
	Reports []lsp.WorkspaceDiagnosticReport `json:"reports"`
}

// WriteLSPDiagnosticsSnapshot 把工作区诊断报告稳定排序后写入
// <storageDir>/lsp-diagnostics.json（权限 0600），供下一次 doctor 进程读取。
//
// 约定：
//   - 没有报告，或所有报告都不含诊断时，不写文件、直接返回 nil，使健康运行
//     不留下任何副作用（与 MCP 健康快照的空报告不写一致）。
//   - 写入前对报告按 URI 排序、对每份报告内的诊断按位置与内容排序，保证同一
//     输入产出逐字节相同的文件；排序在深拷贝上进行，绝不改动调用方切片。
//   - 错误用 %w 包装，交由调用方决定如何处理；本函数不吞错。
func WriteLSPDiagnosticsSnapshot(storageDir string, reports []lsp.WorkspaceDiagnosticReport) error {
	ordered := sortedLSPDiagnosticReports(reports)
	if len(ordered) == 0 {
		return nil
	}
	if storageDir == "" {
		return fmt.Errorf("write LSP diagnostics snapshot: empty storage directory")
	}

	data, err := json.Marshal(lspDiagnosticsSnapshot{Reports: ordered})
	if err != nil {
		return fmt.Errorf("encode LSP diagnostics snapshot: %w", err)
	}
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return fmt.Errorf("create LSP diagnostics snapshot directory: %w", err)
	}
	path := filepath.Join(storageDir, lspDiagnosticsReportFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write LSP diagnostics snapshot: %w", err)
	}
	return nil
}

// sortedLSPDiagnosticReports 返回报告与诊断均已排序的全新切片。跳过没有任何
// 诊断的报告（LSP 的"未变更"报告只有 ResultID），使快照只包含真正的问题。
func sortedLSPDiagnosticReports(reports []lsp.WorkspaceDiagnosticReport) []lsp.WorkspaceDiagnosticReport {
	out := make([]lsp.WorkspaceDiagnosticReport, 0, len(reports))
	for _, report := range reports {
		if len(report.Diagnostics) == 0 {
			continue
		}
		copyReport := report
		copyReport.Diagnostics = append([]lsp.Diagnostic(nil), report.Diagnostics...)
		sort.SliceStable(copyReport.Diagnostics, func(i, j int) bool {
			return lspDiagnosticLess(copyReport.Diagnostics[i], copyReport.Diagnostics[j])
		})
		out = append(out, copyReport)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].URI != out[j].URI {
			return out[i].URI < out[j].URI
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].ResultID < out[j].ResultID
	})
	return out
}

// lspDiagnosticLess 按行、列、严重性、消息、来源依次比较，避免同位置诊断的
// 顺序受输入顺序或 map 遍历影响。
func lspDiagnosticLess(a, b lsp.Diagnostic) bool {
	if a.Range.Start.Line != b.Range.Start.Line {
		return a.Range.Start.Line < b.Range.Start.Line
	}
	if a.Range.Start.Character != b.Range.Start.Character {
		return a.Range.Start.Character < b.Range.Start.Character
	}
	if a.Severity != b.Severity {
		return a.Severity < b.Severity
	}
	if a.Message != b.Message {
		return a.Message < b.Message
	}
	return a.Source < b.Source
}
