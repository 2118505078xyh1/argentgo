package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsDoctorReportsDiagnostics(t *testing.T) {
	root := t.TempDir()
	// 造一个合法命令与一个空正文命令（empty-body 诊断）。
	dir := filepath.Join(root, ".argent", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := "---\ndescription: ok\n---\nDo the thing.\n"
	empty := "---\ndescription: empty\n---\n\n"
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.md"), []byte(empty), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := App{
		StorageDir: root,
		IO:         IO{Out: &out, Err: &errOut, Env: func(string) string { return "" }},
	}
	code := runCommands(app, nil, []string{"-C", root, "--doctor"})
	if code != 0 {
		t.Fatalf("code = %d, err = %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "empty-body") && !strings.Contains(got, "no command diagnostics") {
		t.Fatalf("expected a diagnostic or clean report, got %q", got)
	}
}

func TestCommandsDoctorClean(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	app := App{
		StorageDir: root,
		IO:         IO{Out: &out, Err: &errOut, Env: func(string) string { return "" }},
	}
	if code := runCommands(app, nil, []string{"-C", root, "--doctor"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "no command diagnostics") {
		t.Fatalf("clean report missing: %q", out.String())
	}
}
