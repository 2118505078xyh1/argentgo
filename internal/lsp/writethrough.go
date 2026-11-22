package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// FileRename 描述一次文件级重命名：OldURI 是原路径的 URI，NewURI 是目标路径的 URI。
// 路径可通过 PathToURI 转换得到。
type FileRename struct {
	OldURI string `json:"oldUri"`
	NewURI string `json:"newUri"`
}

// 文件变更类型，对应 LSP 的 FileChangeType。
const (
	FileChangeCreated int = 1
	FileChangeChanged int = 2
	FileChangeDeleted int = 3
)

// FileChange 描述一次工作区文件变更通知。Type 取 FileChangeCreated/Changed/Deleted。
type FileChange struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

// WorkspaceDiagnosticReport 是 workspace/diagnostic 返回的单个文档的诊断报告。
// 未变更的报告只带 ResultID 而 Diagnostics 为空。
type WorkspaceDiagnosticReport struct {
	URI         string       `json:"uri"`
	Kind        string       `json:"kind,omitempty"`
	ResultID    string       `json:"resultId,omitempty"`
	Version     int          `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// WillRenameFiles 请求语言服务器参与文件级重命名，返回需要同步应用的跨文件编辑。
// 服务器可能返回 null 表示没有额外编辑，此时返回 (nil, nil)。
func (c *Client) WillRenameFiles(ctx context.Context, renames []FileRename) (*WorkspaceEdit, error) {
	files := renames
	if files == nil {
		files = []FileRename{}
	}
	raw, err := c.call(ctx, "workspace/willRenameFiles", map[string]any{
		"files": files,
	})
	if err != nil {
		return nil, err
	}
	return parseWorkspaceEdit(raw), nil
}

// DidChangeWatchedFiles 通知语言服务器工作区发生了文件创建/修改/删除，
// 使其刷新内部文件索引与跨文件引用。该请求没有响应。
func (c *Client) DidChangeWatchedFiles(ctx context.Context, changes []FileChange) error {
	entries := changes
	if entries == nil {
		entries = []FileChange{}
	}
	return c.notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{
		"changes": entries,
	})
}

// WorkspaceDiagnostics 请求整个工作区的诊断（LSP 3.17 的 workspace/diagnostic）。
// 不支持该能力的服务器会返回错误，由调用方决定是否忽略。
func (c *Client) WorkspaceDiagnostics(ctx context.Context) ([]WorkspaceDiagnosticReport, error) {
	raw, err := c.call(ctx, "workspace/diagnostic", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseWorkspaceDiagnostics(raw), nil
}

// parseWorkspaceDiagnostics 解码 workspace/diagnostic 的 items 数组。
// null、空结果或非对象返回 nil，与包内其他 parse 辅助保持一致。
func parseWorkspaceDiagnostics(raw json.RawMessage) []WorkspaceDiagnosticReport {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || trimmed[0] != '{' {
		return nil
	}
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil
	}
	if len(payload.Items) == 0 {
		return nil
	}
	out := make([]WorkspaceDiagnosticReport, 0, len(payload.Items))
	for _, entry := range payload.Items {
		var item struct {
			URI         string          `json:"uri"`
			Kind        string          `json:"kind"`
			ResultID    string          `json:"resultId"`
			Version     int             `json:"version"`
			Diagnostics []rawDiagnostic `json:"diagnostics"`
		}
		if err := json.Unmarshal(entry, &item); err != nil {
			continue
		}
		if item.URI == "" {
			continue
		}
		report := WorkspaceDiagnosticReport{
			URI:      item.URI,
			Kind:     item.Kind,
			ResultID: item.ResultID,
			Version:  item.Version,
		}
		for _, d := range item.Diagnostics {
			report.Diagnostics = append(report.Diagnostics, Diagnostic{
				URI:      item.URI,
				Range:    Range{Start: d.Range.Start, End: d.Range.End},
				Severity: severityName(d.Severity),
				Message:  d.Message,
				Source:   d.Source,
			})
		}
		out = append(out, report)
	}
	return out
}
