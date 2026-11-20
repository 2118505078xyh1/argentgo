package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeSymFile(t *testing.T, root, rel, src string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findRepoSym(t *testing.T, syms []repoSymbol, name string) repoSymbol {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %+v", name, syms)
	return repoSymbol{}
}

func TestRepoSymbolsExtractsTopLevelDeclarations(t *testing.T) {
	root := t.TempDir()
	writeSymFile(t, root, "alpha.go", `package alpha

// Client is a thing.
type Client struct{}

const Limit = 10

var A, B int

func NewClient() *Client { return nil }

func (c *Client) Do() error { return nil }
`)
	writeSymFile(t, root, "note.txt", "not go\n")

	syms := extractRepoSymbols(root, []string{"alpha.go", "note.txt"})
	for _, want := range []string{"Client", "Limit", "A", "B", "NewClient", "Do"} {
		findRepoSym(t, syms, want)
	}
	cases := []struct {
		name, kind, receiver, file string
		line                       int
		exported                   bool
	}{
		{"Client", "type", "", "alpha.go", 4, true},
		{"Limit", "const", "", "alpha.go", 6, true},
		{"A", "var", "", "alpha.go", 8, true},
		{"B", "var", "", "alpha.go", 8, true},
		{"NewClient", "func", "", "alpha.go", 10, true},
		{"Do", "method", "Client", "alpha.go", 12, true},
	}
	for _, c := range cases {
		got := findRepoSym(t, syms, c.name)
		if got.Kind != c.kind || got.Receiver != c.receiver || got.File != c.file ||
			got.Line != c.line || got.Exported != c.exported {
			t.Errorf("symbol %s = %+v, want kind=%q receiver=%q file=%q line=%d exported=%v",
				c.name, got, c.kind, c.receiver, c.file, c.line, c.exported)
		}
	}
	if len(syms) != len(cases) {
		t.Fatalf("extracted %d symbols, want %d: %+v", len(syms), len(cases), syms)
	}
}

func TestExtractRepoSymbolsUsesProcessCache(t *testing.T) {
	clearRepoSymbolCache()
	defer clearRepoSymbolCache()
	root := t.TempDir()
	writeSymFile(t, root, "a.go", "package a\n\ntype Alpha struct{}\n")
	writeSymFile(t, root, "b.go", "package b\n\nfunc Beta() {}\n")
	files := []string{"a.go", "b.go"}

	first := extractRepoSymbols(root, files)
	second := extractRepoSymbols(root, files)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated extractRepoSymbols differ:\n%+v\n%+v", first, second)
	}

	// First call parses each Go file once (miss); the second call is served
	// entirely from the cache (hit). This proves extractRepoSymbols is wired
	// to the process-local cache rather than re-reading every file.
	stats := repoSymbolCacheStats()
	if stats.misses != len(files) {
		t.Fatalf("misses = %d, want %d (one per Go file on first call)", stats.misses, len(files))
	}
	if stats.hits != len(files) {
		t.Fatalf("hits = %d, want %d (one per Go file on second call)", stats.hits, len(files))
	}
}

func TestRepoSymbolsRankingDeterministicAndMentionWeighted(t *testing.T) {
	root := t.TempDir()
	writeSymFile(t, root, "alpha.go", "package alpha\n\ntype Client struct{}\n\nfunc (c *Client) Do() error { return nil }\n")
	writeSymFile(t, root, "beta.go", "package beta\n\nfunc BetaFunc() {}\n")
	writeSymFile(t, root, "gamma.go", "package gamma\n\nfunc Other() {}\n")

	syms := extractRepoSymbols(root, []string{"gamma.go", "alpha.go", "beta.go"})
	a := rankRepoSymbols(syms, []string{"Do"})
	b := rankRepoSymbols(syms, []string{"Do"})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("ranking is not deterministic:\n%+v\n%+v", a, b)
	}
	if a[0].Name != "Do" {
		t.Fatalf("exact mention should rank first, got %+v", a[0])
	}

	byPath := rankRepoSymbols(syms, []string{"beta.go"})
	if byPath[0].File != "beta.go" {
		t.Fatalf("path mention should boost beta.go symbols, got %+v", byPath[0])
	}

	// Reversing the input order must not change the ranking.
	reversed := make([]repoSymbol, len(syms))
	for i, s := range syms {
		reversed[len(syms)-1-i] = s
	}
	if !reflect.DeepEqual(rankRepoSymbols(reversed, []string{"Do"}), a) {
		t.Fatal("ranking depends on input order")
	}
}

