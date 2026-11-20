package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// withStartFn 在测试期间替换进程派生器。本文件中的测试刻意不并行：
// startFn 是包级变量。
func withStartFn(t *testing.T, fn func(context.Context, lsp.Server, string) (*lsp.Client, error)) {
	t.Helper()
	previous := startFn
	startFn = fn
	t.Cleanup(func() { startFn = previous })
}

func testPosition(line, character int) lsp.Position {
	return lsp.Position{Line: line, Character: character}
}

func testRange(sl, sc, el, ec int) lsp.Range {
	return lsp.Range{Start: testPosition(sl, sc), End: testPosition(el, ec)}
}

func TestMapPositionRangeAndSymbol(t *testing.T) {
	source := lsp.Symbol{
		Name:           "Compute",
		Kind:           12, // 数字形式的 LSP 函数种类
		Range:          testRange(3, 1, 9, 2),
		SelectionRange: testRange(3, 9, 3, 17),
		URI:            "file:///workspace/compute.go",
		Detail:         "func Compute(x int) int",
		Depth:          2,
	}
	got := mapSymbol(source)
	want := tools.LspSymbol{
		Name:           "Compute",
		Kind:           12,
		Range:          tools.LspRange{Start: tools.LspPos{Line: 3, Character: 1}, End: tools.LspPos{Line: 9, Character: 2}},
		SelectionRange: tools.LspRange{Start: tools.LspPos{Line: 3, Character: 9}, End: tools.LspPos{Line: 3, Character: 17}},
		URI:            "file:///workspace/compute.go",
		Detail:         "func Compute(x int) int",
		Depth:          2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapSymbol() = %+v, want %+v", got, want)
	}

	// 范围和位置转换在上面已间接覆盖，但这里直接检查它们，
	// 使回归时能指明出错的辅助函数。
	if gotRange := mapRange(testRange(0, 4, 7, 11)); gotRange != (tools.LspRange{
		Start: tools.LspPos{Line: 0, Character: 4},
		End:   tools.LspPos{Line: 7, Character: 11},
	}) {
		t.Fatalf("mapRange() = %+v", gotRange)
	}
	if gotPos := mapPosition(testPosition(6, 2)); gotPos != (tools.LspPos{Line: 6, Character: 2}) {
		t.Fatalf("mapPosition() = %+v", gotPos)
	}

	// LspSymbol.Depth 不出现在线上格式中，但必须在进程内映射后保留。
	if got.Depth != source.Depth {
		t.Fatalf("mapSymbol depth = %d, want %d", got.Depth, source.Depth)
	}
}

