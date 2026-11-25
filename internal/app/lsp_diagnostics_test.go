package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/lsp"
)

func TestMapNeutralDiagnosticsUsesOneBasedPositions(t *testing.T) {
	got := mapNeutralDiagnostics([]lsp.Diagnostic{
		{Range: testRange(11, 4, 11, 9), Severity: "error", Message: "undefined: foo", Source: "gopls"},
	})
	want := []NeutralDiagnostic{{Line: 12, Column: 5, Severity: "error", Message: "undefined: foo", Source: "gopls"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapNeutralDiagnostics() = %+v, want %+v", got, want)
	}
	if empty := mapNeutralDiagnostics(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("mapNeutralDiagnostics(nil) = %+v, want non-nil empty slice", empty)
	}
}

func TestFormatDiagnosticsFiltersErrorsCapsAndMarksTruncation(t *testing.T) {
	diags := []NeutralDiagnostic{{Line: 1, Severity: "warning", Message: "a warning"}}
	for i := 0; i < 25; i++ {
		// 第 30..6 行；排序后的输出必须从 6 开始（升序）。
		diags = append(diags, NeutralDiagnostic{Line: 30 - i, Column: 2, Severity: "error", Message: "boom", Source: "gopls"})
	}

	got := FormatDiagnostics("main.go", diags, 20)
	lines := strings.Split(got, "\n")
	if len(lines) != 21 { // 20 条被限制的错误 + 1 行截断标记
		t.Fatalf("FormatDiagnostics lines = %d, want 21:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "main.go:6:2 [gopls]: boom") {
		t.Fatalf("first line = %q, want ascending 1-based main.go:6:2", lines[0])
	}
	if !strings.HasSuffix(lines[20], "... (5 more error(s) truncated)") {
		t.Fatalf("truncation line = %q, want a 5-dropped marker", lines[20])
	}
	if strings.Contains(got, "a warning") {
		t.Fatalf("non-error severity leaked into output:\n%s", got)
	}
}

func TestFormatDiagnosticsUsesDefaultCapForNonPositiveLimit(t *testing.T) {
	diags := make([]NeutralDiagnostic, 0, maxReportDiagnostics+1)
	for i := 0; i <= maxReportDiagnostics; i++ {
		diags = append(diags, NeutralDiagnostic{Line: i + 1, Severity: "error", Message: "x"})
	}
	got := FormatDiagnostics("x.go", diags, 0)
	if lines := strings.Split(got, "\n"); len(lines) != maxReportDiagnostics+1 {
		t.Fatalf("default cap lines = %d, want %d:\n%s", len(lines), maxReportDiagnostics+1, got)
	}
}

func TestFormatDiagnosticsNoErrorsReturnsEmpty(t *testing.T) {
	if got := FormatDiagnostics("x.go", nil, 20); got != "" {
		t.Fatalf("nil diagnostics = %q, want empty", got)
	}
	nonErrors := []NeutralDiagnostic{
		{Line: 1, Severity: "hint", Message: "hint"},
		{Line: 2, Severity: "information", Message: "info"},
		{Line: 3, Severity: "warning", Message: "warning"},
	}
	if got := FormatDiagnostics("x.go", nonErrors, 20); got != "" {
		t.Fatalf("non-error diagnostics = %q, want empty", got)
	}
}

func TestFormatDiagnosticsFoldsAndTrimsSeverity(t *testing.T) {
	diags := []NeutralDiagnostic{{Line: 4, Severity: " ERROR ", Message: "boom"}}
	if got := FormatDiagnostics("x.go", diags, 20); !strings.Contains(got, "x.go:4: boom") {
		t.Fatalf("folded/trimmed severity = %q, want it recognized as an error", got)
	}
}

func TestFormatDiagnosticsDoesNotMutateInput(t *testing.T) {
	diags := []NeutralDiagnostic{
		{Line: 9, Severity: "error", Message: "b"},
		{Line: 2, Severity: "error", Message: "a"},
	}
	want := append([]NeutralDiagnostic(nil), diags...)
	got := FormatDiagnostics("x.go", diags, 20)
	if !reflect.DeepEqual(diags, want) {
		t.Fatalf("input mutated: got %+v, want %+v", diags, want)
	}
	if !strings.HasPrefix(got, "x.go:2: a") {
		t.Fatalf("output not sorted by line: %q", got)
	}
}

func TestLspAdapterDiagnosticsGracefulWithoutServer(t *testing.T) {
	var calls int
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		calls++
		return nil, errors.New("executable file not found")
	})
	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: t.TempDir()}
	t.Cleanup(a.mgr.cancel)

	got, err := a.Diagnostics(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("Diagnostics() error = %v, want graceful empty result", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Diagnostics() = %+v, want non-nil empty slice", got)
	}
	if calls != 1 {
		t.Fatalf("spawn calls = %d, want one cached attempt", calls)
	}

	// 不支持的扩展名绝不会派生进程，并仍会静默降级。
	got, err = a.Diagnostics(context.Background(), "notes.xyz")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("unsupported path: got=%+v err=%v, want empty and no error", got, err)
	}
	if calls != 1 {
		t.Fatalf("spawn calls after unsupported path = %d, want 1", calls)
	}
}

func TestLspAdapterDiagnosticsSwallowsFetchErrors(t *testing.T) {
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return &lsp.Client{}, nil // 从不被解引用：fetchDiagnostics 已被替换
	})
	previous := fetchDiagnostics
	fetchDiagnostics = func(context.Context, *lsp.Client, string) ([]lsp.Diagnostic, error) {
		return nil, lsp.ErrTimeout
	}
	t.Cleanup(func() { fetchDiagnostics = previous })

	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: t.TempDir()}
	t.Cleanup(a.mgr.cancel)
	got, err := a.Diagnostics(context.Background(), "main.go")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("timeout: got=%+v err=%v, want empty and no error", got, err)
	}
}

func TestLspAdapterDiagnosticsMapsFetchedDiagnostics(t *testing.T) {
	withStartFn(t, func(context.Context, lsp.Server, string) (*lsp.Client, error) {
		return &lsp.Client{}, nil
	})
	root := t.TempDir()
	previous := fetchDiagnostics
	fetchDiagnostics = func(_ context.Context, _ *lsp.Client, absPath string) ([]lsp.Diagnostic, error) {
		if want := filepath.Join(root, "main.go"); absPath != want {
			t.Errorf("fetchDiagnostics path = %q, want %q", absPath, want)
		}
		return []lsp.Diagnostic{
			{Range: testRange(11, 4, 11, 9), Severity: "error", Message: "undefined: foo", Source: "gopls"},
			{Range: testRange(0, 0, 0, 1), Severity: "warning", Message: "stale hint", Source: "gopls"},
		}, nil
	}
	t.Cleanup(func() { fetchDiagnostics = previous })

	a := &lspIdeAdapter{mgr: newLspManager(context.Background()), root: root}
	t.Cleanup(a.mgr.cancel)
	got, err := a.Diagnostics(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("Diagnostics() error = %v", err)
	}
	want := []NeutralDiagnostic{
		{Line: 12, Column: 5, Severity: "error", Message: "undefined: foo", Source: "gopls"},
		{Line: 1, Column: 1, Severity: "warning", Message: "stale hint", Source: "gopls"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Diagnostics() = %+v, want %+v", got, want)
	}
	if rendered := FormatDiagnostics("main.go", got, 20); !strings.Contains(rendered, "main.go:12:5 [gopls]: undefined: foo") || strings.Contains(rendered, "stale hint") {
		t.Fatalf("formatted diagnostics = %q, want only the 1-based error", rendered)
	}
}
