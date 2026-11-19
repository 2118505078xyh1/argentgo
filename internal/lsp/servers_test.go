package lsp

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathToURIRoundTrip(t *testing.T) {
	base := t.TempDir()
	cases := []string{
		filepath.Join(base, "src", "main.go"),
		filepath.Join(base, "a b", "file name.rs"),
		filepath.Join(base, "ünïcode", "x.ts"),
	}
	for _, path := range cases {
		uri := PathToURI(path)
		if !strings.HasPrefix(uri, "file://") {
			t.Fatalf("PathToURI(%q) = %q, want a file URI", path, uri)
		}
		if strings.Contains(uri, " ") {
			t.Fatalf("PathToURI(%q) = %q, spaces must be encoded", path, uri)
		}
		round := URIToPath(uri)
		if filepath.Clean(round) != filepath.Clean(path) {
			t.Fatalf("round trip %q -> %q -> %q", path, uri, round)
		}
	}
}

func TestPathToURIOnWindowsStylePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows path shape")
	}
	if got := PathToURI(`C:\src\main.rs`); got != "file:///C:/src/main.rs" {
		t.Fatalf("PathToURI = %q", got)
	}
	if got := URIToPath("file:///C:/src/main.rs"); got != `C:\src\main.rs` {
		t.Fatalf("URIToPath = %q", got)
	}
}

func TestLanguageForPath(t *testing.T) {
	cases := map[string]string{
		"main.go":           "go",
		"lib.rs":            "rust",
		"app.ts":            "typescript",
		"app.tsx":           "typescriptreact",
		"app.js":            "javascript",
		"app.jsx":           "javascriptreact",
		"tool.py":           "python",
		"types.pyi":         "python",
		"notes.xyz":         "xyz",
		"Makefile":          "plaintext",
		"src\\nested\\a.go": "go",
	}
	for path, want := range cases {
		if got := LanguageForPath(path); got != want {
			t.Errorf("LanguageForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRegistryServesTheDefaultLanguages(t *testing.T) {
	registry := NewRegistry()
	cases := map[string]string{
		"main.go":  "gopls",
		"lib.rs":   "rust-analyzer",
		"app.tsx":  "typescript",
		"main.py":  "pyright",
		"notes.md": "",
	}
	for path, want := range cases {
		server, ok := registry.ForPath(path)
		if want == "" {
			if ok {
				t.Errorf("ForPath(%q) = %q, want no server", path, server.Name)
			}
			continue
		}
		if !ok || server.Name != want {
			t.Errorf("ForPath(%q) = %q ok=%v, want %q", path, server.Name, ok, want)
		}
	}
	if _, ok := registry.Lookup("go"); !ok {
		t.Error("gopls is not registered for go")
	}
}

func TestRegistryDefaultsMatchTheReferenceCommandLines(t *testing.T) {
	registry := NewRegistry()
	rust, _ := registry.Lookup("rust")
	if len(rust.Args) != 0 {
		t.Fatalf("rust-analyzer args = %v, want none", rust.Args)
	}
	typescript, _ := registry.Lookup("typescript")
	if len(typescript.Args) != 1 || typescript.Args[0] != "--stdio" {
		t.Fatalf("typescript args = %v, want --stdio", typescript.Args)
	}
	python, _ := registry.Lookup("python")
	if python.Command != "pyright-langserver" || len(python.Args) != 1 || python.Args[0] != "--stdio" {
		t.Fatalf("python server = %+v", python)
	}
	if goServer, _ := registry.Lookup("go"); goServer.Command != "gopls" || len(goServer.Args) != 0 {
		t.Fatalf("go server = %+v", goServer)
	}
}

func TestRegistryAcceptsACustomServer(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Server{Name: "mine", Command: "my-ls", Args: []string{"--stdio"}, LanguageIDs: []string{"mine"}})
	server, ok := registry.ForPath("a.mine")
	if !ok || server.Command != "my-ls" {
		t.Fatalf("custom server = %+v ok=%v", server, ok)
	}
	if len(registry.Languages()) < len(DefaultServers()) {
		t.Fatalf("languages = %v", registry.Languages())
	}
}

func TestHumanPositionConvertsToOneBased(t *testing.T) {
	position := Human(5, 9)
	if position.Line != 4 || position.Character != 8 {
		t.Fatalf("Human(5,9) = %+v", position)
	}
	if got := position.String(); got != "5:9" {
		t.Fatalf("position string = %q", got)
	}
	if Human(0, 0) != (Position{}) {
		t.Fatalf("Human(0,0) = %+v", Human(0, 0))
	}
}
