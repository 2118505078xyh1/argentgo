package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// exportEntry 镜像 session 包中未导出的 exportEntry 的 JSON schema。
// 字段顺序和标签完全一致，意味着用 json.MarshalIndent(..., "", "  ")
// 重新序列化未过滤的转录，与 session.ExportProject 产生的输出逐字节相同
// （包括末尾的换行）。
type exportEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// ExportFilter 保存应用于结构化导出的可选谓词。
// 每个空字段都会被忽略，因此零值保留所有条目。
type ExportFilter struct {
	Since string   // 条目时间戳的包含性下界
	Until string   // 条目时间戳的包含性上界
	Types []string // 可接受的条目 kind 或角色，不区分大小写
}

// active 报告是否设置了任一谓词。值会先去空白，因此以空值给出的 flag
// （例如 --since=）会被视为没有约束。
func (f ExportFilter) active() bool {
	return strings.TrimSpace(f.Since) != "" || strings.TrimSpace(f.Until) != "" || len(f.Types) > 0
}

// filterExportEntries 返回满足所有谓词的条目：时间戳落在 [since, until]
// （含端点）区间内，且 kind 或角色出现在 types 中（不区分大小写）。时间值
// 由 parseAuditTime 解析，因此 RFC 3339、纯日期以及中文形式（年月日 /
// 时分秒）都可用。当时间边界生效时，时间戳无法解析的条目会被丢弃。
// 输入切片永远不会被修改，原始顺序会被保留。
func filterExportEntries(entries []exportEntry, since, until string, types []string) []exportEntry {
	sinceTS, hasSince := parseAuditTime(since)
	untilTS, hasUntil := parseAuditTime(until)
	wanted := make(map[string]struct{}, len(types))
	for _, typ := range types {
		if trimmed := strings.TrimSpace(typ); trimmed != "" {
			wanted[strings.ToLower(trimmed)] = struct{}{}
		}
	}

	out := make([]exportEntry, 0, len(entries))
	for _, entry := range entries {
		if len(wanted) > 0 {
			_, kindOK := wanted[strings.ToLower(entry.Kind)]
			_, roleOK := wanted[strings.ToLower(entry.Role)]
			if !kindOK && !roleOK {
				continue
			}
		}
		if hasSince || hasUntil {
			ts, ok := parseAuditTime(entry.TS)
			if !ok {
				continue
			}
			if hasSince && ts.Before(sinceTS) {
				continue
			}
			if hasUntil && ts.After(untilTS) {
				continue
			}
		}
		out = append(out, entry)
	}
	return out
}

// isFilterableExport 报告导出格式是否是结构化的（JSON 或 YAML），即 CLI
// 能否通过重新解析其条目来过滤它。HTML 和 Markdown 是单个渲染后的整体，
// 无法在这里做结构化过滤。
func isFilterableExport(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "json") || isYAMLExport(format)
}

// filterExportArtifact 对渲染后的 JSON 或 YAML 导出应用 f。YAML 由一个开头
// 注释和随后的 JSON 主体组成；先去掉注释，过滤主体，再恢复注释。当 f 不活动
// 或格式不可过滤时，原样返回原始数据，使未过滤的导出和文本导出逐字节不变。
func filterExportArtifact(data []byte, format string, f ExportFilter) ([]byte, error) {
	if !f.active() || !isFilterableExport(format) {
		return data, nil
	}

	body := data
	var header []byte
	if isYAMLExport(format) {
		header = []byte(yamlExportHeader)
		if !bytes.HasPrefix(data, header) {
			return nil, fmt.Errorf("session: malformed YAML export: missing header")
		}
		body = bytes.TrimPrefix(data, header)
	}

	var entries []exportEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("session: parse export for filtering: %w", err)
	}
	entries = filterExportEntries(entries, f.Since, f.Until, f.Types)
	filtered, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("session: encode filtered export: %w", err)
	}
	filtered = append(filtered, '\n')
	if header != nil {
		filtered = append(header, filtered...)
	}
	return filtered, nil
}
