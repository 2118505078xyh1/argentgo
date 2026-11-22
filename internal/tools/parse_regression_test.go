package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckParseRegression(t *testing.T) {
	orig, had := os.LookupEnv(parseRegressionEnv)
	defer func() {
		if had {
			os.Setenv(parseRegressionEnv, orig)
		} else {
			os.Unsetenv(parseRegressionEnv)
		}
	}()

	good := []byte("package main\nfunc f() {}\n")
	bad := []byte("package main\nfunc f( {\n")

	os.Unsetenv(parseRegressionEnv)
	if err := checkParseRegression("x.go", good, bad); err != nil {
		t.Fatalf("off should be no-op: %v", err)
	}
	os.Setenv(parseRegressionEnv, "1")
	if err := checkParseRegression("x.go", good, bad); err == nil {
		t.Fatal("should reject valid -> invalid Go")
	}
	if err := checkParseRegression("x.go", good, good); err != nil {
		t.Fatalf("valid -> valid should pass: %v", err)
	}
	// 原本就有错误时不拦：允许模型修复其它错误。
	if err := checkParseRegression("x.go", bad, good); err != nil {
		t.Fatalf("invalid -> valid should pass: %v", err)
	}
	// 非 .go 文件不受影响。
	if err := checkParseRegression("x.txt", good, bad); err != nil {
		t.Fatalf("non-go should be no-op: %v", err)
	}
}

func TestEditRejectsParseRegressionOnDisk(t *testing.T) {
	os.Setenv(parseRegressionEnv, "1")
	defer os.Unsetenv(parseRegressionEnv)
	root := t.TempDir()
	path := filepath.Join(root, "m.go")
	if err := os.WriteFile(path, []byte("package main\nfunc f() { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"path": "m.go", "old": "return 1", "new": "return ("})
	if _, err := (editTool{}).Run(context.Background(), root, raw); err == nil {
		t.Fatal("edit should be rejected")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package main\nfunc f() { return 1 }\n" {
		t.Fatalf("file changed despite guard: %q", got)
	}
}
