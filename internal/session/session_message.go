package session

import (
	"encoding/json"
	"fmt"
	"strings"
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

// messageEntry projects one record onto the conversation, or reports false
// when the record is metadata, an event, or a compaction marker.
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
		}
	case KindSummary:
		entry.Role = "system"
		// "summary" is the canonical key of a Compaction payload. When a later
		// compaction drops an earlier one, retainedForCompaction re-emits its
		// system Entry as a KindSummary carrying that original Compaction Data
		// (entryKind only sees the role). Reading "summary" here keeps the
		// restored message's text identical to the Entry that was dropped;
		// otherwise it resurfaces as an empty system message and the earlier
		// summary is lost from the conversation view.
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

// reasoningOf reads the model's thought text from an assistant record. Both
// the neutral "reasoning" key and the OpenAI-compatible "reasoning_content"
// key are accepted so imported transcripts keep their reasoning too. A record
// without either key simply yields an empty string, which keeps legacy
// sessions unchanged.
func reasoningOf(raw json.RawMessage) string {
	return fieldString(raw, "reasoning", "reasoning_content")
}

// AssistantRecord builds the persisted payload for an assistant message that
// carries its reasoning text. Empty fields are omitted so records written by
// earlier builds stay byte-identical. Use it with Session.Append(KindAssistant,
// ...) when a caller has reasoning to preserve; the agent loop's llm.Message
// payload already carries the same "reasoning" key.
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

// fieldString reads the first matching string key from a JSON object payload.
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

// textOf reads a message payload: a bare JSON string, or the usual text keys
// of an object (`content`, `text`, `output`, `result`, `message`).
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

// truncateRunes clips text to limit runes, collapsing it to one line first.
func truncateRunes(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func firstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateRunes(trimmed, limit)
		}
	}
	return ""
}
