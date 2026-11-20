package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// Import format values accepted by `sessions import --format`.
const (
	importFormatAuto   = "auto"
	importFormatClaude = "claude"
	importFormatCodex  = "codex"
)

// sessionsImport converts a foreign JSONL transcript into a native session and
// prints the new session id. The import functions themselves live in
// internal/session and are pure readers; persistence is done here through the
// session store, the same way `sessions undo|retry|name` open a session by dir.
func sessionsImport(app App, path string, args []string) int {
	format := importFormatAuto
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--format":
			if i+1 >= len(args) {
				fmt.Fprintln(app.IO.Err, "argent: --format needs a value (claude|codex|auto)")
				sessionsUsage(app.IO.Err)
				return 2
			}
			i++
			format = args[i]
		case strings.HasPrefix(args[i], "--format="):
			format = strings.TrimPrefix(args[i], "--format=")
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected import argument %q\n", args[i])
			sessionsUsage(app.IO.Err)
			return 2
		}
	}
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case importFormatAuto, importFormatClaude, importFormatCodex:
	default:
		fmt.Fprintf(app.IO.Err, "argent: invalid import format %q (want claude|codex|auto)\n", format)
		sessionsUsage(app.IO.Err)
		return 2
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: read %s: %v\n", path, err)
		return 1
	}

	if format == importFormatAuto {
		format, err = sniffImportFormat(data)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: detect format of %s: %v\n", path, err)
			return 1
		}
	}

	var entries []session.Entry
	switch format {
	case importFormatClaude:
		entries, err = session.ImportClaudeCodeJSONL(bytes.NewReader(data))
	case importFormatCodex:
		entries, err = session.ImportCodexJSONL(bytes.NewReader(data))
	}
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: import %s: %v\n", path, err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintf(app.IO.Err, "error: no messages found in %s (format %s)\n", path, format)
		return 1
	}

	id, err := persistImportedSession(app, entries)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: save imported session: %v\n", err)
		return 1
	}
	fmt.Fprintln(app.IO.Out, id)
	return 0
}

// sniffImportFormat detects the foreign format from the leading lines. Claude
// Code emits type=user/assistant events; Codex opens with a session_meta line.
// A bounded window is scanned so a leading summary/progress line cannot hide
// the real format. Malformed and blank lines are skipped, matching the lenient
// reader in internal/session.
func sniffImportFormat(data []byte) (string, error) {
	const scanLimit = 64
	scanned := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if scanned >= scanLimit {
			break
		}
		scanned++
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil || event == nil {
			continue
		}
		switch event["type"] {
		case "session_meta":
			return importFormatCodex, nil
		case "user", "assistant":
			return importFormatClaude, nil
		}
	}
	if scanned == 0 {
		return "", errors.New("empty transcript")
	}
	return "", errors.New("unrecognized transcript format (want Claude Code user/assistant events or a Codex session_meta line)")
}

// persistImportedSession writes the projected entries as a fresh native
// session. CLI's injected SessionStore is read-only, so the write goes through
// internal/session directly; the returned id is the store's own session id.
//
// Imported Entry ids/parents/timestamps are provenance from the foreign file;
// the store mints its own n%06d record ids, parent chain and timestamps on
// AppendRecord. Role, content, tool calls and usage survive in the record data,
// which is what the conversation view reads back.
func persistImportedSession(app App, entries []session.Entry) (string, error) {
	store, err := session.Open(app.sessionDir())
	if err != nil {
		return "", err
	}
	sess, err := store.NewSession("")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		kind, ok := importEntryKind(entry.Role)
		if !ok {
			continue
		}
		if _, err := sess.AppendRecord(kind, importEntryPayload(entry)); err != nil {
			_ = store.Delete(sess.ID())
			return "", fmt.Errorf("append imported %s record: %w", entry.Role, err)
		}
	}
	return sess.ID(), nil
}

// importEntryKind maps a projected role onto a native record kind. Unknown
// roles are dropped rather than written as unusable records.
func importEntryKind(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return session.KindUser, true
	case "assistant":
		return session.KindAssistant, true
	case "tool":
		return session.KindTool, true
	case "system":
		return session.KindSummary, true
	default:
		return "", false
	}
}

// importEntryPayload returns the data to store for one imported entry. Import
// helpers already build a role/content/tool_calls/usage object, so it is passed
// through verbatim. Only entries without data (the synthetic aborted tool
// results ReconcileToolPairs inserts) need a payload rebuilt from the projected
// fields, otherwise their content and tool-call id would be lost on read-back.
func importEntryPayload(entry session.Entry) json.RawMessage {
	if len(entry.Data) > 0 && json.Valid(entry.Data) {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(entry.Data, &object); err == nil && object != nil {
			return append(json.RawMessage(nil), entry.Data...)
		}
	}
	payload := map[string]any{"role": entry.Role, "content": entry.Content}
	if entry.Name != "" {
		payload["name"] = entry.Name
	}
	if entry.ToolCallID != "" {
		payload["tool_call_id"] = entry.ToolCallID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}