func TestMapWorkspaceEditPreservesChangesAndDocumentChanges(t *testing.T) {
	if got := mapWorkspaceEdit(nil); got != nil {
		t.Fatalf("mapWorkspaceEdit(nil) = %+v, want nil", got)
	}

	source := &lsp.WorkspaceEdit{
		Changes: map[string][]lsp.TextEdit{
			"file:///a.go": {{Range: testRange(1, 0, 1, 3), NewText: "renamed"}},
		},
		DocumentChanges: []lsp.DocumentEdit{
			{
				URI: "file:///b.go",
				Edits: []lsp.TextEdit{
					{Range: testRange(2, 4, 2, 9), NewText: "second"},
					{Range: testRange(5, 0, 5, 1), NewText: "third"},
				},
			},
		},
	}
	got := mapWorkspaceEdit(source)
	want := &tools.LspWorkspaceEdit{
		Changes: map[string][]tools.LspTextEdit{
			"file:///a.go": {{Range: tools.LspRange{Start: tools.LspPos{Line: 1}, End: tools.LspPos{Line: 1, Character: 3}}, NewText: "renamed"}},
		},
		DocumentChanges: []tools.LspDocumentEdit{{
			URI: "file:///b.go",
			Edits: []tools.LspTextEdit{
				{Range: tools.LspRange{Start: tools.LspPos{Line: 2, Character: 4}, End: tools.LspPos{Line: 2, Character: 9}}, NewText: "second"},
				{Range: tools.LspRange{Start: tools.LspPos{Line: 5}, End: tools.LspPos{Line: 5, Character: 1}}, NewText: "third"},
			},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapWorkspaceEdit() = %+v, want %+v", got, want)
	}

	// 相同 URI 在两种形式中都保留：lsp 客户端已经把 documentChanges
	// 合并进 Changes，DTO 不得丢失这一点。
	merged := &lsp.WorkspaceEdit{
		Changes: map[string][]lsp.TextEdit{"file:///a.go": {{NewText: "legacy"}}},
		DocumentChanges: []lsp.DocumentEdit{{
			URI:   "file:///a.go",
			Edits: []lsp.TextEdit{{NewText: "structured"}},
		}},
	}
	out := mapWorkspaceEdit(merged)
	if len(out.Changes["file:///a.go"]) != 1 || len(out.DocumentChanges) != 1 {
		t.Fatalf("merged edit views = changes:%v docChanges:%v", out.Changes, out.DocumentChanges)
	}
}

func TestMapCodeActionCarriesDiagnosticsEditCommandAndDisabled(t *testing.T) {
	source := lsp.CodeAction{
		Title:       "Fix typo",
		Kind:        "quickfix",
		Diagnostics: []lsp.Diagnostic{{Range: testRange(0, 2, 0, 5), Severity: "warning", Message: "misspelled", Source: "gopls"}},
		Edit: &lsp.WorkspaceEdit{Changes: map[string][]lsp.TextEdit{
			"file:///a.go": {{Range: testRange(0, 2, 0, 5), NewText: "correct"}},
		}},
		Command: &lsp.Command{
			Title:     "Apply fix",
			Command:   "gopls.apply",
			Arguments: []any{"file:///a.go", 3, true},
		},
		IsPreferred: true,
		Disabled:    &lsp.CodeActionDisabled{Reason: "needs permission"},
	}
	got := mapCodeAction(source)
	want := tools.LspCodeAction{
		Title:       "Fix typo",
		Kind:        "quickfix",
		Diagnostics: []tools.LspDiagnostic{{Range: tools.LspRange{Start: tools.LspPos{Line: 0, Character: 2}, End: tools.LspPos{Line: 0, Character: 5}}, Severity: "warning", Message: "misspelled", Source: "gopls"}},
		Edit: &tools.LspWorkspaceEdit{Changes: map[string][]tools.LspTextEdit{
			"file:///a.go": {{Range: tools.LspRange{Start: tools.LspPos{Line: 0, Character: 2}, End: tools.LspPos{Line: 0, Character: 5}}, NewText: "correct"}},
		}},
		Command: &tools.LspCommand{
			Title:     "Apply fix",
			Command:   "gopls.apply",
			Arguments: []any{"file:///a.go", 3, true},
		},
		IsPreferred: true,
		Disabled:    &tools.LspCodeActionDisabled{Reason: "needs permission"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapCodeAction() = %+v, want %+v", got, want)
	}

	minimal := mapCodeAction(lsp.CodeAction{Title: "No payload"})
	if minimal.Edit != nil || minimal.Command != nil || minimal.Disabled != nil || minimal.Diagnostics != nil {
		t.Fatalf("minimal action should leave optional fields nil: %+v", minimal)
	}
	if mapCommand(nil) != nil {
		t.Fatal("mapCommand(nil) should be nil")
	}
}

func TestMapSymbolInfoAndLocationAndTextEdit(t *testing.T) {
	info := lsp.SymbolInformation{
		Name:          "Server",
		Kind:          23,
		Location:      lsp.Location{URI: "file:///server.go", Range: testRange(9, 0, 9, 18)},
		ContainerName: "internal",
	}
	wantInfo := tools.LspSymbolInfo{
		Name:          "Server",
		Kind:          23,
		Location:      tools.LspLocation{URI: "file:///server.go", Range: tools.LspRange{Start: tools.LspPos{Line: 9}, End: tools.LspPos{Line: 9, Character: 18}}},
		ContainerName: "internal",
	}
	if got := mapSymbolInfo(info); !reflect.DeepEqual(got, wantInfo) {
		t.Fatalf("mapSymbolInfo() = %+v, want %+v", got, wantInfo)
	}
	wantLoc := tools.LspLocation{URI: "file:///x.go", Range: tools.LspRange{Start: tools.LspPos{Line: 1, Character: 2}, End: tools.LspPos{Line: 3, Character: 4}}}
	if got := mapLocation(lsp.Location{URI: "file:///x.go", Range: testRange(1, 2, 3, 4)}); !reflect.DeepEqual(got, wantLoc) {
		t.Fatalf("mapLocation() = %+v, want %+v", got, wantLoc)
	}
	wantEdit := []tools.LspTextEdit{{Range: tools.LspRange{Start: tools.LspPos{Line: 1}, End: tools.LspPos{Line: 1, Character: 1}}, NewText: " "}}
	if got := mapTextEdits([]lsp.TextEdit{{Range: testRange(1, 0, 1, 1), NewText: " "}}); !reflect.DeepEqual(got, wantEdit) {
		t.Fatalf("mapTextEdits() = %+v, want %+v", got, wantEdit)
	}
	if got := mapTextEdits(nil); got == nil || len(got) != 0 {
		t.Fatalf("mapTextEdits(nil) = %+v, want non-nil empty slice", got)
	}
}

func TestLspManagerCachesSuccessPerRootAndServer(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		// 零值客户端就足够了：缓存从不解引用它。
		return &lsp.Client{}, nil
	})
	m := newLspManager(context.Background())
	t.Cleanup(m.cancel)

	root := t.TempDir()
	goServer, ok := m.reg.ForPath("main.go")
	if !ok {
		t.Fatal("expected gopls to be registered for .go")
	}
	first, err := m.clientFor(goServer, root)
	if err != nil || first == nil {
		t.Fatalf("first clientFor: client=%v err=%v", first, err)
	}
	second, err := m.clientFor(goServer, root)
	if err != nil || second != first {
		t.Fatalf("second clientFor should reuse the first client: client=%v err=%v", second, err)
	}
	if calls != 1 {
		t.Fatalf("spawn calls after two .go lookups = %d, want 1", calls)
	}

	pythonServer, ok := m.reg.Lookup("python")
	if !ok {
		t.Fatal("expected pyright to be registered for python")
	}
	other, err := m.clientFor(pythonServer, root)
	if err != nil || other == first {
		t.Fatalf("different server should get its own client: client=%v err=%v", other, err)
	}
	if calls != 2 {
		t.Fatalf("spawn calls after a python lookup = %d, want 2", calls)
	}

	// 不同根目录下相同的服务名是不同的缓存键。
	otherRoot, err := m.clientFor(goServer, t.TempDir())
	if err != nil || otherRoot == first {
		t.Fatalf("different root should get its own client: client=%v err=%v", otherRoot, err)
	}
	if calls != 3 {
		t.Fatalf("spawn calls after a second root = %d, want 3", calls)
	}
}

func TestLspManagerCachesSpawnFailure(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		return nil, errors.New("exec: \"gopls\": executable file not found")
	})
	m := newLspManager(context.Background())
	t.Cleanup(m.cancel)

	server, _ := m.reg.ForPath("main.go")
	root := t.TempDir()
	_, first := m.clientFor(server, root)
	_, second := m.clientFor(server, root)
	if calls != 1 {
		t.Fatalf("spawn calls after two failures = %d, want 1 (negative cache)", calls)
	}
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("failure should be cached: first=%v second=%v", first, second)
	}
	if !strings.Contains(first.Error(), "gopls") || !strings.Contains(first.Error(), "not installed") {
		t.Fatalf("failure message should be actionable, got %q", first.Error())
	}
}

