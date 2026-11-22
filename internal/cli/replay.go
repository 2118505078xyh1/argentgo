package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// runReplay 实现 `replay [id] [--json] [--verify]`。不带 id 时重放最近更新的会话。
// --verify 走独立诊断分支，默认（无 --verify）的转录渲染保持逐字节不变。
func runReplay(app App, _ context.Context, args []string) int {
	id := ""
	asJSON := false
	verify := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			replayUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		case arg == "--verify":
			verify = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(app.IO.Err, "argentgo: unknown flag %q\n", arg)
			replayUsage(app.IO.Err)
			return 2
		default:
			if id != "" {
				fmt.Fprintf(app.IO.Err, "argentgo: unexpected argument %q\n", arg)
				replayUsage(app.IO.Err)
				return 2
			}
			id = arg
		}
	}

	store := app.sessionStore()
	title := ""
	if id == "" {
		infos, err := store.List()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if len(infos) == 0 {
			fmt.Fprintln(app.IO.Out, "no sessions recorded")
			return 0
		}
		sort.SliceStable(infos, func(i, j int) bool { return infos[i].Updated.After(infos[j].Updated) })
		id, title = infos[0].ID, infos[0].Title
	}
	events, err := store.Load(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if verify {
		return replayVerify(app, id, title, events)
	}
	if asJSON {
		for _, event := range events {
			line, err := json.Marshal(event)
			if err != nil {
				fmt.Fprintln(app.IO.Err, "error:", err)
				return 1
			}
			fmt.Fprintln(app.IO.Out, string(line))
		}
		return 0
	}
	fmt.Fprintf(app.IO.Out, "== %s %s ==\n", id, title)
	for _, event := range events {
		if line := replayLine(event); line != "" {
			fmt.Fprintln(app.IO.Out, line)
		}
	}
	return 0
}

// replayVerify 读取会话记录并调用 session 的重放原语，输出结构化诊断。
//
// 它把读取到的 SessionEvent 映射为 session.Record（两者线格式一致），随后
// 调用 Replay/ReplaySummary/HintFromReplay/ReplayFingerprint/
// VerifyReplayFingerprint 与 ClassifyRecovery，输出分类、未配对数、断链、
// 活跃叶与指纹校验结果，让会话结构审计有用户可见的出口。
// 该函数只读入参，不写 JSONL，也不触碰 app 侧的恢复口径。
func replayVerify(app App, id, title string, events []server.SessionEvent) int {
	records := make([]session.Record, 0, len(events))
	for _, event := range events {
		records = append(records, session.Record{
			ID:     event.ID,
			Parent: event.Parent,
			TS:     event.TS,
			Kind:   event.Kind,
			Data:   event.Data,
		})
	}

	st := session.Replay(records)
	hint := session.HintFromReplay(st)
	fp := session.ReplayFingerprint(st)
	// 指纹以本次重放状态为基准，再对同一记录流重放比对：确认重放是确定性的。
	// 原语保留在 session 包内，这里只消费其结果，不写回任何持久化位置。
	verifyErr := session.VerifyReplayFingerprint(records, fp)

	// ClassifyRecovery 接收的是线性对话 Entry，而 session 的投影函数未导出。
	// 这里按同一口径做最小投影，仅填充分类真正需要的角色、调用 id、parent 与文本。
	plan := session.ClassifyRecovery(verifyEntries(records))

	fmt.Fprintf(app.IO.Out, "== verify %s %s ==\n", id, title)
	fmt.Fprintf(app.IO.Out, "Category: %s\n", plan.Category)
	fmt.Fprintf(app.IO.Out, "Unpaired: %d\n", hint.UnpairedCount)
	fmt.Fprintf(app.IO.Out, "Broken links: %d\n", plan.BrokenLinks)
	fmt.Fprintf(app.IO.Out, "Active leaf: %s\n", st.ActiveLeaf)
	fmt.Fprintf(app.IO.Out, "Summary: %s\n", session.ReplaySummary(st))
	fmt.Fprintf(app.IO.Out, "Hint: %s\n", hint.Text)
	fmt.Fprintf(app.IO.Out, "Fingerprint: %x\n", fp)
	if verifyErr != nil {
		fmt.Fprintf(app.IO.Out, "Verify: %v\n", verifyErr)
	} else {
		fmt.Fprintln(app.IO.Out, "Verify: ok")
	}
	return 0
}

