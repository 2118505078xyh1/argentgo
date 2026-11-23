package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsCommandListAndShow(t *testing.T) {
	// 切到含 docs/ 的临时目录，使 docRoot() 命中。
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "docs", "GUIDE.md"), []byte("# Guide\nhello docs\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "A.md"), []byte("# A\n"), 0o644)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	var out, errOut bytes.Buffer
	app := App{IO: IO{Out: &out, Err: &errOut}}
	if code := runDocs(app, context.Background(), []string{"list"}); code != 0 {
		t.Fatalf("list code=%d err=%q", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "A.md") || !strings.Contains(got, "GUIDE.md") {
		t.Fatalf("list = %q", got)
	}
	out.Reset()
	if code := runDocs(app, context.Background(), []string{"show", "GUIDE.md"}); code != 0 {
		t.Fatalf("show code=%d err=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "hello docs") {
		t.Fatalf("show = %q", out.String())
	}
}

func TestDocsCommandErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	app := App{IO: IO{Out: &out, Err: &errOut}}
	if code := runDocs(app, context.Background(), []string{"show"}); code == 0 {
		t.Fatal("show without name should fail")
	}
	if code := runDocs(app, context.Background(), []string{"bogus"}); code == 0 {
		t.Fatal("unknown subcommand should fail")
	}
	if code := runDocs(app, context.Background(), []string{"--help"}); code != 0 {
		t.Fatal("help should succeed")
	}
}

func TestDocsCommandShowMissing(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "docs"), 0o755)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	var out, errOut bytes.Buffer
	app := App{IO: IO{Out: &out, Err: &errOut}}
	if code := runDocs(app, context.Background(), []string{"show", "NOPE.md"}); code == 0 {
		t.Fatal("show missing doc should fail")
	}
	if errOut.String() == "" {
		t.Fatal("error should be printed")
	}
}
