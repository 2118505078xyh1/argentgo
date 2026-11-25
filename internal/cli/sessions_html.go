package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"example.com/argentgo/internal/exporthtml"
)

// exportEntryView 对应 session 包 JSON 导出的条目结构（保持字段名一致）。
type exportEntryView struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// richHTMLFromJSON 把 session 的 JSON 转录转成单文件自包含富 HTML。
// 它只做结构映射，所有安全转义由 exporthtml 完成。
func richHTMLFromJSON(data []byte) ([]byte, error) {
	var entries []exportEntryView
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode json transcript: %w", err)
	}
	msgs := make([]exporthtml.Message, 0, len(entries))
	for i, e := range entries {
		role := exporthtml.Role(e.Role)
		switch role {
		case exporthtml.RoleUser, exporthtml.RoleAssistant, exporthtml.RoleTool, exporthtml.RoleSystem:
			// 合法角色，沿用
		default:
			// 非消息条目（header/compaction/工具元数据等）作为 system 旁注，
			// 让导出完整但不伪装成对话。
			role = exporthtml.RoleSystem
		}
		text := e.Text
		if e.Note != "" {
			if text != "" {
				text += "\n"
			}
			text += e.Note
		}
		var ts time.Time
		if e.TS != "" {
			if parsed, err := time.Parse(time.RFC3339, e.TS); err == nil {
				ts = parsed
			}
		}
		msgs = append(msgs, exporthtml.Message{
			ID:       fmt.Sprintf("e%d", i),
			Role:     role,
			Content:  text,
			ToolName: e.Tool,
			TS:       ts,
		})
	}
	var buf bytes.Buffer
	if err := exporthtml.Render(&buf, msgs, exporthtml.Options{
		Title:         "argentgo session export",
		FoldTools:     true,
		ShowReasoning: false,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