func TestRepoSymbolsBudgetTrimsByPrefix(t *testing.T) {
	var syms []repoSymbol
	for i := 0; i < 60; i++ {
		syms = append(syms, repoSymbol{
			File: "pkg/gen.go", Name: fmt.Sprintf("Symbol%03d", i), Line: i + 1, Kind: "func",
		})
	}
	ranked := rankRepoSymbols(syms, nil)
	const budget = 60 // deliberately tiny: forces a cut
	kept := budgetRepoSymbols(ranked, budget)
	if len(kept) == 0 || len(kept) >= len(ranked) {
		t.Fatalf("budget kept %d of %d symbols, want a non-empty proper prefix", len(kept), len(ranked))
	}
	cost := 0
	for i, sym := range kept {
		if sym != ranked[i] {
			t.Fatal("budgeted symbols are not a prefix of the ranked list")
		}
		cost += repoMapTokenEstimate(formatRepoSymbol(sym))
	}
	if cost > budget {
		t.Fatalf("kept symbols cost ~%d tokens, over budget %d", cost, budget)
	}

	lines := repoMapSymbolSection(syms, nil, budget)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, fmt.Sprintf("symbols (%d):", len(kept))) {
		t.Fatalf("section header disagrees with kept symbols: %q", out)
	}
	if !strings.Contains(out, "token budget") {
		t.Fatalf("section did not report omitted symbols: %q", out)
	}
	// A generous budget keeps everything and reports no omission.
	all := repoMapSymbolSection(syms, nil, 100000)
	if !strings.Contains(strings.Join(all, "\n"), "symbols (60):") ||
		strings.Contains(strings.Join(all, "\n"), "omitted") {
		t.Fatalf("generous budget did not keep all symbols: %q", strings.Join(all, "\n"))
	}
}

func TestRepoSymbolsOutputUTF8Safe(t *testing.T) {
	root := t.TempDir()
	writeSymFile(t, root, "uni.go", "package uni\n\nfunc 你好World() {}\n\ntype 型 struct{}\n")

	syms := extractRepoSymbols(root, []string{"uni.go"})
	out := strings.Join(repoMapSymbolSection(syms, nil, repoMapSymbolTokenBudget), "\n")
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("symbol section is not valid UTF-8: %q", out)
	}
	for _, want := range []string{"你好World", "型"} {
		if !strings.Contains(out, want) {
			t.Fatalf("symbol section missing %q: %q", want, out)
		}
	}
}

func TestRepoSymbolsNoSymbolsDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	writeSymFile(t, root, "notes.txt", "hello\n")
	if syms := extractRepoSymbols(root, []string{"notes.txt"}); len(syms) != 0 {
		t.Fatalf("non-Go file yielded symbols: %+v", syms)
	}
	out := strings.Join(repoMapSymbolSection(nil, nil, repoMapSymbolTokenBudget), "\n")
	if !strings.Contains(out, "symbols (0):") || !strings.Contains(out, "(none)") {
		t.Fatalf("empty section = %q", out)
	}

	registry := New(root, true)
	got, err := registry.Run(context.Background(), "repo_map", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "symbols (0):") {
		t.Fatalf("repo map missing symbol header: %q", got)
	}

	// A syntactically broken Go file must not panic either. go/parser may
	// recover a placeholder declaration, so only the absence of a panic is
	// asserted here.
	writeSymFile(t, root, "broken.go", "package broken\nfunc (\n")
	_ = repoMapSymbolSection(extractRepoSymbols(root, []string{"broken.go"}), nil, repoMapSymbolTokenBudget)
}

func TestRepoMapSymbolSectionIsAdditiveAndToggleable(t *testing.T) {
	root := t.TempDir()
	writeSymFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeSymFile(t, root, "svc.go", "package main\n\nfunc RunService() {}\n")

	registry := New(root, true)
	out, err := registry.Run(context.Background(), "repo_map", `{"mention":"RunService"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dirs:", "files (2):", "main.go:3 [entry]", "symbols (", "[func] svc.go:3 RunService"} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo map = %q, missing %q", out, want)
		}
	}

	plain, err := registry.Run(context.Background(), "repo_map", `{"symbols":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "symbols (") || !strings.Contains(plain, "files (2):") {
		t.Fatalf("symbols:false should keep only the legacy view, got %q", plain)
	}
}
