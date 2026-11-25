package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/memory"
)

func TestTouchedPathCollectorReminderOnly(t *testing.T) {
	base := t.TempDir()
	secret := "LOCAL_RULE_BODY_NOT_INJECTED"
	if err := os.MkdirAll(filepath.Join(base, "src", "parser"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "src", "parser", "AGENTS.md"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewTouchedPathCollector(base)
	c.Add(filepath.Join("src", "parser", "x.go"), filepath.Join("src", "parser", "x.go"))
	if c.Count() != 1 {
		t.Fatalf("duplicate paths should collapse, count = %d", c.Count())
	}

	reminder := c.Reminder()
	if reminder == "" {
		t.Fatal("expected a reminder for the touched nested path")
	}
	if strings.Contains(reminder, secret) {
		t.Fatalf("reminder must not carry document bodies: %q", reminder)
	}
	if !strings.Contains(reminder, "src/parser/AGENTS.md") {
		t.Fatalf("reminder missing nested path: %q", reminder)
	}
	if len(reminder) > memory.NestedHintMaxBytes {
		t.Fatalf("reminder exceeds byte budget: %d", len(reminder))
	}

	c.Reset()
	if c.Count() != 0 || c.Reminder() != "" {
		t.Fatal("Reset should drop collected paths")
	}
}

func TestTouchedPathCollectorBounding(t *testing.T) {
	c := NewTouchedPathCollector(t.TempDir())
	for i := 0; i < memory.TouchedPathLimit+50; i++ {
		c.Add(filepath.Join("dir", "file"+string(rune(i%251))+string(rune(i/251))+".go"))
	}
	if c.Count() > memory.TouchedPathLimit {
		t.Fatalf("collector exceeded limit: %d", c.Count())
	}
}
