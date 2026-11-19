package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/tools"
)

func mustRun(t *testing.T, registry *tools.Registry, name, args string) string {
	t.Helper()
	out, err := registry.Run(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNamesAndSpecsAreStable(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	wantPrefix := []string{"read", "write", "edit", "bash", "ls", "grep", "find"}
	names := registry.Names()
	if !reflect.DeepEqual(names[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("names = %v, want legacy prefix %v", names, wantPrefix)
	}
	for _, extra := range []string{"glob", "todo", "multi_edit", "web_fetch", "git_status"} {
		if !contains(names, extra) {
			t.Fatalf("names = %v, want %q", names, extra)
		}
	}
	names[0] = "mutated"
	if registry.Names()[0] != "read" {
		t.Fatal("Names() leaks the internal order slice")
	}
	specs := registry.Specs()
	if len(specs) != len(registry.Names()) {
		t.Fatalf("specs = %d, names = %d", len(specs), len(registry.Names()))
	}
	for i, spec := range specs {
		if spec.Function.Name != registry.Names()[i] {
			t.Fatalf("spec %d = %q, want %q", i, spec.Function.Name, registry.Names()[i])
		}
		if !json.Valid([]byte(spec.Function.Parameters)) {
			t.Fatalf("%s has an invalid schema", spec.Function.Name)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestOutputLimitKeepsHeadAndTail(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "big.txt", strings.Repeat("a", 120000))
	registry := tools.NewWithLimit(root, true, 1000)
	out := mustRun(t, registry, "read", `{"path":"big.txt"}`)
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("output was not truncated")
	}
	if len(out) < 1000 || len(out) > 1100 {
		t.Fatalf("output length = %d, want about 1000", len(out))
	}
	if !strings.HasPrefix(out, "aaa") || !strings.HasSuffix(out, "aaa") {
		t.Fatal("truncated output should keep both head and tail")
	}
}

func TestGlobDoubleStar(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "src/a/b.go", "package a\n")
	writeFile(t, root, "README.md", "# hi\n")
	writeFile(t, root, "node_modules/pkg/ignored.go", "package pkg\n")
	registry := tools.New(root, true)

	out := mustRun(t, registry, "glob", `{"pattern":"**/*.go"}`)
	if !strings.Contains(out, "src/main.go") || !strings.Contains(out, "src/a/b.go") {
		t.Fatalf("glob **/*.go = %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("glob should skip node_modules: %q", out)
	}
	lines := strings.Split(out, "\n")
	if !reflect.DeepEqual(lines, []string{"src/a/b.go", "src/main.go"}) {
		t.Fatalf("glob output not sorted: %q", out)
	}

	scoped := mustRun(t, registry, "glob", `{"pattern":"src/**/*.go"}`)
	if !strings.Contains(scoped, "src/a/b.go") || !strings.Contains(scoped, "src/main.go") {
		t.Fatalf("glob src/**/*.go = %q", scoped)
	}

	basename := mustRun(t, registry, "glob", `{"pattern":"*.md"}`)
	if basename != "README.md" {
		t.Fatalf("glob *.md = %q, want README.md", basename)
	}

	if _, err := registry.Run(context.Background(), "glob", `{"pattern":""}`); err == nil {
		t.Fatal("glob accepted an empty pattern")
	}
}

func TestTodoWritesAndReadsState(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	registry := tools.New(root, false)
	registry.SetSessionDir(stateDir)

	out := mustRun(t, registry, "todo", `{"todos":[{"id":"1","text":"first","status":"completed"},{"text":"second","status":"in_progress"},{"text":"third"}]}`)
	for _, want := range []string{"[x] 1 first", "[>] 2 second", "[ ] 3 third"} {
		if !strings.Contains(out, want) {
			t.Fatalf("todo write = %q, want %q", out, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "todos.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var doc struct {
		Todos []struct {
			ID     string `json:"id"`
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("state file is not JSON: %v", err)
	}
	if len(doc.Todos) != 3 || doc.Todos[2].Text != "third" {
		t.Fatalf("state file = %s", data)
	}

	read := mustRun(t, registry, "todo", `{}`)
	if !strings.Contains(read, "[x] 1 first") || !strings.Contains(read, "[ ] 3 third") {
		t.Fatalf("todo read = %q", read)
	}

	if _, err := registry.Run(context.Background(), "todo", `{"todos":[{"text":"x","status":"nope"}]}`); err == nil || !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("err = %v, want an invalid status error", err)
	}
}

func TestTodoEmptyList(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	registry.SetSessionDir(t.TempDir())
	if out := mustRun(t, registry, "todo", `{"op":"read"}`); out != "task list is empty" {
		t.Fatalf("todo = %q", out)
	}
}

func TestMultiEditAppliesInOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "alpha beta gamma\n")
	registry := tools.New(root, true)

	out := mustRun(t, registry, "multi_edit", `{"path":"a.txt","edits":[{"old":"alpha","new":"one"},{"old":"one beta","new":"two"}]}`)
	if !strings.Contains(out, "2 edits") {
		t.Fatalf("multi_edit = %q", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "two gamma\n" {
		t.Fatalf("content = %q, want %q", data, "two gamma\n")
	}
}

func TestMultiEditIsAtomic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "alpha beta")
	registry := tools.New(root, true)

	_, err := registry.Run(context.Background(), "multi_edit", `{"path":"a.txt","edits":[{"old":"alpha","new":"one"},{"old":"missing","new":"two"}]}`)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "alpha beta" {
		t.Fatalf("file changed after a failed multi_edit: %q", data)
	}

	_, err = registry.Run(context.Background(), "multi_edit", `{"path":"a.txt","edits":[]}`)
	if err == nil {
		t.Fatal("multi_edit accepted an empty edit list")
	}
}

func TestWebFetchRejectsSchemesAndLocalAddresses(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	if _, err := registry.Run(context.Background(), "web_fetch", `{"url":"file:///etc/passwd"}`); err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("err = %v, want a scheme rejection", err)
	}
	_, err := registry.Run(context.Background(), "web_fetch", `{"url":"http://127.0.0.1:9/"}`)
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("err = %v, want a local-address rejection", err)
	}
}

func TestWebFetchStripsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><style>p{color:red}</style></head><body><h1>Hello</h1><p>World &amp; friends</p><script>bad()</script></body></html>`))
	}))
	defer server.Close()

	registry := tools.New(t.TempDir(), true)
	out := mustRun(t, registry, "web_fetch", `{"url":"`+server.URL+`","allowLocal":true}`)
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World & friends") {
		t.Fatalf("web_fetch = %q", out)
	}
	if strings.Contains(out, "bad()") || strings.Contains(out, "color:red") || strings.Contains(out, "<h1>") {
		t.Fatalf("web_fetch kept markup: %q", out)
	}
	if _, err := registry.Run(context.Background(), "web_fetch", `{"url":"`+server.URL+`/missing","allowLocal":true}`); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404 error", err)
	}
}

type denyReadGuard struct {
	seen []string
}

func (g *denyReadGuard) Check(_ context.Context, name string, _ json.RawMessage) error {
	g.seen = append(g.seen, name)
	if name == "read" {
		return errors.New("blocked by policy")
	}
	return nil
}

func TestGuardRejectsBeforeToolRuns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.txt", "secret")
	registry := tools.New(root, true)
	guard := &denyReadGuard{}
	registry.SetGuard(guard)

	_, err := registry.Run(context.Background(), "read", `{"path":"a.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "blocked by policy") || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want a guard rejection", err)
	}
	if !reflect.DeepEqual(guard.seen, []string{"read"}) {
		t.Fatalf("guard saw %v", guard.seen)
	}
	if _, err := registry.Run(context.Background(), "ls", `{}`); err != nil {
		t.Fatalf("ls should pass the guard: %v", err)
	}
}

