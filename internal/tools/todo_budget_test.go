package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeTodo runs a todo write with a single pending item carrying text. It
// exercises the same Run path the agent uses, including the context seam.
func writeTodo(t *testing.T, root, text string) (string, error) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"todos": []map[string]string{{"id": "1", "text": text, "status": "pending"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return (todoTool{}).Run(context.Background(), root, raw)
}

func TestTodoBudgetShortWriteAccepted(t *testing.T) {
	t.Setenv(todoMaxCharsEnv, "") // force the default budget
	root := t.TempDir()
	out, err := writeTodo(t, root, "a short plan")
	if err != nil {
		t.Fatalf("short todo write rejected: %v", err)
	}
	if !strings.Contains(out, "a short plan") {
		t.Fatalf("out = %q, want the rendered plan", out)
	}
}

func TestTodoBudgetRejectsOversizedWrite(t *testing.T) {
	t.Setenv(todoMaxCharsEnv, "") // force the default budget
	root := t.TempDir()
	_, err := writeTodo(t, root, strings.Repeat("a", defaultTodoMaxChars+1))
	if !errors.Is(err, ErrTodoTooLarge) {
		t.Fatalf("err = %v, want ErrTodoTooLarge", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".argent", todoFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("state file written despite budget rejection: stat = %v", statErr)
	}
}

func TestTodoBudgetBoundary(t *testing.T) {
	items := []todoItem{{ID: "1", Text: strings.Repeat("x", 64), Status: "pending"}}
	data, err := marshalTodos(items)
	if err != nil {
		t.Fatal(err)
	}
	exact := utf8.RuneCount(data)

	// Exactly at the budget is allowed.
	t.Setenv(todoMaxCharsEnv, strconv.Itoa(exact))
	root := t.TempDir()
	if _, err := writeTodo(t, root, items[0].Text); err != nil {
		t.Fatalf("write at exactly %d runes rejected: %v", exact, err)
	}

	// One rune over is rejected.
	t.Setenv(todoMaxCharsEnv, strconv.Itoa(exact-1))
	root2 := t.TempDir()
	if _, err := writeTodo(t, root2, items[0].Text); !errors.Is(err, ErrTodoTooLarge) {
		t.Fatalf("write at %d runes under a %d budget = %v, want ErrTodoTooLarge", exact, exact-1, err)
	}
}

func TestTodoBudgetCountsRunesNotBytes(t *testing.T) {
	// Derive the fixed JSON overhead so the test stays correct if the written
	// shape changes: total runes = overhead + text runes.
	base, err := marshalTodos([]todoItem{{ID: "1", Text: "", Status: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	overhead := utf8.RuneCount(base)
	const limit = 1000
	t.Setenv(todoMaxCharsEnv, strconv.Itoa(limit))

	// A CJK plan well under the rune budget but far over it in bytes must pass.
	ok := strings.Repeat("中", limit-overhead-20)
	if len(ok) <= limit {
		t.Fatalf("test setup: byte length %d should exceed limit %d", len(ok), limit)
	}
	root := t.TempDir()
	if _, err := writeTodo(t, root, ok); err != nil {
		t.Fatalf("CJK plan of %d runes (%d bytes) rejected: %v", utf8.RuneCountInString(ok), len(ok), err)
	}

	// The same script pushed over the rune budget is rejected.
	big := strings.Repeat("中", limit-overhead+20)
	root2 := t.TempDir()
	if _, err := writeTodo(t, root2, big); !errors.Is(err, ErrTodoTooLarge) {
		t.Fatalf("oversized CJK plan err = %v, want ErrTodoTooLarge", err)
	}
}

func TestTodoMaxCharsEnvFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"unset", "", defaultTodoMaxChars},
		{"malformed", "not-a-number", defaultTodoMaxChars},
		{"zero", "0", defaultTodoMaxChars},
		{"negative", "-5", defaultTodoMaxChars},
		{"whitespace padded", " 777 ", 777},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(todoMaxCharsEnv, tc.raw)
			if got := todoMaxChars(); got != tc.want {
				t.Fatalf("todoMaxChars() = %d, want %d", got, tc.want)
			}
		})
	}
}
