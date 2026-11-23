package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEditMiss(t *testing.T, root, rel, content, old string) error {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"path": rel, "old": old, "new": "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(root, true).Run(context.Background(), "edit", string(raw))
	return err
}

// TestEditMissHint 锁定 #39：old 未命中 but 与文件存在最长公共前缀时，
// 错误文案追加首个分歧行的行号与该行原文（≤160 rune）。
func TestEditMissHint(t *testing.T) {
	t.Run("multi-line divergence on later line", func(t *testing.T) {
		err := runEditMiss(t, t.TempDir(), "f.txt",
			"line one\nline two\nline three\nline four\n",
			"line one\nline two\nline THREE\nline four\n")
		if err == nil {
			t.Fatal("expected a miss error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "first divergent line 3:") {
			t.Fatalf("hint missing line number: %s", msg)
		}
		if !strings.Contains(msg, "line three") {
			t.Fatalf("hint missing divergent excerpt: %s", msg)
		}
	})

	t.Run("single-line partial match", func(t *testing.T) {
		err := runEditMiss(t, t.TempDir(), "f.txt", "foo baz\n", "foo bar")
		if err == nil {
			t.Fatal("expected a miss error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "first divergent line 1:") || !strings.Contains(msg, "foo baz") {
			t.Fatalf("unexpected hint: %s", msg)
		}
	})

	t.Run("no common prefix keeps byte-stable error", func(t *testing.T) {
		err := runEditMiss(t, t.TempDir(), "f.txt", "dup\ndup\n", "missing")
		if err == nil {
			t.Fatal("expected a miss error")
		}
		if want := "edit: old text not found in f.txt"; err.Error() != want {
			t.Fatalf("err = %q, want exactly %q", err.Error(), want)
		}
	})

	t.Run("CJK and overlong excerpt clipped to 160 runes", func(t *testing.T) {
		prefix := strings.Repeat("汉", 200)
		err := runEditMiss(t, t.TempDir(), "f.txt", prefix+"尾\n", prefix+"头\n")
		if err == nil {
			t.Fatal("expected a miss error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "first divergent line 1:") {
			t.Fatalf("hint missing: %s", msg)
		}
		// 摘录按 rune 截断，并以省略号收尾，绝不切坏 CJK。
		if !strings.HasSuffix(strings.TrimSuffix(msg, `"`), "…") {
			t.Fatalf("excerpt was not clipped: %s", msg)
		}
		if n := len([]rune(strings.Repeat("汉", 160))); n != 160 {
			t.Fatal("sanity check failed")
		}
		if strings.Contains(msg, strings.Repeat("汉", 161)) {
			t.Fatal("excerpt exceeds 160 runes")
		}
	})
}

// TestEditRepairError 锁定 repair 阶梯原有的 "most similar lines" 诊断不回退，
// 并确认它仍以 "edit repair failed" 开头。
func TestEditRepairError(t *testing.T) {
	err := editRepairError("foo baz\nqux\n", "foo bar")
	if err == nil {
		t.Fatal("expected a repair error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "edit repair failed") {
		t.Fatalf("repair error lost its prefix: %s", msg)
	}
	if !strings.Contains(msg, "most similar lines:") {
		t.Fatalf("repair error lost similar-line hint: %s", msg)
	}
	if !strings.Contains(msg, "first divergent line 1:") {
		t.Fatalf("repair error lost divergent-line hint: %s", msg)
	}
}