func TestGuardRunsBeforeYoloCheck(t *testing.T) {
	registry := tools.New(t.TempDir(), false)
	registry.SetGuard(&denyReadGuard{})

	// "write" is mutating, but the guard must be consulted (and win) first.
	_, err := registry.Run(context.Background(), "write", `{"path":"a.txt","content":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("err = %v, want the --yolo hint (guard allows write)", err)
	}

	registry.SetGuard(rejectAllGuard{})
	_, err = registry.Run(context.Background(), "write", `{"path":"a.txt","content":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "blocked") || strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("err = %v, want the guard rejection before the yolo check", err)
	}
}

type rejectAllGuard struct{}

func (rejectAllGuard) Check(context.Context, string, json.RawMessage) error {
	return errors.New("blocked")
}

type panicGuard struct{}

func (panicGuard) Check(context.Context, string, json.RawMessage) error {
	panic("boom")
}

func TestGuardPanicBecomesToolError(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	registry.SetGuard(panicGuard{})
	_, err := registry.Run(context.Background(), "ls", `{}`)
	if err == nil || !strings.Contains(err.Error(), "guard panicked") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}
	registry.SetGuard(nil)
	if _, err := registry.Run(context.Background(), "ls", `{}`); err != nil {
		t.Fatalf("ls after clearing the guard: %v", err)
	}
}

type captureGuard struct {
	args string
}

func (g *captureGuard) Check(_ context.Context, _ string, args json.RawMessage) error {
	g.args = string(args)
	return nil
}

func TestGuardSeesNormalizedArgs(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	guard := &captureGuard{}
	registry.SetGuard(guard)
	if _, err := registry.Run(context.Background(), "ls", "  "); err != nil {
		t.Fatalf("ls: %v", err)
	}
	if guard.args != "{}" {
		t.Fatalf("guard args = %q, want {}", guard.args)
	}
}
