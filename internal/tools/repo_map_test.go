package tools_test

import (
	"context"
	"fmt"
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

func TestRepoMapPinsImportantFilesWhenCrowdedOut(t *testing.T) {
	root := t.TempDir()
	// 100 filler files in a directory that sorts before the important ones,
	// so a naive "first 80 walked files" cap would drop everything under zz/.
	for i := 0; i < 100; i++ {
		writeFile(t, root, fmt.Sprintf("aaa/f%03d.txt", i), "filler\n")
	}
	writeFile(t, root, "zz/go.mod", "module example\n")
	writeFile(t, root, "zz/README.md", "# Title\n")
	writeFile(t, root, "zz/Cargo.toml", "[package]\n")
	writeFile(t, root, "zz/Dockerfile", "FROM scratch\n")

	registry := tools.New(root, true)
	out := mustRun(t, registry, "repo_map", `{"symbols":false}`)

	for _, want := range []string{
		"files (80):",
		"zz/go.mod:1 [entry]", "zz/README.md:1 [entry]",
		"zz/Cargo.toml:1 [entry]", "zz/Dockerfile:1 [entry]",
		"... [capped at 80 files]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo_map missing %q under a tight file budget:\n%s", want, out)
		}
	}
	// Important files are pinned to the front even though their paths sort
	// after the ordinary filler files.
	firstImportant := strings.Index(out, "zz/README.md")
	firstOrdinary := strings.Index(out, "aaa/f000.txt")
	if firstImportant < 0 || firstOrdinary < 0 || firstImportant > firstOrdinary {
		t.Fatalf("important files were not pinned ahead of ordinary files:\n%s", out)
	}
}

func TestRepoMapSymbolOrderingUnchangedByImportantPinning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n\nfunc Helper() {}\n")
	writeFile(t, root, "ordinary.go", "package main\n\nfunc Other() {}\n")
	writeFile(t, root, "go.mod", "module example\n") // important file is present

	registry := tools.New(root, true)

	// An exact mention still wins regardless of the important-file pinning.
	mentioned := mustRun(t, registry, "repo_map", `{"mention":"Other"}`)
	other := strings.Index(mentioned, "[func] ordinary.go:3 Other")
	helper := strings.Index(mentioned, "[func] main.go:3 Helper")
	if other < 0 || helper < 0 || other > helper {
		t.Fatalf("exact mention should rank Other above Helper:\n%s", mentioned)
	}

	// Without a mention, the entry-point base-score boost still applies.
	plain := mustRun(t, registry, "repo_map", `{}`)
	helper = strings.Index(plain, "[func] main.go:3 Helper")
	other = strings.Index(plain, "[func] ordinary.go:3 Other")
	if helper < 0 || other < 0 || helper > other {
		t.Fatalf("entry-point boost should rank Helper above Other:\n%s", plain)
	}
}

func TestRepoMapEmptyWorkspace(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	out := mustRun(t, registry, "repo_map", `{}`)
	for _, want := range []string{"dirs:", "  (none)", "files (0):", "symbols (0):"} {
		if !strings.Contains(out, want) {
			t.Fatalf("empty repo_map missing %q: %q", want, out)
		}
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
