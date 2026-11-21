package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("session: encode payload: %w", err)
	}
	return data, nil
}

// messageEntry 将一条记录投影到对话上；当记录是元数据、事件或压缩标记时返回 false。
func messageEntry(record Record) (Entry, bool) {
	entry := Entry{
		ID:     record.ID,
		Parent: record.Parent,
		TS:     record.TS,
		Data:   record.Data,
		Name:   fieldString(record.Data, "name", "tool"),
	}
	switch record.Kind {
	case KindUser, "prompt":
		entry.Role = "user"
		entry.Content = textOf(record.Data)
	case KindAssistant, "model", "reply":
		entry.Role = "assistant"
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
		entry.Reasoning = reasoningOf(record.Data)
		entry.ReasoningSignature = signatureOf(record.Data)
	case KindTool, "tool_result", "tool-result", "toolresult":
		entry.Role = "tool"
		entry.Content = firstNonEmpty(
			fieldString(record.Data, "output"),
			fieldString(record.Data, "content"),
			fieldString(record.Data, "result"),
			textOf(record.Data),
		)
		entry.ToolCallID = firstNonEmpty(
			fieldString(record.Data, "toolCallId", "tool_call_id"),
			fieldString(record.Data, "id"),
		)
	case KindMessage:
		entry.Role = fieldString(record.Data, "role")
		if entry.Role == "" {
			entry.Role = "user"
		}
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
		if entry.Role == "assistant" {
			entry.Reasoning = reasoningOf(record.Data)
			entry.ReasoningSignature = signatureOf(record.Data)
		}
	case KindSummary:
		entry.Role = "system"
		// "summary" 是 Compaction 载荷的规范键。当后续压缩丢弃更早的压缩时，
		// retainedForCompaction 会把它作为携带原始 Compaction Data 的 KindSummary 重新发出 system Entry
		// （entryKind 只能看到角色）。在这里读取 "summary" 可使恢复后的消息文本与被丢弃的 Entry 完全一致；
		// 否则它会变成一条空的系统消息，先前的摘要就会从对话视图中丢失。
		entry.Content = firstNonEmpty(fieldString(record.Data, "text"),
			fieldString(record.Data, "summary"), textOf(record.Data))
	default:
		return Entry{}, false
	}
	if entry.Role == "" {
		return Entry{}, false
	}
	return entry, true
}

func messageEntries(records []Record) []Entry {
	out := make([]Entry, 0, len(records))
	for _, record := range records {
		if entry, ok := messageEntry(record); ok {
			out = append(out, entry)
		}
	}
	return ReconcileToolPairs(out)
}

// reasoningOf 从 assistant 记录中读取模型的思考文本。中性的 "reasoning" 键和
// OpenAI 兼容的 "reasoning_content" 键都被接受，使导入的转录也能保留其推理内容。
// 两个键都不存在的记录只会得到空字符串，从而使旧会话保持不变。
func reasoningOf(raw json.RawMessage) string {
	return fieldString(raw, "reasoning", "reasoning_content")
}

// signatureOf 从 assistant 记录中读取思考文本的签名。它既接受 llm.Message
// 写入的中性 "reasoning_signature" 键，也接受归档使用的 "ReasoningSignature"
// 键，使旧记录与显式写入的记录都能被正确投影；两键都不存在时返回空字符串。
func signatureOf(raw json.RawMessage) string {
	return fieldString(raw, "reasoning_signature", "ReasoningSignature")
}

// AssistantRecord 为携带推理文本的 assistant 消息构建持久化载荷。空字段会被省略，
// 使较早版本写入的记录保持逐字节一致。当调用方有推理内容需要保留时，
// 可将其与 Session.Append(KindAssistant, ...) 一起使用；agent 循环的 llm.Message
// 载荷已经携带相同的 "reasoning" 键。
func AssistantRecord(content, reasoning string) map[string]any {
	payload := make(map[string]any, 2)
	if content != "" {
		payload["content"] = content
	}
	if reasoning != "" {
		payload["reasoning"] = reasoning
	}
	return payload
}

// AssistantRecordWithSignature 在 AssistantRecord 的基础上携带推理签名。
// 空字段同样会被省略，使不带签名的写入保持旧字节不变。
func AssistantRecordWithSignature(content, reasoning, signature string) map[string]any {
	payload := AssistantRecord(content, reasoning)
	if signature != "" {
		payload["reasoning_signature"] = signature
	}
	return payload
}

// fieldString 从 JSON 对象载荷中读取第一个匹配的字符串键。
func fieldString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

// textOf 读取消息载荷：裸 JSON 字符串，或对象常用的文本键
// （`content`、`text`、`output`、`result`、`message`）。
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"content", "text", "output", "result", "message"} {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// truncateRunes 将文本裁剪到 limit 个符文，先将其折叠为一行。
// 切点交给 textutil.ClipRunes 吸附到字素簇边界，因此组合序列、
// ZWJ emoji 与旗帜不会被从中间劈开；符文预算与 TrimSpace 语义不变。
func truncateRunes(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return strings.TrimSpace(textutil.ClipRunes(cleaned, limit))
}

func firstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateRunes(trimmed, limit)
		}
	}
	return ""
}
