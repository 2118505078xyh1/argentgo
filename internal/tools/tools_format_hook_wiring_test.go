package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatHookDefaultOffKeepsToolOutputUnchanged(t *testing.T) {
	root := t.TempDir()
	registry := New(root, true)
	ctx := context.Background()

	out, err := registry.Run(ctx, "write", `{"path":"f.txt","content":"hello\n"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if want := "wrote 6 bytes to f.txt"; out != want {
		t.Fatalf("write output = %q, want %q", out, want)
	}
	out, err = registry.Run(ctx, "edit", `{"path":"f.txt","old":"hello","new":"world"}`)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if want := "edited f.txt"; out != want {
		t.Fatalf("edit output = %q, want %q", out, want)
	}
}

func TestWriteToolFormatHookWritesFinalBytesAndReportsDiff(t *testing.T) {
	root := t.TempDir()
	registry := New(root, true)
	registry.SetFormatHook(func(path string, content []byte) []byte {
		if !strings.HasSuffix(filepath.ToSlash(path), "f.go") {
			t.Errorf("hook path = %q, want f.go", path)
		}
		return []byte(strings.ReplaceAll(string(content), "  ", "\t"))
	})

	out, err := registry.Run(context.Background(), "write",
		`{"path":"f.go","content":"package p\nfunc f() {\n  x := 1\n}\n"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	wantContent := "package p\nfunc f() {\n\tx := 1\n}\n"
	got, err := os.ReadFile(filepath.Join(root, "f.go"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != wantContent {
		t.Fatalf("disk content = %q, want %q", got, wantContent)
	}
	if want := "(+4/-0 行" + formattedMarker + ")"; !strings.Contains(out, want) {
		t.Fatalf("output = %q, want to contain %q", out, want)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
}

func TestEditToolFormatHookWritesFinalBytesAndReportsDiff(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	registry := New(root, true)
	registry.SetFormatHook(func(_ string, content []byte) []byte {
		return []byte(strings.Replace(string(content), "B", "B\nb2", 1))
	})

	out, err := registry.Run(context.Background(), "edit", `{"path":"f.txt","old":"b","new":"B"}`)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	wantContent := "a\nB\nb2\nc\n"
	got, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != wantContent {
		t.Fatalf("disk content = %q, want %q", got, wantContent)
	}
	if want := "(+2/-1 行" + formattedMarker + ")"; !strings.Contains(out, want) {
		t.Fatalf("output = %q, want to contain %q", out, want)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
}

func TestFormatHookUnchangedOrEmptyUsesAfterWithoutMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook FormatHook
	}{
		{name: "identity", hook: func(_ string, content []byte) []byte { return content }},
		{name: "empty", hook: func(_ string, _ []byte) []byte { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			registry := New(root, true)
			registry.SetFormatHook(tc.hook)
			out, err := registry.Run(context.Background(), "write", `{"path":"f.txt","content":"a\nb\n"}`)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if !strings.Contains(out, "(+2/-0 行)") {
				t.Fatalf("output = %q, want plain +2/-0 summary", out)
			}
			if strings.Contains(out, formattedMarker) {
				t.Fatalf("output = %q, want no formatted marker", out)
			}
		})
	}
}

func TestSetFormatHookNilRestoresDefault(t *testing.T) {
	root := t.TempDir()
	registry := New(root, true)
	registry.SetFormatHook(func(_ string, content []byte) []byte { return append(content, []byte("x\n")...) })
	registry.SetFormatHook(nil)

	out, err := registry.Run(context.Background(), "write", `{"path":"f.txt","content":"hello\n"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if want := "wrote 6 bytes to f.txt"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}
