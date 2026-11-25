package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

// ExportOptions 控制 ExportProject 如何渲染转录。
type ExportOptions struct {
	Branch   string // 要导出其路径的记录 id（或唯一前缀）；为空则跟随活动叶节点
	Sanitize bool   // 渲染前对内容、路径、工具载荷和凭据进行脱敏
	Format   string // "html" (default), "json" or "markdown"
}

// maxExportBytes 限制分享文件的大小，使非常大的会话不会产出无界的文件。
// 文本格式（HTML/Markdown）追加 exportTruncatedMarker；
// JSON 格式则从结构上限制（见 capExportJSON），使产物始终是有效的 JSON 数组。
const maxExportBytes = 2 << 20 // 2 MiB

const exportTruncatedMarker = "\n[truncated: export exceeded 2MiB limit]\n"

// sensitiveExportKeys 将 JSON 键映射到其脱敏占位符中使用的"kind"。
// 具有这些名称的字段携带内容、文件系统路径、命令或工具载荷，
// 绝不能以明文形式出现在可移植的分享文件中。该映射刻意做到详尽：
// 未知的键仍会经过 security.Redact 的凭据模式检查。
var sensitiveExportKeys = map[string]string{
	"content":     "content",
	"text":        "text",
	"prompt":      "prompt",
	"message":     "message",
	"question":    "text",
	"reply":       "text",
	"note":        "text",
	"output":      "tool-output",
	"result":      "tool-output",
	"stdout":      "tool-output",
	"stderr":      "tool-output",
	"response":    "tool-output",
	"command":     "command",
	"cmd":         "command",
	"args":        "tool-args",
	"arguments":   "tool-args",
	"input":       "tool-args",
	"payload":     "tool-args",
	"path":        "path",
	"file":        "path",
	"filepath":    "path",
	"filename":    "path",
	"dir":         "path",
	"directory":   "path",
	"target":      "path",
	"cwd":         "path",
	"url":         "url",
	"uri":         "url",
	"title":       "title",
	"description": "description",
	"summary":     "summary",
	"reason":      "reason",
	"snippet":     "snippet",
	"skill":       "skill",
	"agent":       "agent",
	"shell":       "shell",
	"script":      "script",
	"code":        "code",
	"patch":       "patch",
	"diff":        "diff",
	"query":       "query",
}

// exportEntry 是结构化（JSON/Markdown）转录中的一条记录。
type exportEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"` // 仅在合成的截断记录上设置
}

// exportTruncatedNote 是 JSON 转录被截断时追加的合成记录所携带的说明，
// 使该说明可被机器读取，且产物仍是有效的 JSON 数组。
const exportTruncatedNote = "truncated: export exceeded 2MiB limit"

