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

// `sessions import --format` 接受的导入格式值。
const (
	importFormatAuto   = "auto"
	importFormatClaude = "claude"
	importFormatCodex  = "codex"
)

// sessionsImport 把外部 JSONL 转录转换为原生会话，并打印新的会话 id。
// 导入函数本身位于 internal/session，是纯读取器；持久化在这里通过会话
// store 完成，与 `sessions undo|retry|name` 按目录打开会话的方式一致。
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

// sniffImportFormat 从开头的几行检测外部格式。Claude Code 发出
// type=user/assistant 事件；Codex 以一行 session_meta 开头。扫描窗口有界，
// 使开头的 summary/progress 行无法隐藏真正的格式。格式错误和空行会被跳过，
// 与 internal/session 中宽松的读取器保持一致。
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

// persistImportedSession 把投影后的条目作为一个全新的原生会话写入。CLI
// 注入的 SessionStore 是只读的，因此写入直接走 internal/session；返回的
// id 是 store 自己的会话 id。
//
// 导入条目的 id/parent/时间戳是来自外部文件的溯源信息；store 在
// AppendRecord 时会生成自己的 n%06d 记录 id、父子链和时间戳。角色、内容、
// 工具调用和 usage 会保留在记录数据中，对话视图读取的正是这些内容。
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

// importEntryKind 把投影后的角色映射为原生记录 kind。未知角色会被丢弃，
// 而不是写成不可用的记录。
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

// importEntryPayload 返回单个导入条目要存储的数据。导入辅助函数已经构建了
// role/content/tool_calls/usage 对象，因此它会被原样传递。只有没有数据的
// 条目（ReconcileToolPairs 插入的合成中止工具结果）需要基于投影字段重建
// 负载，否则它们的内容和工具调用 id 会在回读时丢失。
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
