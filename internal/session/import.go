// Package session — foreign transcript import.
//
// These helpers convert the JSONL transcripts written by other coding agents
// into argent-go's native Entry projection, the same shape Session.Messages()
// returns. They are pure readers: nothing here touches the store, mints record
// ids in the store's namespace, or wires a CLI subcommand.
//
// The strategy mirrors goose's session/import_formats: parse each line leniently
// (blank and malformed lines are skipped), map the foreign events onto messages,
// then run the package's canonical tool-pair reconciliation so the result is a
// valid conversation a store can replay.
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

// Import id prefixes are deliberately distinct from the store's "n%06d" ids so
// imported provenance stays recognizable and cannot collide with store ids.
const (
	claudeImportIDPrefix = "cc"
	codexImportIDPrefix  = "cx"
)

// claudeCall is the shape stored in an assistant Entry's `tool_calls` array.
// It is exported to tests indirectly through Entry.Data; keeping it flat makes
// the payload both readable and usable by toolCallIDs, which only needs `id`.
type claudeCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// claudeUsage is the token accounting Claude Code attaches to assistant
// events. It is preserved in Entry.Data under `usage`.
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// ImportClaudeCodeJSONL converts a Claude Code `.jsonl` transcript into the
// linear conversation argent-go projects from its own records.
//
// Claude Code writes one typed event per line. Only `user` and `assistant`
// events carry conversation content; every other line (attachments, progress,
// queue operations, titles, ...) is ignored. A message's `content` is either a
// plain string (typical for user prompts) or an array of blocks:
//
//   - `text`        — message text, joined with newlines;
//   - `tool_use`    — an assistant tool call, preserved in Entry.Data under
//     `tool_calls` so toolCallIDs/ReconcileToolPairs can pair it;
//   - `tool_result` — delivered inside a `user` event, emitted as a `tool`
//     Entry whose ToolCallID is the originating tool_use id;
//   - anything else (image, thinking, ...) is skipped.
//
// Assistant `message.usage` is preserved verbatim in Entry.Data under `usage`.
// Timestamps are parsed as RFC3339 and normalized to UTC; an unparseable or
// missing timestamp leaves Entry.TS zero.
//
// The source is read leniently: malformed lines are skipped rather than
// failing the whole import. An empty (or entirely non-message) transcript
// yields an empty, non-nil slice. Only a nil reader is an error.
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

// ImportCodexJSONL converts a Codex (OpenAI Responses) `.jsonl` rollout into
// the native Entry projection. Codex transcripts begin with a `session_meta`
// line and then carry conversation items as `response_item` lines whose payload
// type is `message`, `function_call` or `function_call_output`. `event_msg`
// lines (token accounting) and all other event types are metadata and skipped.
//
// Developer/system messages are harness prompts and are skipped. The `cwd`,
// `id` and usage totals found in session_meta/event_msg do not belong on an
// Entry and are intentionally not surfaced here.
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
			// Codex stores arguments as a JSON-encoded string.
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

// importEntryBuilder chains imported entries by parent and mints fallback ids
// only when a source event does not carry one.
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

// readImportLines parses every non-blank line that is a JSON object. Bad lines
// are skipped so a torn or foreign export cannot make the whole import fail.
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
	// A scanner error (line too long or read failure) ends the stream; whatever
	// was decoded so far is still a usable, lenient import.
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

// importTimestamp parses an RFC3339 timestamp, accepting the fractional
// seconds Claude Code and Codex both emit. A bad timestamp stays zero so callers
// can distinguish it from the Unix epoch.
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

// importBlockID gives each block within one event a distinct id while keeping
// the source uuid as provenance. A missing uuid falls back to a minted id.
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

// importedToolResultText flattens a Claude Code tool_result `content`, which is
// either a string or an array of text/tool_reference blocks.
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

// codexMessageText joins the text-bearing blocks of a Codex message payload.
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
