// Package session —— 外部转录导入。
//
// 这些辅助函数把其他编码 agent 写入的 JSONL 转录转换为 argent-go 原生的 Entry 投影，
// 即 Session.Messages() 返回的相同形态。它们是纯读取器：这里没有任何代码触碰 store、
// 在 store 的命名空间中生成记录 id，或接线 CLI 子命令。
//
// 该策略参照 goose 的 session/import_formats：宽松解析每一行（跳过空白和格式错误的行），
// 把外部事件映射为消息，然后运行本包规范的 tool-pair 协调，
// 使结果成为 store 可以重放的有效对话。
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// 导入 id 的前缀刻意区别于 store 的 "n%06d" id，
// 使导入来源保持可识别，且不会与 store 的 id 冲突。
const (
	claudeImportIDPrefix = "cc"
	codexImportIDPrefix  = "cx"
)

// claudeCall 是存储在 assistant Entry 的 `tool_calls` 数组中的形态。
// 它通过 Entry.Data 间接暴露给测试；保持扁平结构使载荷既易读，
// 又能被只需要 `id` 的 toolCallIDs 使用。
type claudeCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// claudeUsage 是 Claude Code 附加在 assistant 事件上的 token 统计。
// 它被保留在 Entry.Data 的 `usage` 键下。
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// ImportClaudeCodeJSONL 把 Claude Code 的 `.jsonl` 转录转换为
// argent-go 从其自身记录投影出的线性对话。
//
// Claude Code 每行写入一个带类型的事件。只有 `user` 和 `assistant` 事件携带对话内容；
// 其他每一行（附件、进度、队列操作、标题等）都会被忽略。消息的 `content` 要么是普通字符串
// （用户提示的典型情况），要么是块数组：
//
//   - `text`        —— 消息文本，用换行符连接；
//   - `tool_use`    —— assistant 工具调用，保留在 Entry.Data 的 `tool_calls` 下，
//     以便 toolCallIDs/ReconcileToolPairs 配对；
//   - `tool_result` —— 在 `user` 事件中传递，输出为一个 `tool` Entry，
//     其 ToolCallID 是发起它的 tool_use id；
//   - 其他任何内容（图片、思考等）都会被跳过。
//
// assistant 的 `message.usage` 原样保留在 Entry.Data 的 `usage` 下。
// 时间戳按 RFC3339 解析并规范化为 UTC；无法解析或缺失的时间戳使 Entry.TS 保持零值。
//
// 源数据以宽松方式读取：格式错误的行会被跳过，而不是让整个导入失败。
// 空的（或完全不含消息的）转录会返回空的非 nil 切片。只有 nil 读取器才是错误。
func ImportClaudeCodeJSONL(src io.Reader) ([]Entry, error) {
	lines, err := readImportLines(src)
	if err != nil {
		return nil, err
	}

	builder := newImportEntryBuilder(claudeImportIDPrefix)
	toolNames := make(map[string]string)

	for _, line := range lines {
		eventType := importString(line, "type")
		if eventType != "user" && eventType != "assistant" {
			continue
		}
		message := importObject(line, "message")
		if message == nil {
			continue
		}
		ts := importTimestamp(line)
		baseID := firstNonEmpty(importString(line, "uuid"), importString(line, "id"))

		switch eventType {
		case "user":
			switch content := message["content"].(type) {
			case string:
				if content != "" {
					builder.add("user", content, "", "", userImportData(content), ts, baseID)
				}
			case []any:
				for i, raw := range content {
					block, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					id := importBlockID(baseID, i)
					switch importString(block, "type") {
					case "text":
						if text := importString(block, "text"); text != "" {
							builder.add("user", text, "", "", userImportData(text), ts, id)
						}
					case "tool_result":
						callID := importString(block, "tool_use_id")
						text := importedToolResultText(block["content"])
						if importBool(block, "is_error") {
							text = "error: " + text
						}
						builder.add("tool", text, toolNames[callID], callID,
							toolImportData(toolNames[callID], callID, text), ts, id)
					}
				}
			}

		case "assistant":
			var texts []string
			var calls []claudeCall
			switch content := message["content"].(type) {
			case string:
				if content != "" {
					texts = append(texts, content)
				}
			case []any:
				for _, raw := range content {
					block, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					switch importString(block, "type") {
					case "text":
						if text := importString(block, "text"); text != "" {
							texts = append(texts, text)
						}
					case "tool_use":
						callID := importString(block, "id")
						if callID == "" {
							callID = builder.mint()
						}
						name := importString(block, "name")
						if name == "" {
							name = "unknown_tool"
						}
						args, _ := json.Marshal(block["input"])
						if len(args) == 0 || string(args) == "null" {
							args = json.RawMessage(`{}`)
						}
						calls = append(calls, importToolCall(callID, name, args))
						toolNames[callID] = name
					}
				}
			}
			if len(texts) == 0 && len(calls) == 0 {
				continue
			}
			text := strings.Join(texts, "\n")
			var usage *claudeUsage
			if raw := importObject(message, "usage"); raw != nil {
				usage = new(claudeUsage)
				if encoded, err := json.Marshal(raw); err == nil {
					_ = json.Unmarshal(encoded, usage)
				}
			}
			builder.add("assistant", text, "", "", assistantImportData(text, calls, usage), ts, baseID)
		}
	}

	return ReconcileToolPairs(builder.entries), nil
}

