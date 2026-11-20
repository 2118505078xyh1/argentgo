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
	// defaultTodoMaxChars bounds the characters a single todo write may persist
	// to session state. Without it a model can dump an unbounded plan into
	// todos.json, which is replayed into context on every later turn. The value
	// mirrors goose's GOOSE_TODO_MAX_CHARS default.
	defaultTodoMaxChars = 50_000

	// todoMaxCharsEnv overrides the budget at runtime. It is read on every call
	// (like intentSchemaDisabled) so tests and long-lived runtimes can change it
	// without a restart. A missing, malformed or non-positive value falls back to
	// the default so a typo can never silently disable the safety valve.
	todoMaxCharsEnv = "ARGENT_TODO_MAX_CHARS"
)

// ErrTodoTooLarge is returned when a todo write would exceed the character
// budget. Callers can match it with errors.Is.
var ErrTodoTooLarge = errors.New("todo: content exceeds character budget")

// todoMaxChars resolves the active character budget for a todo write.
func todoMaxChars() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(todoMaxCharsEnv))); err == nil && n > 0 {
		return n
	}
	return defaultTodoMaxChars
}

// marshalTodos serializes the list exactly as it is persisted. Both the budget
// check and the write go through it so the counted characters are the ones that
// actually land in session state.
func marshalTodos(items []todoItem) ([]byte, error) {
	return json.MarshalIndent(struct {
		Todos []todoItem `json:"todos"`
	}{Todos: items}, "", "  ")
}

// todoItem is one tracked task in the session task list.
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
	// Count runes, not bytes: a CJK character is three bytes in UTF-8, so a byte
	// budget would reject plans roughly three times earlier than advertised.
	limit := todoMaxChars()
	if used := utf8.RuneCount(data); used > limit {
		return "", fmt.Errorf("%w: %d runes exceed the %d-rune limit (set %s to raise it)", ErrTodoTooLarge, used, limit, todoMaxCharsEnv)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
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