// ExportProject 将会话的一个分支渲染为可移植的、可选择脱敏的分享文件。
// project 可以是 *Session 或会话目录（字符串）；给定目录时，id 用于标识会话。
// 参数列表中的 branch 优先于 opts.Branch。
func ExportProject(project any, id, branch string, opts ExportOptions) ([]byte, error) {
	if branch == "" {
		branch = opts.Branch
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "html"
	}
	switch format {
	case "html", "json", "markdown":
	default:
		return nil, fmt.Errorf("session: unsupported export format %q (want html, json or markdown)", opts.Format)
	}

	records, path, sessionID, err := loadExportRecords(project, id)
	if err != nil {
		return nil, err
	}
	if id != "" {
		sessionID = id
	}
	if sessionID == "" {
		sessionID = "session"
	}

	chain, err := exportBranch(records, branch)
	if err != nil {
		return nil, err
	}
	if opts.Sanitize {
		chain = sanitizeRecords(chain)
	}
	if len(chain) == 0 {
		return capExport(emptyExport(format, sessionID)), nil
	}

	var out []byte
	switch format {
	case "html":
		out, err = renderExportHTML(path, chain, opts.Sanitize)
		if err == nil {
			out = capExport(out)
		}
	case "json":
		// 在右方括号之后追加纯文本标记会使产物无法解析，因此改为从结构上限制 JSON。
		out, err = capExportJSON(chain)
	case "markdown":
		out, err = renderExportMarkdown(sessionID, chain)
		if err == nil {
			out = capExport(out)
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loadExportRecords 将 project 解析为记录、其支撑路径和 id。
func loadExportRecords(project any, id string) ([]Record, string, string, error) {
	switch p := project.(type) {
	case *Session:
		if p == nil {
			return nil, "", "", fmt.Errorf("session: nil session passed to export")
		}
		records, err := p.Records()
		return records, p.path(), p.ID(), err
	case string:
		path, err := ResolvePath(p, id)
		if err != nil {
			return nil, "", id, err
		}
		records, err := readRecords(path)
		return records, path, id, err
	default:
		return nil, "", id, fmt.Errorf("session: unsupported export project type %T", project)
	}
}

// exportBranch 选择 branch 从根到叶的记录链。它镜像 RenderHTML 的遍历：
// 空 branch 跟随活动（最后一条）记录，未知 branch 会报错，而不是静默回退。
func exportBranch(records []Record, branch string) ([]Record, error) {
	if len(records) == 0 {
		return nil, nil
	}
	byID := indexByID(records)
	leaf := branch
	if leaf != "" {
		// 接受完整的记录 id 或唯一前缀，与 ResolvePath 一致。
		if _, ok := byID[leaf]; !ok {
			if resolved, ok := resolveRecordPrefix(byID, leaf); ok {
				leaf = resolved
			}
		}
	}
	if leaf == "" {
		leaf = records[len(records)-1].ID
	}
	if _, ok := byID[leaf]; !ok {
		return nil, fmt.Errorf("session: unknown branch %q", branch)
	}
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok || seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func resolveRecordPrefix(byID map[string]Record, prefix string) (string, bool) {
	found := ""
	for id := range byID {
		if strings.HasPrefix(id, prefix) {
			if found != "" {
				return "", false // 不唯一
			}
			found = id
		}
	}
	return found, found != ""
}

// sanitizeRecords 返回记录的深拷贝，其 Data 载荷中所有敏感字段都被替换为
// "[redacted:<kind>:<id>]" 占位符，其余字符串都经过 security.Redact。
func sanitizeRecords(records []Record) []Record {
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = sanitizeRecord(record)
	}
	return out
}

func sanitizeRecord(record Record) Record {
	if len(record.Data) == 0 || string(record.Data) == "null" {
		return record
	}
	var value any
	// 使用 UseNumber 解码，使整数载荷在往返中精确保留。
	// json.Unmarshal 到 any 会把每个数字变成 float64，静默舍入超过 2^53 的值
	// （大 id、微秒级时间戳、token 计数器），从而破坏原本忠实的脱敏导出。
	dec := json.NewDecoder(bytes.NewReader(record.Data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		record.Data = redactedJSON("data", record.ID)
		return record
	}
	var sanitized any
	if _, ok := value.(string); ok {
		// 裸字符串载荷就是消息文本，没有可用于分类的键。
		sanitized = redactedText("text", record.ID)
	} else {
		sanitized = sanitizeExportValue(value, "", record.ID)
	}
	data, err := json.Marshal(sanitized)
	if err != nil {
		record.Data = redactedJSON("data", record.ID)
		return record
	}
	record.Data = data
	return record
}

func sanitizeExportValue(value any, key string, id string) any {
	switch typed := value.(type) {
	case string:
		if kind, ok := sensitiveExportKeys[strings.ToLower(key)]; ok {
			return redactedText(kind, id)
		}
		return security.Redact(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, inner := range typed {
			out[k] = sanitizeExportValue(inner, k, id)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			// 数组元素继承父键：`args: ["cmd", ...]`
			// 必须对每个元素脱敏，而不只是对数组本身。
			out[i] = sanitizeExportValue(inner, key, id)
		}
		return out
	default:
		// 数字、布尔值和 null 不携带自由格式文本。
		return value
	}
}

func redactedText(kind, id string) string {
	return fmt.Sprintf("[redacted:%s:%s]", kind, id)
}

func redactedJSON(kind, id string) json.RawMessage {
	data, _ := json.Marshal(redactedText(kind, id))
	return data
}

// renderExportHTML 委托给 RenderHTML。若不修改 tree.go，就无法从内存中的记录构建 HTML，
// 因此脱敏导出会把已脱敏的记录链写入临时 JSONL 文件再渲染它。对于未脱敏的导出，
// 直接渲染原文件，但仍必须把已解析链的叶节点作为 branch 传入：
// RenderHTML 在 branch 为空时跟随活动（最后一条）记录，
// 当调用方选择了非活动分支时，这会静默导出错误的路径。
func renderExportHTML(path string, chain []Record, sanitize bool) ([]byte, error) {
	if !sanitize {
		leaf := ""
		if len(chain) > 0 {
			leaf = chain[len(chain)-1].ID
		}
		var buf bytes.Buffer
		if err := RenderHTML(path, leaf, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	file, err := os.CreateTemp("", "argent-export-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("session: create export temp file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	for _, record := range chain {
		line, err := json.Marshal(record)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("session: encode export record: %w", err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			file.Close()
			return nil, fmt.Errorf("session: write export temp file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("session: close export temp file: %w", err)
	}
	var buf bytes.Buffer
	if err := RenderHTML(tempPath, "", &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderExportJSON(chain []Record) ([]byte, error) {
	entries := make([]exportEntry, 0, len(chain))
	for _, record := range chain {
		entries = append(entries, exportEntryFromRecord(record))
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("session: encode json export: %w", err)
	}
	return append(data, '\n'), nil
}

// capExportJSON 在不破坏结构的前提下将 JSON 转录限制在 maxExportBytes 之内。
// 与 capExport（用于文本格式）不同，它从不在右方括号后追加裸文本：
// 它保留能容纳的最长完整条目前缀，并追加一条合成的
// {"kind":"truncated","note":"..."} 记录。结果始终是 json.Unmarshal 可读的有效 JSON 数组。
func capExportJSON(chain []Record) ([]byte, error) {
	entries := make([]exportEntry, 0, len(chain))
	for _, record := range chain {
		entries = append(entries, exportEntryFromRecord(record))
	}

	// 快速路径：已经在上限之内的导出必须与普通渲染器逐字节一致
	// （没有标记，也没有 schema 变化）。
	full, err := renderExportJSON(chain)
	if err != nil {
		return nil, err
	}
	if len(full) <= maxExportBytes {
		return full, nil
	}

	// 序列化 n 条真实条目的前缀再加上截断记录。增加条目只会让数组变大，
	// 因此其大小随 n 单调，我们可以二分查找仍能容纳在上限内的最长前缀。
	marshal := func(n int) ([]byte, error) {
		view := make([]exportEntry, 0, n+1)
		view = append(view, entries[:n]...)
		view = append(view, exportEntry{Kind: "truncated", Role: "system", Note: exportTruncatedNote})
		data, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("session: encode json export: %w", err)
		}
		return append(data, '\n'), nil
	}

	lo, hi, best := 0, len(entries), 0
	for lo <= hi {
		mid := lo + (hi-lo)/2
		out, err := marshal(mid)
		if err != nil {
			return nil, err
		}
		if len(out) <= maxExportBytes {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	// best == 0 仍会得到一个只包含极小截断记录的数组，
	// 它远低于 2 MiB 的上限。
	return marshal(best)
}

func renderExportMarkdown(id string, chain []Record) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Session %s\n\n", id)
	for _, record := range chain {
		entry := exportEntryFromRecord(record)
		fmt.Fprintf(&buf, "**%s**: %s\n\n", entry.Role, entry.Text)
	}
	return buf.Bytes(), nil
}

// exportEntryFromRecord 将一条记录投影到导出 schema。
// 元数据记录（header、attempt、compaction 等）以尽力而为的文本渲染，
// 使分享文件是完整的转录，而不只是消息。
func exportEntryFromRecord(record Record) exportEntry {
	entry := exportEntry{Kind: record.Kind, Role: record.Kind}
	if !record.TS.IsZero() {
		entry.TS = record.TS.UTC().Format(time.RFC3339)
	}
	if message, ok := messageEntry(record); ok {
		entry.Role = message.Role
		entry.Text = message.Content
		entry.Tool = message.Name
		return entry
	}
	switch record.Kind {
	case KindHeader:
		entry.Role = "system"
		entry.Text = fieldString(record.Data, "title")
	case KindAttempt:
		entry.Role = "system"
		var attempt Attempt
		if jsonUnmarshal(record.Data, &attempt) == nil {
			entry.Text = strings.TrimSpace(attempt.Reason + " " + attempt.Snippet())
		}
		if entry.Text == "" {
			entry.Text = redactedText("attempt", record.ID)
		}
	case KindCompaction:
		entry.Role = "system"
		var compaction Compaction
		if json.Unmarshal(record.Data, &compaction) == nil {
			entry.Text = compaction.Summary
		}
	default:
		entry.Text = textOf(record.Data)
	}
	return entry
}

func emptyExport(format, id string) []byte {
	switch format {
	case "json":
		return []byte("[]\n")
	case "markdown":
		return []byte(fmt.Sprintf("# Session %s\n\n", id))
	default:
		return []byte(fmt.Sprintf("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>Session %s</title></head><body><h1>Session %s</h1></body></html>\n", id, id))
	}
}

// capExport 将产物限制在 maxExportBytes 之内，并回退到 UTF-8 符文边界，
// 使截断的分享文件永远不会停在某个码点的中间。
func capExport(data []byte) []byte {
	if len(data) <= maxExportBytes {
		return data
	}
	limit := maxExportBytes - len(exportTruncatedMarker)
	for limit > 0 && !utf8.RuneStart(data[limit]) {
		limit--
	}
	out := make([]byte, 0, maxExportBytes)
	out = append(out, data[:limit]...)
	out = append(out, exportTruncatedMarker...)
	return out
}
