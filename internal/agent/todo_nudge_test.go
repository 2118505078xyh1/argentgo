package agent

import (
	"strings"
	"testing"
)

func TestStaleTodoNudge(t *testing.T) {
	t.Setenv("ARGENT_TODO_NUDGE", "1")
	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	s.todoState = []todoItemLite{
		{Text: "实现功能", Status: "completed"},
		{Text: "补一条很长的当前任务用于点名截断测试，这里继续填充足够多的内容以覆盖截断逻辑", Status: "in_progress"},
	}
	s.callsSinceTodo = 10
	if !s.maybeStaleTodoNudge() {
		t.Fatal("达到阈值且有未完成项时应注入")
	}
	last := s.messages[len(s.messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "任务列表已 10 次") {
		t.Fatalf("提醒内容错误: %#v", last)
	}
	if !strings.Contains(last.Content, "补一条很长的当前任务") {
		t.Fatal("未点名当前任务")
	}
	// 每 10 次一次：计数未再增加时不应重复。
	if s.maybeStaleTodoNudge() {
		t.Fatal("同一间隔内不应重复提醒")
	}
	// 全部完成后不再提醒。
	s.callsSinceTodo = 20
	s.todoState[1].Status = "completed"
	if s.maybeStaleTodoNudge() {
		t.Fatal("无未完成项时不应提醒")
	}
}

func TestOpenTasksNudge(t *testing.T) {
	t.Setenv("ARGENT_TODO_NUDGE", "1")
	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	s.todoState = []todoItemLite{{Text: "待办 A", Status: "pending"}, {Text: "待办 B", Status: "pending"}}
	if !s.maybeOpenTasksNudge() {
		t.Fatal("有未完成项时应注入轮末提醒")
	}
	if !strings.Contains(s.messages[len(s.messages)-1].Content, "仍有 2 项任务未完成") {
		t.Fatalf("提醒内容错误: %#v", s.messages[len(s.messages)-1])
	}
	if s.maybeOpenTasksNudge() {
		t.Fatal("每次运行只应提醒一次")
	}
}

func TestTodoStateParsingAndCounting(t *testing.T) {
	t.Setenv("ARGENT_TODO_NUDGE", "1")
	if !isTodoWriteCall("todo", `{"op":"write","todos":[{"text":"a"}]}`) {
		t.Fatal("todo write 未被识别")
	}
	if isTodoWriteCall("todo", `{"op":"read"}`) {
		t.Fatal("todo read 不应重置计数")
	}
	if !isTodoWriteCall("todo_write", `{"todos":[]}`) {
		t.Fatal("todo_write 未被识别")
	}
	args := `{"op":"write","todos":[{"text":"甲","status":"in_progress"},{"content":"乙"}]}`
	state := parseTodoState(args)
	if len(state) != 2 || state[0].Status != "in_progress" || state[1].Status != "pending" {
		t.Fatalf("解析错误: %#v", state)
	}
	s := newRunState(Options{Model: "m", System: "s"}, "go", nil)
	s.callsSinceTodo = 9
	s.observeTodoCall(toolCall("c1", "read", `{}`))
	if s.callsSinceTodo != 10 {
		t.Fatalf("普通工具应累加: %d", s.callsSinceTodo)
	}
	s.observeTodoCall(toolCall("c2", "todo", args))
	if s.callsSinceTodo != 0 || len(s.todoState) != 2 {
		t.Fatalf("写入应清零并更新状态: %d %#v", s.callsSinceTodo, s.todoState)
	}
}
