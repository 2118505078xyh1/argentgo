package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

func contextPackWrite(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func contextPackRun(t *testing.T, root, args string) string {
	t.Helper()
	out, err := New(root, true).Run(context.Background(), "context_pack", args)
	if err != nil {
		t.Fatalf("context_pack(%s): %v", args, err)
	}
	return out
}

func contextPackRunErr(t *testing.T, root, args string) error {
	t.Helper()
	_, err := New(root, true).Run(context.Background(), "context_pack", args)
	if err == nil {
		t.Fatalf("context_pack(%s) succeeded, want error", args)
	}
	return err
}

func TestContextPackSingleFile(t *testing.T) {
	root := t.TempDir()
	contextPackWrite(t, root, "anchor.go", "package main\nfunc main() {}\n")

	out := contextPackRun(t, root, `{"target":"anchor.go","files":[]}`)
	want := "## anchor.go ##\npackage main\nfunc main() {}\n"
	if out != want {
		t.Fatalf("pack = %q, want %q", out, want)
	}
}

func TestContextPackMultiFileTargetLast(t *testing.T) {
	root := t.TempDir()
	contextPackWrite(t, root, "a.go", "A")
	contextPackWrite(t, root, "b.go", "B")
	contextPackWrite(t, root, "anchor.go", "Z")

	// Explicit files are reordered deterministically; the anchor stays last.
	out := contextPackRun(t, root, `{"target":"anchor.go","files":["b.go","a.go"]}`)
	want := "## a.go ##\nA\n## b.go ##\nB\n## anchor.go ##\nZ"
	if out != want {
		t.Fatalf("pack = %q, want %q", out, want)
	}
	if strings.LastIndex(out, "## ") != strings.LastIndex(out, "## anchor.go ##") {
		t.Fatalf("target is not the last block: %q", out)
	}
}

func TestContextPackTargetGapWindow(t *testing.T) {
	root := t.TempDir()
	var src strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&src, "L%d\n", i)
	}
	contextPackWrite(t, root, "anchor.go", src.String())

	out := contextPackRun(t, root, `{"target":"anchor.go","files":[],"line":5,"before":2,"after":2}`)
	want := "## anchor.go ##\nL3\nL4\nL5\nL6\nL7"
	if out != want {
		t.Fatalf("window pack = %q, want %q", out, want)
	}
	if strings.Contains(out, "L2\n") || strings.Contains(out, "L8\n") {
		t.Fatalf("window leaked lines outside [3,7]: %q", out)
	}

	// Per-side caps apply independently: 9999 clamps to 200 on each side.
	var big strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&big, "L%03d\n", i)
	}
	contextPackWrite(t, root, "big.go", big.String())
	out = contextPackRun(t, root, `{"target":"big.go","files":[],"line":250,"before":9999,"after":9999}`)
	if !strings.Contains(out, "L050\n") || !strings.Contains(out, "\nL450") {
		t.Fatalf("clamped window missing its 200-line sides: %q", out)
	}
	if strings.Contains(out, "L049\n") || strings.Contains(out, "L451\n") {
		t.Fatalf("window exceeded the 200-line cap: %q", out)
	}

	// A line past EOF yields an empty body but still names the anchor. Use a
	// fresh root so sibling files do not appear ahead of the target.
	smallRoot := t.TempDir()
	contextPackWrite(t, smallRoot, "short.go", "one\ntwo\nthree\n")
	out = contextPackRun(t, smallRoot, `{"target":"short.go","files":[],"line":99,"before":1,"after":1}`)
	if out != "## short.go ##\n" {
		t.Fatalf("out-of-range window = %q, want empty body", out)
	}
}

