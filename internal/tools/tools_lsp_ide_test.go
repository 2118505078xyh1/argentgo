package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeLspIde 是一个确定性的 LspIde，返回固定的 DTO 并记录
// 它收到的每个参数，使工具的透传行为可测试。
type fakeLspIde struct {
	symbols []LspSymbol
	refs    []LspLocation
	actions []LspCodeAction
	rename  *LspWorkspaceEdit
	edits   []LspTextEdit
	infos   []LspSymbolInfo

	symbolPath string

	refPath    string
	refLine    int
	refChar    int
	refInclude bool

	actionPath      string
	actionStartLine int
	actionStartChar int
	actionEndLine   int
	actionEndChar   int

	renamePath    string
	renameLine    int
	renameChar    int
	renameNewName string

	fmtPath         string
	fmtTabSize      int
	fmtInsertSpaces bool

	query string

	calls int
}

func (f *fakeLspIde) DocumentSymbol(path string) ([]LspSymbol, error) {
	f.symbolPath = path
	f.calls++
	return f.symbols, nil
}

func (f *fakeLspIde) References(path string, line, character int, includeDecl bool) ([]LspLocation, error) {
	f.refPath, f.refLine, f.refChar, f.refInclude = path, line, character, includeDecl
	f.calls++
	return f.refs, nil
}

func (f *fakeLspIde) CodeActions(path string, startLine, startChar, endLine, endChar int) ([]LspCodeAction, error) {
	f.actionPath = path
	f.actionStartLine, f.actionStartChar = startLine, startChar
	f.actionEndLine, f.actionEndChar = endLine, endChar
	f.calls++
	return f.actions, nil
}

func (f *fakeLspIde) Rename(path string, line, char int, newName string) (*LspWorkspaceEdit, error) {
	f.renamePath, f.renameLine, f.renameChar, f.renameNewName = path, line, char, newName
	f.calls++
	return f.rename, nil
}

func (f *fakeLspIde) Formatting(path string, tabSize int, insertSpaces bool) ([]LspTextEdit, error) {
	f.fmtPath, f.fmtTabSize, f.fmtInsertSpaces = path, tabSize, insertSpaces
	f.calls++
	return f.edits, nil
}

func (f *fakeLspIde) WorkspaceSymbols(query string) ([]LspSymbolInfo, error) {
	f.query = query
	f.calls++
	return f.infos, nil
}

func cannedFake() *fakeLspIde {
	return &fakeLspIde{
		symbols: []LspSymbol{{
			Name: "DoThing", Kind: 6, Depth: 1,
			Range:          LspRange{Start: LspPos{Line: 2, Character: 0}, End: LspPos{Line: 4, Character: 1}},
			SelectionRange: LspRange{Start: LspPos{Line: 2, Character: 5}, End: LspPos{Line: 2, Character: 12}},
			URI:            "file:///work/a.go", Detail: "func",
		}},
		refs: []LspLocation{{
			URI:   "file:///work/b.go",
			Range: LspRange{Start: LspPos{Line: 9, Character: 3}, End: LspPos{Line: 9, Character: 10}},
		}},
		actions: []LspCodeAction{{
			Title: "Add missing import", Kind: "quickfix", IsPreferred: true,
			Edit: &LspWorkspaceEdit{Changes: map[string][]LspTextEdit{
				"file:///work/a.go": {{Range: LspRange{Start: LspPos{Line: 0, Character: 0}}, NewText: "package a\n"}},
			}},
		}},
		rename: &LspWorkspaceEdit{Changes: map[string][]LspTextEdit{
			"file:///work/a.go": {{Range: LspRange{Start: LspPos{Line: 3, Character: 1}, End: LspPos{Line: 3, Character: 7}}, NewText: "Renamed"}},
		}},
		edits: []LspTextEdit{{
			Range:   LspRange{Start: LspPos{Line: 1, Character: 0}, End: LspPos{Line: 1, Character: 4}},
			NewText: "    ",
		}},
		infos: []LspSymbolInfo{{
			Name: "DoThing", Kind: 6, ContainerName: "pkg",
			Location: LspLocation{
				URI:   "file:///work/a.go",
				Range: LspRange{Start: LspPos{Line: 2, Character: 0}, End: LspPos{Line: 2, Character: 7}},
			},
		}},
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal expected: %v", err)
	}
	return string(data)
}

// TestLspIdeToolsReturnExpectedJSON 验证每个工具把适配器的
// 结果序列化为中立 DTO 的紧凑 JSON。
func TestLspIdeToolsReturnExpectedJSON(t *testing.T) {
	ctx := context.Background()
	fake := cannedFake()

	cases := []struct {
		name     string
		tool     Tool
		args     string
		expected any
	}{
		{"lsp_symbols", newLspSymbolsTool(fake), `{"path":"a.go"}`, fake.symbols},
		{"lsp_references", newLspReferencesTool(fake), `{"path":"a.go","line":2,"character":5,"includeDeclaration":true}`, fake.refs},
		{"lsp_code_actions", newLspCodeActionsTool(fake), `{"path":"a.go","startLine":2,"startChar":0,"endLine":4,"endChar":1}`, fake.actions},
		{"lsp_rename_preview", newLspRenamePreviewTool(fake), `{"path":"a.go","line":3,"character":1,"newName":"Renamed"}`, fake.rename},
		{"lsp_format", newLspFormatTool(fake), `{"path":"a.go","tabSize":2,"insertSpaces":false}`, fake.edits},
		{"lsp_workspace_symbols", newLspWorkspaceSymbolsTool(fake), `{"query":"DoThing"}`, fake.infos},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tool.Run(ctx, t.TempDir(), json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			want := mustJSON(t, tc.expected)
			if got != want {
				t.Fatalf("output mismatch\n got: %s\nwant: %s", got, want)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("output is not valid JSON: %s", got)
			}
		})
	}
}

