package config

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatAllDiagnosticsMultiple(t *testing.T) {
	diags := []Diagnostic{
		{Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty"},
		{Path: "$.baseURL", Kind: DiagnosticConflict, Message: "conflicting values; using baseURL"},
		{Path: "$.logLevel", Kind: DiagnosticUnsupported, Message: "unsupported legacy setting; ignored"},
	}

	got := formatAllDiagnostics(diags)
	// 按路径字典序排列：baseURL < logLevel < model。
	want := strings.Join([]string{
		"warning/conflict $.baseURL: conflicting values; using baseURL",
		"info/unsupported $.logLevel: unsupported legacy setting; ignored",
		"error/invalid $.model: model must not be empty",
	}, "\n")
	if got != want {
		t.Fatalf("formatAllDiagnostics() =\n%s\nwant:\n%s", got, want)
	}
	// 每一类严重级别都出现在输出里。
	for _, severity := range []string{"error/", "warning/", "info/"} {
		if !strings.Contains(got, severity) {
			t.Errorf("output missing severity %q: %s", severity, got)
		}
	}
}

func TestFormatAllDiagnosticsSingle(t *testing.T) {
	diags := []Diagnostic{{Path: "$.maxTurns", Kind: DiagnosticInvalid, Message: "maxTurns must be >= 0"}}
	want := "error/invalid $.maxTurns: maxTurns must be >= 0"
	if got := formatAllDiagnostics(diags); got != want {
		t.Fatalf("formatAllDiagnostics() = %q, want %q", got, want)
	}
}

func TestFormatAllDiagnosticsEmpty(t *testing.T) {
	cases := []struct {
		name  string
		diags []Diagnostic
	}{
		{name: "nil", diags: nil},
		{name: "empty slice", diags: []Diagnostic{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAllDiagnostics(tc.diags); got != "" {
				t.Fatalf("formatAllDiagnostics() = %q, want empty string", got)
			}
		})
	}
}

func TestFormatAllDiagnosticsCJK(t *testing.T) {
	message := "模型名称不能为空：请检查配置（必填）"
	diags := []Diagnostic{{Path: "$.模型", Kind: DiagnosticInvalid, Message: message}}

	got := formatAllDiagnostics(diags)
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, message) {
		t.Fatalf("output lost the CJK message: %q", got)
	}
	if !strings.Contains(got, "$.模型") {
		t.Fatalf("output lost the CJK path: %q", got)
	}
	// 中英文混排且消息内嵌换行时，CJK 字符仍完整，且只占一行。
	diags = []Diagnostic{{Path: "$.提示", Kind: DiagnosticUnsupported, Message: "旧设置已忽略\n请改用新键"}}
	got = formatAllDiagnostics(diags)
	if !utf8.ValidString(got) || !strings.Contains(got, "旧设置已忽略 请改用新键") {
		t.Fatalf("CJK message with newline mangled: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("one diagnostic should occupy one line: %q", got)
	}
}

func TestFormatAllDiagnosticsDedup(t *testing.T) {
	dup := Diagnostic{Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty"}
	diags := []Diagnostic{
		dup,
		{Path: "$.baseURL", Kind: DiagnosticConflict, Message: "conflicting values"},
		dup,
		dup,
	}
	got := formatAllDiagnostics(diags)
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Fatalf("got %d lines after dedup, want 2: %s", len(lines), got)
	}
	if strings.Count(got, "model must not be empty") != 1 {
		t.Fatalf("duplicate diagnostic was not collapsed: %s", got)
	}
}

func TestFormatAllDiagnosticsStableOrder(t *testing.T) {
	diags := []Diagnostic{
		{Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty"},
		{Path: "$.baseURL", Kind: DiagnosticConflict, Message: "conflicting values"},
		{Path: "$.logLevel", Kind: DiagnosticUnsupported, Message: "unsupported legacy setting"},
	}
	reversed := []Diagnostic{diags[2], diags[1], diags[0]}

	first := formatAllDiagnostics(diags)
	second := formatAllDiagnostics(reversed)
	if first != second {
		t.Fatalf("output depends on input order:\n%s\nvs\n%s", first, second)
	}

	lines := strings.Split(first, "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		// 行格式为 "严重级别/类别 路径: 消息"，路径是第二个字段。
		fields := strings.Fields(line)
		paths = append(paths, fields[1])
	}
	for i := 1; i < len(paths); i++ {
		if paths[i-1] > paths[i] {
			t.Fatalf("paths not in ascending order: %q before %q", paths[i-1], paths[i])
		}
	}
}

func TestFormatAllDiagnosticsDoesNotMutateInput(t *testing.T) {
	diags := []Diagnostic{
		{Path: "$.model", Kind: DiagnosticInvalid, Message: "model must not be empty"},
		{Path: "$.baseURL", Kind: DiagnosticConflict, Message: "conflicting values"},
	}
	before := append([]Diagnostic(nil), diags...)

	formatAllDiagnostics(diags)

	if !reflect.DeepEqual(diags, before) {
		t.Fatalf("formatAllDiagnostics mutated its input: got %+v, want %+v", diags, before)
	}
}

func TestValidateAllIsOptInAndReportsEverything(t *testing.T) {
	cfg, err := LoadWithOverrides(t.TempDir(),
		envFunc(map[string]string{"HOME": t.TempDir()}),
		map[string]any{"sandboxMode": "loose", "model": "", "maxTurns": -1})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}

	// Validate 保持"首个错误"约定。
	first := cfg.Validate()
	if first == nil || !strings.Contains(first.Error(), "sandboxMode") {
		t.Fatalf("Validate error = %v, want first-error sandboxMode complaint", first)
	}

	// ValidateAll 一次报告全部问题，且每条诊断的路径都出现在报告里。
	all := cfg.ValidateAll()
	if all == nil {
		t.Fatal("ValidateAll returned nil for invalid config")
	}
	for _, d := range cfg.Diagnostics() {
		if !strings.Contains(all.Error(), d.Path) {
			t.Errorf("ValidateAll report missing path %q: %s", d.Path, all)
		}
	}
	if lines := strings.Split(formatAllDiagnostics(cfg.Diagnostics()), "\n"); len(lines) != 3 {
		t.Fatalf("got %d diagnostics, want 3: %s", len(lines), lines)
	}

	valid, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": t.TempDir()}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := valid.ValidateAll(); err != nil {
		t.Fatalf("ValidateAll rejected a valid config: %v", err)
	}
}