// ImportCodexJSONL 把 Codex（OpenAI Responses）的 `.jsonl` 运行记录转换为
// 原生 Entry 投影。Codex 转录以一行 `session_meta` 开始，随后以 `response_item` 行
// 携带对话条目，其载荷类型为 `message`、`function_call` 或 `function_call_output`。
// `event_msg` 行（token 统计）和所有其他事件类型都是元数据，会被跳过。
//
// developer/system 消息是框架提示，会被跳过。session_meta/event_msg 中的 `cwd`、
// `id` 和用量总数不属于 Entry，刻意不在这里呈现。
func ImportCodexJSONL(src io.Reader) ([]Entry, error) {
	lines, err := readImportLines(src)
	if err != nil {
		return nil, err
	}

	builder := newImportEntryBuilder(codexImportIDPrefix)
	toolNames := make(map[string]string)

	for _, line := range lines {
		if importString(line, "type") != "response_item" {
			continue
		}
		payload := importObject(line, "payload")
		if payload == nil {
			continue
		}
		ts := importTimestamp(line)
		id := importString(payload, "id")

		switch importString(payload, "type") {
		case "message":
			role := importString(payload, "role")
			if role != "user" && role != "assistant" {
				continue
			}
			text := codexMessageText(payload["content"])
			if text == "" {
				continue
			}
			if role == "user" {
				builder.add("user", text, "", "", userImportData(text), ts, id)
			} else {
				builder.add("assistant", text, "", "", assistantImportData(text, nil, nil), ts, id)
			}

		case "function_call":
			callID := importString(payload, "call_id")
			if callID == "" {
				callID = id
			}
			if callID == "" {
				callID = builder.mint()
			}
			name := importString(payload, "name")
			if name == "" {
				name = "unknown_tool"
			}
			// Codex 将参数存储为 JSON 编码的字符串。
			args, _ := json.Marshal(importString(payload, "arguments"))
			calls := []claudeCall{importToolCall(callID, name, args)}
			toolNames[callID] = name
			builder.add("assistant", "", name, callID, assistantImportData("", calls, nil), ts, id)

		case "function_call_output":
			callID := importString(payload, "call_id")
			text := importString(payload, "output")
			builder.add("tool", text, toolNames[callID], callID,
				toolImportData(toolNames[callID], callID, text), ts, id)
		}
	}

	return ReconcileToolPairs(builder.entries), nil
}

// importEntryBuilder 用 parent 把导入的条目链接起来，
// 仅在源事件没有携带 id 时才生成回退 id。
type importEntryBuilder struct {
	entries []Entry
	parent  string
	seq     int
	prefix  string
}

func newImportEntryBuilder(prefix string) *importEntryBuilder {
	return &importEntryBuilder{prefix: prefix}
}

func (b *importEntryBuilder) mint() string {
	b.seq++
	return fmt.Sprintf("%s%06d", b.prefix, b.seq)
}

func (b *importEntryBuilder) add(role, content, name, callID string, data map[string]any, ts time.Time, id string) {
	if id == "" {
		id = b.mint()
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = json.RawMessage("null")
	}
	b.entries = append(b.entries, Entry{
		ID:         id,
		Parent:     b.parent,
		Role:       role,
		Content:    content,
		Name:       name,
		ToolCallID: callID,
		TS:         ts,
		Data:       raw,
	})
	b.parent = id
}

// readImportLines 解析每一个非空且为 JSON 对象的行。坏行会被跳过，
// 使残缺或外部来源的导出不会让整个导入失败。
func readImportLines(src io.Reader) ([]map[string]any, error) {
	if src == nil {
		return nil, errors.New("session: nil import source")
	}
	var lines []map[string]any
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var object map[string]any
		if err := json.Unmarshal(line, &object); err != nil || object == nil {
			continue
		}
		lines = append(lines, object)
	}
	// 扫描器错误（行过长或读取失败）会结束流；至此已解码的内容仍是一个可用的宽松导入结果。
	return lines, nil
}

func importString(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

func importObject(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	value, _ := object[key].(map[string]any)
	return value
}

func importBool(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	value, _ := object[key].(bool)
	return value
}

// importTimestamp 解析 RFC3339 时间戳，接受 Claude Code 和 Codex 都会输出的小数秒。
// 无效时间戳保持零值，使调用方可以将其与 Unix 纪元区分开。
func importTimestamp(line map[string]any) time.Time {
	stamp := importString(line, "timestamp")
	if stamp == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// importBlockID 为同一事件内的每个块赋予不同的 id，同时保留源 uuid 作为来源标记。
// 缺少 uuid 时回退到生成的 id。
func importBlockID(base string, index int) string {
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s#%d", base, index)
}

func importToolCall(id, name string, arguments json.RawMessage) claudeCall {
	return claudeCall{ID: id, Name: name, Arguments: arguments}
}

func userImportData(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

func toolImportData(name, callID, text string) map[string]any {
	return map[string]any{
		"role":         "tool",
		"name":         name,
		"tool_call_id": callID,
		"content":      text,
	}
}

func assistantImportData(text string, calls []claudeCall, usage *claudeUsage) map[string]any {
	data := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		data["tool_calls"] = calls
		data["name"] = calls[0].Name
	}
	if usage != nil {
		data["usage"] = usage
	}
	return data
}

// importedToolResultText 把 Claude Code 的 tool_result `content` 展平，
// 它要么是字符串，要么是 text/tool_reference 块的数组。
func importedToolResultText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch importString(block, "type") {
			case "text":
				if text := importString(block, "text"); text != "" {
					parts = append(parts, text)
				}
			case "tool_reference":
				if name := importString(block, "tool_name"); name != "" {
					parts = append(parts, "[tool_reference: "+name+"]")
				}
			default:
				if encoded, err := json.Marshal(block); err == nil {
					parts = append(parts, string(encoded))
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if encoded, err := json.Marshal(value); err == nil {
			return string(encoded)
		}
	}
	return ""
}

// codexMessageText 连接 Codex 消息载荷中携带文本的块。
func codexMessageText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch importString(block, "type") {
			case "input_text", "text", "output_text":
				if text := importString(block, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