func TestContextPackByteBudgetTruncation(t *testing.T) {
	root := t.TempDir()
	contextPackWrite(t, root, "a.txt", "hello")
	cjk := strings.Repeat("漢字", 8)
	contextPackWrite(t, root, "z.txt", cjk) // target, does not fit

	out := contextPackRun(t, root, `{"target":"z.txt","files":["a.txt"],"budget_bytes":50}`)
	if len(out) > 50 {
		t.Fatalf("output = %d bytes, budget = 50: %q", len(out), out)
	}
	if !strings.Contains(out, "[truncated at 17 bytes]") {
		t.Fatalf("missing/incorrect truncation marker: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("output split a UTF-8 rune: %q", out)
	}
	if strings.Contains(out, "漢") || strings.Count(out, "## ") != 1 {
		t.Fatalf("a non-fitting block was partially emitted: %q", out)
	}

	// With room for the whole CJK block the content is verbatim, no marker.
	exact := len("## a.txt ##\nhello") + 1 + len("## z.txt ##\n"+cjk)
	out = contextPackRun(t, root, fmt.Sprintf(`{"target":"z.txt","files":["a.txt"],"budget_bytes":%d}`, exact))
	if !strings.Contains(out, cjk) || strings.Contains(out, "[truncated") {
		t.Fatalf("exact-fit pack = %q", out)
	}
}

func TestContextPackExplicitEmptyFilesSuppressesAutoSelection(t *testing.T) {
	root := t.TempDir()
	contextPackWrite(t, root, "anchor.txt", "ANCHOR")
	contextPackWrite(t, root, "sibling.txt", "SIBLING")

	// An explicitly supplied empty list is an override meaning "no other
	// files": it must not fall back to auto-selecting the target's siblings.
	out := contextPackRun(t, root, `{"target":"anchor.txt","files":[]}`)
	if out != "## anchor.txt ##\nANCHOR" {
		t.Fatalf("empty files override = %q, want only the anchor block", out)
	}

	// Omitting the field still selects siblings automatically.
	out = contextPackRun(t, root, `{"target":"anchor.txt"}`)
	if !strings.Contains(out, "## sibling.txt ##") {
		t.Fatalf("omitted files should auto-select the sibling: %q", out)
	}
}

func TestContextPackPathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	contextPackWrite(t, root, "ok.txt", "ok")

	// Use a platform-appropriate absolute path outside the workspace: on
	// Windows drive-qualified paths are absolute, on POSIX a leading slash is.
	// The literal "C:/Windows/win.ini" is NOT absolute on Linux, so it must not
	// be used as a cross-platform escape fixture.
	absOutside := "/etc/passwd"
	if runtime.GOOS == "windows" {
		absOutside = `C:\Windows\win.ini`
	}
	absArgs, err := json.Marshal(map[string]any{"target": "ok.txt", "files": []string{absOutside}})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"target":"../outside.txt"}`,
		`{"target":"ok.txt","files":["../secret.txt"]}`,
		string(absArgs),
	} {
		if err := contextPackRunErr(t, root, args); err == nil {
			t.Fatalf("expected path escape error for %s", args)
		}
	}
}

func TestContextPackCJKContent(t *testing.T) {
	root := t.TempDir()
	content := "package main\n// 注释：你好世界\nfunc 函数() string { return \"你好\" }\n"
	contextPackWrite(t, root, "anchor.go", content)

	out := contextPackRun(t, root, `{"target":"anchor.go","files":[]}`)
	if out != "## anchor.go ##\n"+content || !utf8.ValidString(out) {
		t.Fatalf("CJK pack = %q", out)
	}

	out = contextPackRun(t, root, `{"target":"anchor.go","files":[],"line":2,"before":0,"after":0}`)
	if !strings.Contains(out, "注释：你好世界") || strings.Contains(out, "package main") {
		t.Fatalf("CJK window = %q", out)
	}
}

func TestContextPackEmptyAndMissingTarget(t *testing.T) {
	root := t.TempDir()
	for _, args := range []string{
		`{}`,
		`{"target":""}`,
		`{"target":"missing.go"}`,
		`{"target":"."}`, // a directory is not a file
		`not json`,
	} {
		contextPackRunErr(t, root, args)
	}

	contextPackWrite(t, root, "empty.go", "")
	out := contextPackRun(t, root, `{"target":"empty.go","files":[]}`)
	if out != "## empty.go ##\n" {
		t.Fatalf("empty file pack = %q", out)
	}
}

func TestContextPackSchemaMutatingAndRegistration(t *testing.T) {
	tool := contextPackTool{}
	if tool.Mutating() {
		t.Fatal("context_pack must be read-only")
	}
	schema := tool.Schema()
	if !json.Valid([]byte(schema)) {
		t.Fatalf("context_pack schema is not valid JSON: %s", schema)
	}
	var doc struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Required) != 1 || doc.Required[0] != "target" {
		t.Fatalf("required = %v, want [target]", doc.Required)
	}
	for _, key := range []string{"target", "files", "budget_bytes", "line", "before", "after"} {
		if _, ok := doc.Properties[key]; !ok {
			t.Fatalf("schema missing %q: %s", key, schema)
		}
	}

	r := New(t.TempDir(), true)
	names := r.Names()
	pos := -1
	for i, name := range names {
		if name == "context_pack" {
			pos = i
			break
		}
	}
	if pos < 0 {
		t.Fatalf("context_pack not registered: %v", names)
	}
	for i := pos + 1; i < len(names) && names[i] != "tool_search"; i++ {
		if !strings.HasPrefix(names[i], "memory_") {
			t.Fatalf("context_pack must precede the memory tail: %v", names)
		}
	}
	advertised := false
	for _, spec := range r.SpecsFor(nil, security.SandboxReadOnly) {
		if spec.Function.Name == "context_pack" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("read-only sandbox must still advertise context_pack")
	}
}

func TestRegisterContextPackInsertsBeforeMemoryAndIsIdempotent(t *testing.T) {
	r := &Registry{
		tools: map[string]Tool{},
		order: []string{"read", "memory_retain", "tool_search"},
	}
	registerContextPack(r)
	want := []string{"read", "context_pack", "memory_retain", "tool_search"}
	if strings.Join(r.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", r.order, want)
	}
	registerContextPack(r)
	if len(r.order) != len(want) {
		t.Fatalf("second registration changed the order: %v", r.order)
	}
}

func TestContextPackBudgetCaps(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, contextPackDefaultBudget},
		{-3, contextPackDefaultBudget},
		{1, contextPackMinBudget},
		{64, 64},
		{8000, 8000},
		{31999, 31999},
		{32000, contextPackMaxBudget},
		{32001, contextPackMaxBudget},
		{1 << 20, contextPackMaxBudget},
	} {
		if got := contextPackBudget(tc.in); got != tc.want {
			t.Errorf("contextPackBudget(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}

	// End-to-end: a big target with the default budget stays bounded.
	root := t.TempDir()
	contextPackWrite(t, root, "big.txt", strings.Repeat("a", 20000))
	out := contextPackRun(t, root, `{"target":"big.txt","files":[],"budget_bytes":0}`)
	if len(out) > contextPackDefaultBudget || !strings.Contains(out, "[truncated at") {
		t.Fatalf("default-budget pack = %d bytes: %q", len(out), out)
	}
}
