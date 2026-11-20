package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// outlineGoSrc is used by several tests. Line numbers of the declarations are
// deliberately non-trivial so the outline's line reporting is exercised.
const outlineGoSrc = `package alpha

// Client is a thing. 中文注释
type Client struct{}

const Limit = 10

func NewClient() *Client { return nil }

func (c *Client) Do() error { return nil }
`

func runOutlineRead(t *testing.T, root, args string) string {
	t.Helper()
	out, err := (readTool{}).Run(context.Background(), root, json.RawMessage(args))
	if err != nil {
		t.Fatalf("read %s: %v", args, err)
	}
	return out
}

func TestReadOutlineGoFileListsSymbols(t *testing.T) {
	root := pagingRoot(t, "alpha.go", outlineGoSrc)
	out := runOutlineRead(t, root, `{"path":"alpha.go","outline":true}`)
	want := strings.Join([]string{
		"outline: alpha.go (4 symbols)",
		"  type    Client:4",
		"  const   Limit:6",
		"  func    NewClient:8",
		"  method  Client.Do:10",
	}, "\n")
	if out != want {
		t.Fatalf("outline =\n%s\nwant\n%s", out, want)
	}
	if strings.Contains(out, "return nil") {
		t.Fatalf("outline leaked function bodies: %q", out)
	}
}

func TestReadOutlineDefaultsOff(t *testing.T) {
	root := pagingRoot(t, "alpha.go", outlineGoSrc)
	out := runOutlineRead(t, root, `{"path":"alpha.go"}`)
	if out != outlineGoSrc {
		t.Fatalf("default read changed: got %q, want %q", out, outlineGoSrc)
	}
	if strings.Contains(out, "outline:") {
		t.Fatalf("default read must not emit an outline: %q", out)
	}
}

func TestReadOutlineNonGoFallsBackToFullRead(t *testing.T) {
	root := pagingRoot(t, "note.txt", "你好\nworld\n")
	out := runOutlineRead(t, root, `{"path":"note.txt","outline":true}`)
	if out != "你好\nworld\n" {
		t.Fatalf("non-Go outline = %q, want a normal full read", out)
	}
}

func TestReadOutlineCJKIdentifiers(t *testing.T) {
	root := pagingRoot(t, "uni.go", "package uni\n\nfunc 你好() {}\n\ntype 世界 struct{}\n")
	out := runOutlineRead(t, root, `{"path":"uni.go","outline":true}`)
	if !strings.Contains(out, "func    你好:3") || !strings.Contains(out, "type    世界:5") {
		t.Fatalf("CJK identifiers lost in outline: %q", out)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("outline has broken runes: %q", out)
	}
}

func TestReadOutlineLargeGoFileStaysCompact(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "func Fn%d() int { // 中文注释占位\n", i)
		b.WriteString("  x := 0\n  y := x + 1\n  z := y + 2\n  _ = z\n  return z\n}\n\n")
	}
	src := b.String()
	root := pagingRoot(t, "big.go", src)
	out := runOutlineRead(t, root, `{"path":"big.go","outline":true}`)

	if got := strings.Count(out, "\n") + 1; got != 401 { // 1 header + 400 symbols
		t.Fatalf("outline has %d lines, want 401", got)
	}
	if len(out) >= len(src)/3 {
		t.Fatalf("outline not compact: %d bytes vs source %d bytes", len(out), len(src))
	}
	if !strings.Contains(out, "Fn399:") {
		t.Fatalf("outline missing last symbol: %q", out)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatal("outline has broken runes")
	}
}

func TestReadSchemaDocumentsOutline(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte((readTool{}).Schema()), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties["outline"]
	if !ok || p.Type != "boolean" {
		t.Fatalf("schema outline property = %+v, ok=%v, want type boolean", p, ok)
	}
}