func TestLspManagerCloseCancelsContext(t *testing.T) {
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		t.Fatal("no spawn expected")
		return nil, nil
	})
	m := newLspManager(context.Background())
	if err := m.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-m.ctx.Done():
	default:
		t.Fatal("Close() should cancel the manager context")
	}
}

func TestLspAdapterRejectsUnsupportedPathWithoutSpawning(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		return &lsp.Client{}, nil
	})
	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: t.TempDir()}
	t.Cleanup(a.mgr.cancel)

	_, err := a.DocumentSymbol("notes.unknownext")
	if err == nil || !strings.Contains(err.Error(), "no LSP server configured") {
		t.Fatalf("unsupported path error = %v, want a clear 'no LSP server configured' error", err)
	}
	if calls != 0 {
		t.Fatalf("spawn calls for unsupported path = %d, want 0", calls)
	}
}

func TestLspAdapterUnavailableServerReturnsClearErrorAndCachesIt(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		return nil, errors.New("executable file not found")
	})
	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: t.TempDir()}
	t.Cleanup(a.mgr.cancel)

	_, first := a.DocumentSymbol("main.go")
	if first == nil || !strings.Contains(first.Error(), "gopls") || !strings.Contains(first.Error(), "not installed") {
		t.Fatalf("unavailable server error = %v, want gopls / not installed", first)
	}
	_, second := a.References("main.go", 1, 2, false)
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("cached error mismatch: first=%v second=%v", first, second)
	}
	if calls != 1 {
		t.Fatalf("spawn calls after two lsp tools = %d, want 1 (negative cache)", calls)
	}
}

func TestLspAdapterWorkspaceSymbolsEmptyWhenNoServer(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		return nil, errors.New("not installed")
	})
	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: t.TempDir()}
	t.Cleanup(a.mgr.cancel)

	got, err := a.WorkspaceSymbols("anything")
	if err != nil {
		t.Fatalf("WorkspaceSymbols() error = %v, want graceful empty result", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("WorkspaceSymbols() = %+v, want non-nil empty slice", got)
	}
	if calls != 1 {
		t.Fatalf("spawn calls = %d, want exactly one on-demand gopls attempt", calls)
	}
}

func TestLspAdapterResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: root}
	t.Cleanup(a.mgr.cancel)

	if got, want := a.resolvePath("pkg/main.go"), filepath.Join(root, "pkg", "main.go"); got != want {
		t.Fatalf("resolvePath(relative) = %q, want %q", got, want)
	}
	absolute := filepath.Join(root, "x.go")
	if got := a.resolvePath(absolute); got != absolute {
		t.Fatalf("resolvePath(absolute) = %q, want %q", got, absolute)
	}
}

func TestNewLspIdeAdapterEnvKillSwitch(t *testing.T) {
	t.Setenv("ARGENT_LSP", "0")
	if got := NewLspIdeAdapter(t.TempDir()); got != nil {
		t.Fatalf("ARGENT_LSP=0 should disable the adapter, got %T", got)
	}
	t.Setenv("ARGENT_LSP", "1")
	if got := NewLspIdeAdapter(t.TempDir()); got == nil {
		t.Fatal("ARGENT_LSP=1 should activate the adapter")
	}
}
