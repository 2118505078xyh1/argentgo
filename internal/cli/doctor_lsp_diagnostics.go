package cli

// doctor 的 LSP 工作区诊断 issues 旁路。与 MCP 运行时健康快照（doctor.go 的
// doctorMCPRuntimeHealthIssues）完全同构：app 侧在 opt-in 时把已运行客户端的
// 诊断报告落到存储目录，doctor 只读这份快照，不触碰任何 LSP 客户端。
//
// 复用 doctorIssue 旁路而非新增 check：9 项 check 被 golden 锁定，issues 恒为
// warn、不计数、不改退出码；未开启 opt-in 或快照不存在/损坏时返回 nil，
// 文本与 JSON 输出逐字节不变。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// doctorLSPDiagnosticsEnv 是读取 LSP 诊断快照的 opt-in 开关。与写入端约定
	// 一致：只有值为 "1"（允许首尾空白）时才读取，默认空操作。
	doctorLSPDiagnosticsEnv = "ARGENT_LSP_DIAGNOSTICS"
	// doctorLSPDiagnosticsReportFile 必须与写入端（internal/app）的同名常量一致。
	doctorLSPDiagnosticsReportFile = "lsp-diagnostics.json"
	// doctorLSPDiagnosticsIssueName 是诊断 issue 的稳定名称。
	doctorLSPDiagnosticsIssueName = "lspDiagnostics"
	// doctorLSPDiagnosticsMaxIssues 限制单次 doctor 输出的诊断条数，避免一份
	// 超大快照淹没其余健康信息。
	doctorLSPDiagnosticsMaxIssues = 20
)

// 以下 DTO 与 lsp.WorkspaceDiagnosticReport 的 json tag 对齐。有意不 import
// internal/lsp：cli 只需读取几个文本字段，也避免让传输层依赖 LSP 实现包。
type doctorLSPDiagnosticsSnapshot struct {
	Reports []doctorLSPDiagnosticsReport `json:"reports"`
}

type doctorLSPDiagnosticsReport struct {
	URI         string                `json:"uri"`
	Diagnostics []doctorLSPDiagnostic `json:"diagnostics"`
}

type doctorLSPDiagnostic struct {
	URI      string `json:"uri"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// doctorLSPDiagnosticsIssues 读取存储目录下的诊断快照并映射为 warn 级 issue。
// opt-in 关闭、存储目录为空、文件不存在或内容损坏时一律返回 nil，保证默认
// doctor 行为逐字节不变。
func doctorLSPDiagnosticsIssues(app App) []doctorIssue {
	if strings.TrimSpace(app.env(doctorLSPDiagnosticsEnv)) != "1" {
		return nil
	}
	dir := app.storageDir()
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, doctorLSPDiagnosticsReportFile))
	if err != nil {
		return nil
	}
	var snapshot doctorLSPDiagnosticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil
	}
	return doctorLSPDiagnosticsIssuesFromReports(snapshot.Reports)
}

// doctorLSPDiagnosticsIssuesFromReports 是把报告映射为 issue 的纯函数，便于
// 测试注入报告而不触碰文件系统。报告按 URI 排序，报告内诊断按严重性与消息
// 排序，因此输出稳定；每条消息都经 redactDoctorDetail 脱敏后再交给打印层。
func doctorLSPDiagnosticsIssuesFromReports(reports []doctorLSPDiagnosticsReport) []doctorIssue {
	ordered := append([]doctorLSPDiagnosticsReport(nil), reports...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].URI < ordered[j].URI
	})

	var out []doctorIssue
	for _, report := range ordered {
		if len(out) >= doctorLSPDiagnosticsMaxIssues {
			break
		}
		uri := strings.TrimSpace(report.URI)
		count := len(report.Diagnostics)
		diagnostics := append([]doctorLSPDiagnostic(nil), report.Diagnostics...)
		sort.SliceStable(diagnostics, func(i, j int) bool {
			if diagnostics[i].Severity != diagnostics[j].Severity {
				return diagnostics[i].Severity < diagnostics[j].Severity
			}
			return diagnostics[i].Message < diagnostics[j].Message
		})
		for _, diagnostic := range diagnostics {
			if len(out) >= doctorLSPDiagnosticsMaxIssues {
				break
			}
			itemURI := uri
			if itemURI == "" {
				itemURI = strings.TrimSpace(diagnostic.URI)
			}
			if itemURI == "" {
				itemURI = "(unknown)"
			}
			severity := strings.TrimSpace(diagnostic.Severity)
			if severity == "" {
				severity = "error"
			}
			message := strings.TrimSpace(diagnostic.Message)
			if message == "" {
				message = "无诊断详情"
			}
			text := fmt.Sprintf("LSP 诊断: uri=%s severity=%s count=%d message=%s",
				itemURI, severity, count, message)
			out = append(out, doctorIssue{
				Name:    doctorLSPDiagnosticsIssueName,
				Level:   statusWarn,
				Message: redactDoctorDetail(text),
			})
		}
	}
	return out
}
