package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const pagingNoticeFmt = "[showing lines %d–%d of %d; re-call with offset/limit to page]"

func pagingRoot(t *testing.T, name, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func runPagingRead(t *testing.T, root, args string) string {
	t.Helper()
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(args))
	if err != nil {
		t.Fatalf("read %s: %v", args, err)
	}
	return out
}

func TestReadFullFileHasNoPagingNotice(t *testing.T) {
	// A trailing newline must not create a phantom line or change the bytes.
	content := "one\ntwo\nthree\n"
	root := pagingRoot(t, "f.txt", content)
	out := runPagingRead(t, root, `{"path":"f.txt"}`)
	if out != content {
		t.Fatalf("full read = %q, want byte-identical %q", out, content)
	}
	if strings.Contains(out, "showing lines") {
		t.Fatalf("full read should not carry a paging notice: %q", out)
	}
}

func TestReadBeyondLimitAppendsPagingNotice(t *testing.T) {
	root := pagingRoot(t, "f.txt", "l1\nl2\nl3\nl4\nl5\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":1,"limit":2}`)
	want := "l1\nl2\n" + fmt.Sprintf(pagingNoticeFmt, 1, 2, 5)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
}

func TestReadMiddleSliceReportsCorrectRange(t *testing.T) {
	root := pagingRoot(t, "f.txt", "a\nb\nc\nd\ne\nf\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":3,"limit":2}`)
	want := "c\nd\n" + fmt.Sprintf(pagingNoticeFmt, 3, 4, 6)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
}

func TestReadExactEndHasNoPagingNotice(t *testing.T) {
	// Reaching the final real line (no trailing newline) must stay quiet.
	root := pagingRoot(t, "f.txt", "a\nb\nc\nd\ne")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":4,"limit":2}`)
	if out != "d\ne" {
		t.Fatalf("read = %q, want %q", out, "d\ne")
	}
	if strings.Contains(out, "showing lines") {
		t.Fatalf("exact-end read should not carry a paging notice: %q", out)
	}
}

func TestReadDescriptionHasPagingHintUnderCap(t *testing.T) {
	desc := (readTool{}).Description()
	for _, hint := range []string{"prefer", "shell", "offset", "limit"} {
		if !strings.Contains(desc, hint) {
			t.Fatalf("description %q missing hint %q", desc, hint)
		}
	}
	if len(desc) >= 1024 {
		t.Fatalf("description is %d bytes, want < 1024", len(desc))
	}
	if !utf8.ValidString(desc) {
		t.Fatal("description is not valid UTF-8")
	}
}

func TestReadPagingKeepsUTF8Intact(t *testing.T) {
	root := pagingRoot(t, "f.txt", "你好\n世界\n再见\n朋友\n")
	out := runPagingRead(t, root, `{"path":"f.txt","offset":1,"limit":2}`)
	want := "你好\n世界\n" + fmt.Sprintf(pagingNoticeFmt, 1, 2, 4)
	if out != want {
		t.Fatalf("read = %q, want %q", out, want)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("read contains broken runes: %q", out)
	}
}