// TestLspIdeToolsUnavailableWithoutAdapter 检查 nil 适配器时
// 六个工具都给出优雅消息、无错误且不 panic。
func TestLspIdeToolsUnavailableWithoutAdapter(t *testing.T) {
	ctx := context.Background()
	tools := []Tool{
		newLspSymbolsTool(nil), newLspReferencesTool(nil), newLspCodeActionsTool(nil),
		newLspRenamePreviewTool(nil), newLspFormatTool(nil), newLspWorkspaceSymbolsTool(nil),
	}
	args := `{"path":"a.go","line":0,"character":0,"query":"x","newName":"x"}`
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			got, err := tool.Run(ctx, t.TempDir(), json.RawMessage(args))
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if got != lspIdeUnavailable {
				t.Fatalf("got %q, want %q", got, lspIdeUnavailable)
			}
		})
	}
}

// TestLspIdeToolsMetadata 验证 schema、只读性质和描述
// 长度，使这些工具可安全地在只读沙箱中公布。
func TestLspIdeToolsMetadata(t *testing.T) {
	tools := []Tool{
		newLspSymbolsTool(nil), newLspReferencesTool(nil), newLspCodeActionsTool(nil),
		newLspRenamePreviewTool(nil), newLspFormatTool(nil), newLspWorkspaceSymbolsTool(nil),
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			if !json.Valid([]byte(tool.Schema())) {
				t.Fatalf("schema is not valid JSON: %s", tool.Schema())
			}
			if tool.Mutating() {
				t.Fatalf("%s must be read-only", tool.Name())
			}
			if len(tool.Description()) >= maxToolDescLen {
				t.Fatalf("description too long: %d bytes", len(tool.Description()))
			}
		})
	}
}

// TestSetLspIdeSwapsAdapter 验证 app 接线：SetLspIde 之前工具
// 报告不可用，之后它们调用新安装的适配器。
func TestSetLspIdeSwapsAdapter(t *testing.T) {
	ctx := context.Background()
	r := New(t.TempDir(), false)

	before, err := r.Run(ctx, "lsp_symbols", `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("Run before wiring: %v", err)
	}
	if before != lspIdeUnavailable {
		t.Fatalf("before wiring: got %q, want %q", before, lspIdeUnavailable)
	}

	fake := cannedFake()
	r.SetLspIde(fake)

	after, err := r.Run(ctx, "lsp_symbols", `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("Run after wiring: %v", err)
	}
	if want := mustJSON(t, fake.symbols); after != want {
		t.Fatalf("after wiring: got %s, want %s", after, want)
	}
	if fake.symbolPath != "a.go" {
		t.Fatalf("adapter received path %q, want %q", fake.symbolPath, "a.go")
	}

	// 六个工具都已注册，现在指向新的适配器。
	for _, name := range []string{
		"lsp_symbols", "lsp_references", "lsp_code_actions",
		"lsp_rename_preview", "lsp_format", "lsp_workspace_symbols",
	} {
		found := false
		for _, registered := range r.Names() {
			if registered == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s is not registered", name)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", fake.calls)
	}
}

// TestLspIdeToolsPassZeroBasedPositions 检查坐标原样到达
// 适配器（不做基于 1 的转换）且格式化默认值合理。
func TestLspIdeToolsPassZeroBasedPositions(t *testing.T) {
	ctx := context.Background()
	fake := cannedFake()

	if _, err := newLspReferencesTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go","line":0,"character":7,"includeDeclaration":true}`)); err != nil {
		t.Fatalf("references: %v", err)
	}
	if fake.refLine != 0 || fake.refChar != 7 || !fake.refInclude || fake.refPath != "a.go" {
		t.Fatalf("references got path=%q line=%d char=%d include=%v", fake.refPath, fake.refLine, fake.refChar, fake.refInclude)
	}

	if _, err := newLspCodeActionsTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go","startLine":0,"startChar":1,"endLine":2,"endChar":3}`)); err != nil {
		t.Fatalf("code actions: %v", err)
	}
	if fake.actionStartLine != 0 || fake.actionStartChar != 1 || fake.actionEndLine != 2 || fake.actionEndChar != 3 {
		t.Fatalf("code actions got start=%d:%d end=%d:%d",
			fake.actionStartLine, fake.actionStartChar, fake.actionEndLine, fake.actionEndChar)
	}

	if _, err := newLspRenamePreviewTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go","line":0,"character":4,"newName":"New"}`)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if fake.renameLine != 0 || fake.renameChar != 4 || fake.renameNewName != "New" {
		t.Fatalf("rename got line=%d char=%d name=%q", fake.renameLine, fake.renameChar, fake.renameNewName)
	}

	// 显式的格式化选项被原样透传。
	if _, err := newLspFormatTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go","tabSize":8,"insertSpaces":false}`)); err != nil {
		t.Fatalf("format: %v", err)
	}
	if fake.fmtTabSize != 8 || fake.fmtInsertSpaces {
		t.Fatalf("format got tabSize=%d insertSpaces=%v", fake.fmtTabSize, fake.fmtInsertSpaces)
	}

	// 省略选项时的默认值：tabSize 4、insertSpaces true。
	if _, err := newLspFormatTool(fake).Run(ctx, t.TempDir(),
		json.RawMessage(`{"path":"a.go"}`)); err != nil {
		t.Fatalf("format defaults: %v", err)
	}
	if fake.fmtTabSize != 4 || !fake.fmtInsertSpaces {
		t.Fatalf("format defaults got tabSize=%d insertSpaces=%v", fake.fmtTabSize, fake.fmtInsertSpaces)
	}
}
