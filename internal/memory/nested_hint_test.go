package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNestedInstructionHintAppendsReminderOnly(t *testing.T) {
	base := t.TempDir()
	secret := "SECRET_BODY_NEVER_INJECTED_中文段落"
	writeFile(t, base, "AGENTS.md", secret)
	writeFile(t, filepath.Join(base, "src"), "AGENTS.md", secret)
	writeFile(t, filepath.Join(base, "src", "parser"), "AGENTS.md", secret)

	// 传入的是「工具触及」的文件路径；提醒只应列出路径，不含正文。
	hint := NestedInstructionHint(base, []string{filepath.Join("src", "parser", "x.go")})
	if hint == "" {
		t.Fatal("expected a nested instruction hint")
	}
	if strings.Contains(hint, secret) {
		t.Fatalf("hint must not include document bodies: %q", hint)
	}
	for _, want := range []string{"src/parser/AGENTS.md", "src/AGENTS.md", "AGENTS.md"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing path %q: %q", want, hint)
		}
	}
	if !strings.HasPrefix(hint, "<system-reminder>") || !strings.HasSuffix(hint, "</system-reminder>") {
		t.Fatalf("hint not wrapped as a system reminder: %q", hint)
	}
	if len(hint) > NestedHintMaxBytes {
		t.Fatalf("hint too long: %d bytes > %d", len(hint), NestedHintMaxBytes)
	}
	if strings.Count(hint, nestedHintSep)+1 > NestedHintMaxPaths {
		t.Fatalf("hint lists too many paths: %q", hint)
	}
}

func TestNestedInstructionHintBoundedWithManyDirs(t *testing.T) {
	base := t.TempDir()
	var dirs []string
	for i := 0; i < 40; i++ {
		dir := filepath.Join(base, "dir"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		writeFile(t, dir, "AGENTS.md", strings.Repeat("规则", 20))
		rel, err := filepath.Rel(base, dir)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, rel)
	}

	hint := NestedInstructionHint(base, dirs)
	if hint == "" {
		t.Fatal("expected a hint")
	}
	if len(hint) > NestedHintMaxBytes {
		t.Fatalf("hint too long: %d bytes", len(hint))
	}
	if strings.Count(hint, nestedHintSep)+1 > NestedHintMaxPaths {
		t.Fatalf("too many paths: %q", hint)
	}
	// 字节上限处不得切坏 UTF-8 rune。
	if !strings.HasSuffix(hint, "</system-reminder>") {
		t.Fatalf("hint lost its footer: %q", hint)
	}
}

func TestNestedInstructionHintEmptyWhenNoDocs(t *testing.T) {
	base := t.TempDir()
	if hint := NestedInstructionHint(base, []string{"nonexistent"}); hint != "" {
		t.Fatalf("no docs should yield empty hint, got %q", hint)
	}
}
