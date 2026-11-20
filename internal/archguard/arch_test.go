// Package archguard encodes the dependency rules from the repository's
// AGENTS.md as an automated test, so architectural drift is caught by
// `go test ./internal/archguard` instead of by code review alone.
//
// The test is deliberately stdlib-only (go/parser + AST walking), adds no new
// module, and never compiles the packages it inspects: it parses imports only.
// It therefore works on any host, including cross-compilation targets.
package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedExternalModule is the single third-party module AGENTS.md permits,
// and only inside native/ (the desktop shell). Everything under internal/
// must be stdlib-only. Sub-packages of this module (e.g. mygo/ui) are allowed
// because they share the same module prefix.
//
// Allow-list rationale:
//   - stdlib imports (no "." in the first path segment) are allowed everywhere;
//   - imports of this repository's own module (<module>/...) are NOT third
//     party and are allowed under internal/ as intra-repo references;
//   - the only external exception is mygo, and only under native/;
//   - the archguard test itself imports stdlib only;
//   - all .go files are checked, including _test.go files, build-tag variants,
//     and generated/mock sources -- there are no silent exceptions.
const allowedExternalModule = "github.com/egoist/mygo"

// internalLayerRules encodes package-scoped layering rules (AGENTS.md):
//   - tools never imports agent;
//   - agent is a library layer and never imports the app wirer;
//   - llm is the wire layer and must stay below agent/tools/app.
//
// Imports are matched against "<module path> + forbiddenImport".
var internalLayerRules = []struct {
	pkgRel          string // package directory, relative to repo root, slash-separated
	forbiddenImport string // suffix appended to the module path
	rule            string // human-readable rule, used in failures
}{
	{"internal/tools", "/internal/agent", "internal/tools must not import internal/agent"},
	{"internal/agent", "/internal/app", "internal/agent must not import internal/app (app is the top-level wirer)"},
	{"internal/llm", "/internal/agent", "internal/llm is the wire layer and must not import internal/agent"},
	{"internal/llm", "/internal/tools", "internal/llm is the wire layer and must not import internal/tools"},
	{"internal/llm", "/internal/app", "internal/llm must not import internal/app"},
}

func TestInternalDependencyRules(t *testing.T) {
	root := findRepoRoot(t)
	mod := readModulePath(t, root)
	prefix := mod + "/"

	for _, abs := range goFiles(t, filepath.Join(root, "internal")) {
		rel := relSlash(t, root, abs)
		pkgDir := filepath.ToSlash(filepath.Dir(rel))

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", rel, err)
			continue
		}

		for _, imp := range importPaths(file) {
			// Rule (a): internal packages are stdlib + own-module only.
			if isExternal(imp) && !strings.HasPrefix(imp, prefix) && imp != mod {
				t.Errorf("%s imports forbidden %q (rule: internal packages are stdlib-only; the only external module %q is allowed, and only in native/)",
					rel, imp, allowedExternalModule)
			}

			// Rule (b): no cgo anywhere. ("C" has no dot, so rule (a) ignores it.)
			if imp == "C" {
				t.Errorf("%s imports forbidden %q (rule: no cgo anywhere; everything must cross-compile)", rel, imp)
			}

			// Rules (c), (d), (e): package layering.
			for _, r := range internalLayerRules {
				if pkgDir == r.pkgRel && imp == mod+r.forbiddenImport {
					t.Errorf("%s imports forbidden %q (rule: %s)", rel, imp, r.rule)
				}
			}
		}
	}

	// native/ is the single external exception. Enforce that its external
	// imports stay within the one allow-listed module, and that cgo is absent.
	if st, err := os.Stat(filepath.Join(root, "native")); err == nil && st.IsDir() {
		for _, abs := range goFiles(t, filepath.Join(root, "native")) {
			rel := relSlash(t, root, abs)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
			if err != nil {
				t.Errorf("parse %s: %v", rel, err)
				continue
			}
			for _, imp := range importPaths(file) {
				if imp == "C" {
					t.Errorf("%s imports forbidden %q (rule: no cgo anywhere; everything must cross-compile)", rel, imp)
				}
				if isExternal(imp) && imp != allowedExternalModule && !strings.HasPrefix(imp, allowedExternalModule+"/") {
					t.Errorf("%s imports forbidden %q (rule: the only external module allowed in native/ is %q)",
						rel, imp, allowedExternalModule)
				}
			}
		}
	}
}

// goFiles returns every .go file under dir, recursively, without filtering by
// build tags or file name (strict: _test.go and platform variants are included).
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// importPaths returns the unquoted import paths declared by file. Quotes are
// stripped with strings.Trim so no extra package (strconv) is needed.
func importPaths(file *ast.File) []string {
	var out []string
	for _, spec := range file.Imports {
		if spec.Path != nil {
			out = append(out, strings.Trim(spec.Path.Value, "`\""))
		}
	}
	return out
}

// isExternal reports whether imp denotes a non-stdlib module, i.e. its first
// path segment contains a dot (github.com/..., golang.org/..., gopkg.in/...).
func isExternal(imp string) bool {
	first := imp
	if i := strings.Index(imp, "/"); i >= 0 {
		first = imp[:i]
	}
	return strings.Contains(first, ".")
}

// findRepoRoot walks up from the test's working directory to the go.mod file.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above the working directory")
		}
		dir = parent
	}
}

// readModulePath discovers the module path from go.mod rather than hardcoding
// it, so the guard keeps working if the module is renamed.
func readModulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"`")
		}
	}
	t.Fatal("no module directive found in go.mod")
	return ""
}

func relSlash(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("rel %s: %v", path, err)
	}
	return filepath.ToSlash(rel)
}
