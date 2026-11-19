package tools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestRepoMapListsDirsFilesAndEntryPoints(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, "go.mod", "module example\n")
	writeFile(t, root, "cmd/app/main.go", "package main\n")
	writeFile(t, root, "internal/a.go", "package internal\n")
	writeFile(t, root, "node_modules/pkg/x.go", "package pkg\n")
	writeFile(t, root, "target/debug/x.rs", "fn main() {}\n")
	writeFile(t, root, "dist/bundle.js", "// built\n")

	registry := tools.New(root, true)
	out := mustRun(t, registry, "repo_map", `{}`)
	for _, want := range []string{
		"cmd/", "internal/", "main.go:3 [entry]", "go.mod:1 [entry]",
		"cmd/app/main.go:1 [entry]", "internal/a.go:1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo_map = %q, want %q", out, want)
		}
	}
	for _, unwanted := range []string{"node_modules", "target", "dist"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("repo_map included ignored path %q: %q", unwanted, out)
		}
	}
	if strings.Index(out, "cmd/") > strings.Index(out, "internal/") {
		t.Fatalf("repo_map dirs are not sorted: %q", out)
	}

	again := mustRun(t, registry, "repo_map", `{}`)
	if again != out {
		t.Fatalf("repo_map is not deterministic:\n%s\n---\n%s", out, again)
	}
}

func TestRepoMapScopesPathAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cmd/app/main.go", "package main\n")
	writeFile(t, root, "go.mod", "module example\n")
	registry := tools.New(root, true)

	scoped := mustRun(t, registry, "repo_map", `{"path":"cmd"}`)
	if !strings.Contains(scoped, "app/main.go:1 [entry]") || strings.Contains(scoped, "go.mod") {
		t.Fatalf("scoped repo_map = %q", scoped)
	}
	if _, err := registry.Run(context.Background(), "repo_map", `{"path":"../outside"}`); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v, want a workspace escape rejection", err)
	}
	if _, err := registry.Run(context.Background(), "repo_map", `{"path":"go.mod"}`); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want a not-a-directory error", err)
	}
	if _, err := registry.Run(context.Background(), "repo_map", `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Fatalf("err = %v, want a bad-arguments error", err)
	}
}