// verifyEntries 把记录投影为对话条目，口径对齐 session 包的 messageEntry：
// 角色、工具调用 id、parent/id 与文本。session 的投影函数未导出且本轮禁止改动
// session/*，因此在此处复制其最小字段集合，仅供 ClassifyRecovery 使用。
func verifyEntries(records []session.Record) []session.Entry {
	entries := make([]session.Entry, 0, len(records))
	for _, record := range records {
		entry := session.Entry{
			ID:     record.ID,
			Parent: record.Parent,
			TS:     record.TS,
			Data:   record.Data,
		}
		switch record.Kind {
		case session.KindUser, "prompt":
			entry.Role = "user"
			entry.Content = verifyTextOf(record.Data)
		case session.KindAssistant, "model", "reply":
			entry.Role = "assistant"
			entry.Content = verifyTextOf(record.Data)
			entry.ToolCallID = verifyFieldString(record.Data, "toolCallId", "tool_call_id")
		case session.KindTool, "tool_result", "tool-result", "toolresult":
			entry.Role = "tool"
			entry.Content = verifyFirstNonEmpty(
				verifyFieldString(record.Data, "output"),
				verifyFieldString(record.Data, "content"),
				verifyFieldString(record.Data, "result"),
				verifyTextOf(record.Data),
			)
			entry.ToolCallID = verifyFirstNonEmpty(
				verifyFieldString(record.Data, "toolCallId", "tool_call_id"),
				verifyFieldString(record.Data, "id"),
			)
		case session.KindMessage:
			entry.Role = verifyFieldString(record.Data, "role")
			if entry.Role == "" {
				entry.Role = "user"
			}
			entry.Content = verifyTextOf(record.Data)
			entry.ToolCallID = verifyFieldString(record.Data, "toolCallId", "tool_call_id")
		case session.KindSummary:
			entry.Role = "system"
			entry.Content = verifyFirstNonEmpty(
				verifyFieldString(record.Data, "text"),
				verifyFieldString(record.Data, "summary"),
				verifyTextOf(record.Data),
			)
		default:
			continue // 元数据/事件不入投影
		}
		entries = append(entries, entry)
	}
	return entries
}

// verifyFieldString 从 JSON 对象载荷中读取第一个匹配的字符串键。
func verifyFieldString(raw json.RawMessage, keys ...string) string {
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

// verifyTextOf 读取消息载荷：裸 JSON 字符串，或对象常用的文本键。
func verifyTextOf(raw json.RawMessage) string {
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
		if value, ok := object[key].(string); ok {
			return value
		}
	}
	return ""
}

// verifyFirstNonEmpty 返回第一个非空白字符串。
func verifyFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// replayLine 把一条会话记录渲染为一行转录文本。
func replayLine(event server.SessionEvent) string {
	payload := map[string]any{}
	if len(event.Data) > 0 {
		_ = json.Unmarshal(event.Data, &payload)
	}
	text := firstText(payload, "text", "prompt", "content", "message")
	switch event.Kind {
	case "turn":
		return "" // accounting only
	case "tool":
		name := firstText(payload, "name", "tool")
		output := firstText(payload, "output", "result")
		if text == "" {
			text = output
		}
		if name == "" {
			name = "tool"
		}
		return "tool " + name + "> " + clipTranscript(text)
	case "summary", "compaction":
		return "summary> " + clipTranscript(text)
	}
	if text == "" {
		return ""
	}
	role := event.Kind
	if role == "" || role == "message" {
		if value, ok := payload["role"].(string); ok && value != "" {
			role = value
		}
	}
	return role + "> " + clipTranscript(text)
}

func firstText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func clipTranscript(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 500 {
		return text[:499] + "…"
	}
	return text
}

func replayUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo replay [<id>] [--json] [--verify]

Prints a stored session transcript. Without an id the most recently updated
session is replayed. --json prints the raw record lines instead. --verify runs
session replay diagnostics and prints the recovery category, unpaired tool
calls, broken links, active leaf and replay fingerprint.
`)
}
