package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// staleTasksAfter 是触发"任务列表陈旧"提醒的工具调用间隔。
	staleTasksAfter = 10
	// todoNudgeMaxRunes 是提醒中点名的当前项截断上限。
	todoNudgeMaxRunes = 120
)

// todoItemLite 是 agent 侧对 todo 状态的最小投影。agent 不依赖 tools/session，
// 因此只解析模型可见的 JSON 参数，不读取持久化的 todos.json。
type todoItemLite struct {
	Text   string
	Status string
}

// todoNudgeOff 报告是否通过环境变量禁用提醒（默认开启）。
func todoNudgeOff() bool { return featureDisabled("ARGENT_TODO_NUDGE") }

// isTodoWriteCall 报告一次工具调用是否是任务列表写入。
// argentgo 的工具名是 "todo"（op=write 或省略 op 但带 todos），
// 同时兼容跨 harness 的 "todo_write"。
func isTodoWriteCall(name, args string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "todo" && n != "todo_write" && n != "todowrite" {
		return false
	}
	if n != "todo" {
		return true
	}
	var probe struct {
		Op    string          `json:"op"`
		Todos json.RawMessage `json:"todos"`
	}
	if json.Unmarshal([]byte(args), &probe) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(probe.Op), "write") || len(probe.Todos) > 0
}

// parseTodoState 从一次成功写入的参数中提取任务状态。
// 解析失败时返回 nil，调用方保留上一份已知状态，不把提醒建立在猜测上。
func parseTodoState(args string) []todoItemLite {
	var doc struct {
		Todos []struct {
			Text    string `json:"text"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(args), &doc) != nil {
		return nil
	}
	items := make([]todoItemLite, 0, len(doc.Todos))
	for _, entry := range doc.Todos {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = strings.TrimSpace(entry.Content)
		}
		if text == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(entry.Status))
		if status == "" {
			status = "pending"
		}
		items = append(items, todoItemLite{Text: text, Status: status})
	}
	return items
}

// openTodos 返回尚未完成的任务（completed/cancelled 之外）。
func openTodos(items []todoItemLite) []todoItemLite {
	out := make([]todoItemLite, 0, len(items))
	for _, item := range items {
		if item.Status == "completed" || item.Status == "cancelled" {
			continue
		}
		out = append(out, item)
	}
	return out
}

// hasOpenTodos 报告是否存在未完成项。
func hasOpenTodos(items []todoItemLite) bool { return len(openTodos(items)) > 0 }

// truncateRunes 按 rune 截断文本并追加省略号，避免切坏 CJK 与 emoji。
func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max-1]) + "…"
}

// staleTodoNote 点名当前任务的陈旧提醒。
func staleTodoNote(items []todoItemLite, calls int) string {
	open := openTodos(items)
	current := "（未点名）"
	if len(open) > 0 {
		current = truncateRunes(strings.ReplaceAll(open[0].Text, "\n", " "), todoNudgeMaxRunes)
	}
	return fmt.Sprintf("任务列表已 %d 次工具调用未更新；当前未完成项：%s。若进度已变化，请用 todo 工具更新列表。", calls, current)
}

// openTasksNote 是轮末的未完成项提醒。
func openTasksNote(items []todoItemLite) string {
	open := openTodos(items)
	if len(open) == 0 {
		return ""
	}
	current := truncateRunes(strings.ReplaceAll(open[0].Text, "\n", " "), todoNudgeMaxRunes)
	return fmt.Sprintf("仍有 %d 项任务未完成（当前：%s）；结束前请确认已全部处理或更新任务列表。", len(open), current)
}

// observeTodoCall 在工具成功后更新任务列表状态与调用计数。
// 返回是否发生了一次任务写入（用于清零计数）。
func (s *runState) observeTodoCall(call llm.ToolCall) bool {
	if isTodoWriteCall(call.Function.Name, call.Function.Arguments) {
		if state := parseTodoState(call.Function.Arguments); state != nil {
			s.todoState = state
		}
		s.callsSinceTodo = 0
		s.lastTodoNudgeAt = 0
		return true
	}
	s.callsSinceTodo++
	return false
}

// maybeStaleTodoNudge 在请求前检查是否该提醒模型更新任务列表。
// 仅在达到 10 次间隔且有未完成项时注入一次，每次运行至多每 10 次一次。
func (s *runState) maybeStaleTodoNudge() bool {
	if todoNudgeOff() || s.depth != 0 || len(s.todoState) == 0 {
		return false
	}
	if s.callsSinceTodo < staleTasksAfter {
		return false
	}
	if s.lastTodoNudgeAt > 0 && s.callsSinceTodo-s.lastTodoNudgeAt < staleTasksAfter {
		return false
	}
	if !hasOpenTodos(s.todoState) {
		return false
	}
	s.lastTodoNudgeAt = s.callsSinceTodo
	s.injectSteeringAs(nudgePayload, staleTodoNote(s.todoState, s.callsSinceTodo))
	return true
}

// maybeOpenTasksNudge 在轮末检查是否有未完成项，若有则注入一次提醒。
func (s *runState) maybeOpenTasksNudge() bool {
	if todoNudgeOff() || s.depth != 0 || s.openTasksNoted || len(s.todoState) == 0 {
		return false
	}
	note := openTasksNote(s.todoState)
	if note == "" {
		return false
	}
	s.openTasksNoted = true
	s.injectSteeringAs(nudgePayload, note)
	return true
}

// todoNudgeEnv 供诊断读取当前是否被环境变量关闭。
func todoNudgeEnv() string { return os.Getenv("ARGENT_TODO_NUDGE") }
