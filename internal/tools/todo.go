package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---- todo ----

const (
	// defaultTodoMaxChars 限制单次 todo 写入可持久化到
	// 会话状态的字符数。没有它，模型可以把无界计划倾倒进
	// todos.json，而后者会在之后的每一回合被重放到上下文中。该值
	// 与 goose 的 GOOSE_TODO_MAX_CHARS 默认值一致。
	defaultTodoMaxChars = 50_000

	// todoMaxCharsEnv 在运行时覆盖预算。它在每次调用时读取，
	// 像 intentSchemaDisabled 那样）使测试和长寿命运行时可以更改它
	// 无需重启。缺失、格式错误或非正的值会回退到
	// 默认值，因此拼写错误绝不会悄悄禁用这个安全阀。
	todoMaxCharsEnv = "ARGENT_TODO_MAX_CHARS"
)

// 当一次 todo 写入会超出字符预算时返回 ErrTodoTooLarge。
// 调用方可以用 errors.Is 匹配它。
var ErrTodoTooLarge = errors.New("todo: content exceeds character budget")

// todoMaxChars 解析一次 todo 写入的有效字符预算。
func todoMaxChars() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(todoMaxCharsEnv))); err == nil && n > 0 {
		return n
	}
	return defaultTodoMaxChars
}

// marshalTodos 按持久化的原样序列化列表。预算
// 检查和写入都经过它，因此被计数的字符就是
// 真正落入会话状态的字符。
func marshalTodos(items []todoItem) ([]byte, error) {
	return json.MarshalIndent(struct {
		Todos []todoItem `json:"todos"`
	}{Todos: items}, "", "  ")
}

// todoItem 是会话任务列表中被跟踪的一个任务。
type todoItem struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

const todoFileName = "todos.json"

type todoTool struct{}

func (todoTool) Name() string { return "todo" }
func (todoTool) Description() string {
	return "Maintain the session task list. Send the full list to replace it; omit todos to read it back. Status is pending, in_progress or completed."
}
func (todoTool) Mutating() bool { return false }
func (todoTool) Schema() string {
	return `{"type":"object","properties":{"op":{"type":"string","enum":["write","read"],"description":"write replaces the list, read returns it"},"todos":{"type":"array","description":"the full task list","items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["text"]}}}}`
}

func (todoTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Op    string `json:"op"`
		Todos []struct {
			ID      string `json:"id"`
			Text    string `json:"text"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("todo: bad arguments: %w", err)
	}
	stateDir := sessionDir(ctx, root)
	path := filepath.Join(stateDir, todoFileName)

	read := strings.EqualFold(strings.TrimSpace(args.Op), "read") || (args.Op == "" && args.Todos == nil)
	if read {
		items, err := loadTodos(path)
		if err != nil {
			return "", err
		}
		return renderTodos(items), nil
	}

	items := make([]todoItem, 0, len(args.Todos))
	for i, entry := range args.Todos {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			text = strings.TrimSpace(entry.Content)
		}
		if text == "" {
			continue
		}
		status := strings.TrimSpace(entry.Status)
		switch status {
		case "":
			status = "pending"
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("todo: item %d has invalid status %q (want pending, in_progress or completed)", i+1, status)
		}
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		items = append(items, todoItem{ID: id, Text: text, Status: status})
	}
	data, err := marshalTodos(items)
	if err != nil {
		return "", err
	}
	// 按 rune 计数，而非字节：一个 CJK 字符在 UTF-8 中占三个字节，因此按
	// 字节计数会让计划被拒绝的时机比宣称的早大约三倍。
	limit := todoMaxChars()
	if used := utf8.RuneCount(data); used > limit {
		return "", fmt.Errorf("%w: %d runes exceed the %d-rune limit (set %s to raise it)", ErrTodoTooLarge, used, limit, todoMaxCharsEnv)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	if err := atomicWriteTodo(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return renderTodos(items), nil
}

func loadTodos(path string) ([]todoItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Todos []todoItem `json:"todos"`
	}
	if err := json.Unmarshal(data, &doc); err == nil && doc.Todos != nil {
		return doc.Todos, nil
	}
	var items []todoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("todo: corrupt state file %s: %w", path, err)
	}
	return items, nil
}

func renderTodos(items []todoItem) string {
	if len(items) == 0 {
		return "task list is empty"
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		marker := " "
		switch item.Status {
		case "completed":
			marker = "x"
		case "in_progress":
			marker = ">"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s %s", marker, item.ID, item.Text))
	}
	return strings.Join(lines, "\n")
}
