package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeAnchorSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestAnchorSourceLinesMarksOffendingLineWithGutter(t *testing.T) {
	dir := t.TempDir()
	writeAnchorSource(t, dir, "f.go", "package p\n\nfunc f() {\n\tprintln(\"a\")\n\tprintln(\"b\")\n\tprintln(\"c\")\n\tboom()\n}\n")

	got := AnchorSourceLines("f.go:7: undefined: boom", dir, 2)
	if !strings.Contains(got, "f.go:7:\n") {
		t.Fatalf("missing anchored header: %q", got)
	}
	if !strings.Contains(got, "█7| \tboom()") {
		t.Fatalf("offending line is not marked: %q", got)
	}
	if !strings.Contains(got, "5| \tprintln(\"b\")") || !strings.Contains(got, "6| \tprintln(\"c\")") {
		t.Fatalf("context lines/gutter are wrong: %q", got)
	}
	if n := strings.Count(got, "█"); n != 1 {
		t.Fatalf("got %d markers, want 1: %q", n, got)
	}
}

func TestAnchorSourceLinesClampsPadAtFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	writeAnchorSource(t, dir, "edge.go", "one\ntwo\nthree\n")

	first := AnchorSourceLines("edge.go:1", dir, 3)
	if strings.Contains(first, "0|") {
		t.Fatalf("window escaped above line 1: %q", first)
	}
	if !strings.Contains(first, "█1| one") || !strings.Contains(first, "3| three") {
		t.Fatalf("start-clamped window is wrong: %q", first)
	}

	last := AnchorSourceLines("edge.go:3", dir, 3)
	if !strings.Contains(last, "█3| three") || !strings.Contains(last, "1| one") {
		t.Fatalf("end-clamped window is wrong: %q", last)
	}
	if strings.Count(last, "\n") > 4 {
		t.Fatalf("window was not clamped at EOF: %q", last)
	}
}

func TestAnchorSourceRegexParsesPathForms(t *testing.T) {
	cases := []struct {
		in, wantPath, wantLine string
	}{
		{`C:\work\pkg\f.go:12`, `C:\work\pkg\f.go`, "12"},
		{`/work/pkg/f.go:12`, `/work/pkg/f.go`, "12"},
		{`pkg/f.go:12`, `pkg/f.go`, "12"},
		{`f.go:12`, `f.go`, "12"},
		{`pkg\f.go:12:5`, `pkg\f.go`, "12"},
	}
	re := regexp.MustCompile(sourceAnchorPattern)
	for _, tc := range cases {
		m := re.FindStringSubmatch(tc.in)
		if m == nil {
			t.Fatalf("pattern did not match %q", tc.in)
		}
		if got := m[1] + m[2]; got != tc.wantPath {
			t.Errorf("path from %q = %q, want %q", tc.in, got, tc.wantPath)
		}
		if m[3] != tc.wantLine {
			t.Errorf("line from %q = %q, want %q", tc.in, m[3], tc.wantLine)
		}
	}
}

func TestAnchorSourceLinesAcceptsAbsolutePathInsideRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeAnchorSource(t, dir, "abs.go", strings.Repeat("x\n", 9)+"bad()\n")

	got := AnchorSourceLines("error at "+path+":10", dir, 1)
	if !strings.Contains(got, "█10| bad()") {
		t.Fatalf("absolute in-root path was not anchored: %q", got)
	}
}

func TestAnchorSourceLinesDedupsAndCapsAnchors(t *testing.T) {
	dir := t.TempDir()
	writeAnchorSource(t, dir, "m.go", strings.Repeat("line\n", 20))

	dup := AnchorSourceLines("m.go:3: a\nm.go:3: again\n", dir, 0)
	if n := strings.Count(dup, "m.go:"); n != 1 {
		t.Fatalf("dedup produced %d anchors, want 1: %q", n, dup)
	}

	var out strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&out, "m.go:%d: err\n", i)
	}
	capped := AnchorSourceLines(out.String(), dir, 0)
	if n := strings.Count(capped, "m.go:"); n != maxSourceAnchors {
		t.Fatalf("got %d anchors, want cap %d: %q", n, maxSourceAnchors, capped)
	}
}

func TestAnchorSourceLinesRejectsPathEscapes(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeAnchorSource(t, outside, "secret.go", "s3cr3t value\n")
	writeAnchorSource(t, dir, "ok.go", "safe()\n")

	if got := AnchorSourceLines("../secret.go:1", dir, 0); got != "" {
		t.Fatalf("relative escape was anchored: %q", got)
	}
	if got := AnchorSourceLines(filepath.Join(outside, "secret.go")+":1", dir, 0); got != "" {
		t.Fatalf("absolute outside-root path was anchored: %q", got)
	}
	if got := AnchorSourceLines("../secret.go:1 ok.go:1", dir, 0); !strings.Contains(got, "ok.go:1:") || strings.Contains(got, "s3cr3t") {
		t.Fatalf("mixed output kept unsafe anchor or lost safe one: %q", got)
	}
}

func TestAnchorSourceLinesRedactsSecretInSourceLine(t *testing.T) {
	dir := t.TempDir()
	const token = "sk_live_abcdefghijklmnop"
	writeAnchorSource(t, dir, "secret.go", "package p\nvar key = \""+token+"\"\n")

	got := AnchorSourceLines("secret.go:2", dir, 0)
	if strings.Contains(got, token) {
		t.Fatalf("source-line secret leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("secret was not redacted: %q", got)
	}
}

func TestAnchorSourceLinesNoAnchorsReturnsEmpty(t *testing.T) {
	if got := AnchorSourceLines("everything is fine", t.TempDir(), 2); got != "" {
		t.Fatalf("no-anchor output = %q, want empty", got)
	}
}

func TestAnchorSourceLinesSkipsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.go")
	if err := os.WriteFile(path, []byte("package p\n\x00bad()\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := AnchorSourceLines("bin.go:2", dir, 0); got != "" {
		t.Fatalf("binary file was anchored: %q", got)
	}
}

func TestAnchorSourceLinesClipsLongLinesRuneSafe(t *testing.T) {
	dir := t.TempDir()
	writeAnchorSource(t, dir, "long.go", strings.Repeat("界", 400)+"\n")

	got := AnchorSourceLines("long.go:1", dir, 0)
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, sourceTruncMark) {
		t.Fatalf("long line was not truncated: %q", got)
	}
	_, after, found := strings.Cut(got, "| ")
	if !found {
		t.Fatalf("no gutter in output: %q", got)
	}
	content := strings.TrimSuffix(after, "\n")
	if len(content) > maxSourceLineBytes+len(sourceTruncMark) {
		t.Fatalf("content is %d bytes, want <= %d", len(content), maxSourceLineBytes+len(sourceTruncMark))
	}
}
