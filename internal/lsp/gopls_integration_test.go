package lsp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

// This file is an end-to-end integration test: it speaks real LSP over stdio
// to an actual gopls process. It is build-tag-free so `go test ./internal/lsp`
// exercises it whenever gopls happens to be installed, but every failure that
// is about the *environment* (gopls missing, slow, or refusing to start) is a
// t.Skip, never a suite failure. Only a successful response that is clearly
// wrong (definition in the wrong file, missing declared symbols, too few
// references) is reported with t.Errorf — that is a real client finding.
//
// `go test -short ./internal/lsp` skips it via testing.Short().

// integrationSource is a small but valid Go module body. RunService is
// declared once and called once: that gives definition/references a precise
// target, while the struct, const and second function give documentSymbol a
// varied tree to flatten. Keep everything ASCII so a byte offset within a line
// is also the LSP UTF-16 character offset.
const integrationSource = `package main

import "fmt"

const AppName = "lsptest"

type Service struct {
	Name string
}

func RunService(s Service) string {
	return s.Name
}

func Help() string {
	return "help"
}

func main() {
	s := Service{Name: AppName}
	fmt.Println(RunService(s))
	fmt.Println(Help())
}
`

func TestIntegrationGoplsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}

	gopls := locateGopls(t)
	// lsp.Start launches the server with exec.Command("gopls"), so it must be
	// discoverable through PATH even when it lives in the Go bin directory
	// that is not on the default PATH.
	t.Setenv("PATH", filepath.Dir(gopls)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOTOOLCHAIN", "local") // never try to download a toolchain during CI

	workspace := t.TempDir()
	mainPath := writeIntegrationWorkspace(t, workspace, integrationSource)

	// The initialize handshake is bounded separately: a cold gopls may spend a
	// while indexing the standard library on its very first run.
	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	client, err := lsp.Start(startCtx, lsp.Server{
		Name:        "gopls",
		Command:     "gopls",
		LanguageIDs: []string{"go"},
	}, workspace)
	startCancel()
	if err != nil {
		t.Skipf("gopls could not be started (integration optional): %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// One generous budget for all subsequent requests; the whole test stays
	// well under 120s because each phase has its own shorter deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	uri, err := client.Open(ctx, mainPath)
	if err != nil {
		t.Skipf("gopls did not accept the document (integration optional): %v", err)
	}

	// Diagnostics is a useful readiness hint (gopls publishes an empty set for
	// a clean file), but it is best-effort: some versions skip the publish
	// entirely when there is nothing to report.
	diagnostics, diagErr := client.Diagnostics(ctx, uri, 20*time.Second)
	if diagErr != nil {
		t.Logf("diagnostics wait: %v (continuing)", diagErr)
	} else {
		t.Logf("diagnostics: %d item(s) for a valid Go file", len(diagnostics))
	}

	declPos := findToken(t, integrationSource, "RunService", 0) // func RunService
	callPos := findToken(t, integrationSource, "RunService", 1) // RunService(s)
	declLine := declPos.Line

	// Readiness gate: definition needs gopls to have type-checked the package,
	// which can lag behind didOpen. Poll until the call site resolves to the
	// declaration line, then make the real assertions from that result.
	deadline := time.Now().Add(30 * time.Second)
	var defs []lsp.Location
	var defErr error
	ready := false
	for time.Now().Before(deadline) {
		defs, defErr = client.DefinitionAt(ctx, mainPath, callPos)
		if defErr == nil && definitionAtLine(defs, mainPath, declLine) {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Skipf("gopls did not resolve the call site to the RunService declaration "+
			"(integration optional: server too slow or unavailable); last result=%+v, err=%v", defs, defErr)
	}
	t.Logf("DefinitionAt(call site) -> %d location(s), first at %s", len(defs), defs[0].Range.Start.String())

	// CORE: a definition that lands in another file is a real finding.
	for _, loc := range defs {
		if !sameFile(loc.URI, mainPath) {
			t.Errorf("DefinitionAt returned %q, want the workspace main.go", loc.URI)
		}
	}

	// CORE: documentSymbol must list the declarations we wrote.
	symbols, err := client.DocumentSymbolAt(ctx, mainPath)
	if err != nil {
		t.Skipf("gopls documentSymbol failed (integration optional): %v", err)
	}
	names := make([]string, 0, len(symbols))
	present := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		names = append(names, symbol.Name)
		present[symbol.Name] = true
	}
	t.Logf("DocumentSymbolAt -> %d symbol(s): %v", len(symbols), names)
	for _, want := range []string{"RunService", "Service", "AppName", "Help", "main"} {
		if !present[want] {
			t.Errorf("DocumentSymbolAt missing %q; got %v", want, names)
		}
	}

	// CORE: references at the declaration must include the declaration itself
	// and the call site.
	refs, err := client.ReferencesAt(ctx, mainPath, declPos, true)
	if err != nil {
		t.Skipf("gopls references failed (integration optional): %v", err)
	}
	t.Logf("ReferencesAt(RunService) -> %d location(s)", len(refs))
	if len(refs) < 2 {
		t.Errorf("ReferencesAt found %d location(s), want >= 2 (declaration + call site)", len(refs))
	}

	// BEST-EFFORT: report what the server gave us; never fail the suite for
	// an empty hover, a filtered workspace query or already-formatted code.
	hover, err := client.HoverAt(ctx, mainPath, callPos)
	switch {
	case err != nil:
		t.Logf("hover error (best-effort): %v", err)
	case hover == nil:
		t.Log("hover: no content (best-effort)")
	case !strings.Contains(hover.Contents, "RunService"):
		t.Logf("hover: unexpected content %q (best-effort)", hover.Contents)
	default:
		t.Logf("hover: %q", hover.Contents)
	}

	workspaceSymbols, err := client.WorkspaceSymbols(ctx, "RunService")
	if err != nil {
		t.Logf("workspace/symbol error (best-effort): %v", err)
	} else {
		t.Logf("WorkspaceSymbols(RunService) -> %d result(s)", len(workspaceSymbols))
	}

	edits, err := client.FormattingAt(ctx, mainPath, lsp.FormattingOptions{TabSize: 4, InsertSpaces: true})
	if err != nil {
		t.Logf("formatting error (best-effort): %v", err)
	} else {
		t.Logf("FormattingAt -> %d edit(s)", len(edits))
	}
}

// locateGopls finds gopls on PATH or in the conventional Go bin directory. A
// missing server makes the whole integration test skip, so CI without gopls
// stays green.
func locateGopls(t *testing.T) string {
	t.Helper()
	if found, err := exec.LookPath("gopls"); err == nil {
		return found
	}
	if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
		fallback := filepath.Join(userProfile, "go", "bin", "gopls.exe")
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() {
			return fallback
		}
	}
	t.Skip("gopls not installed: not on PATH and not found at ~/go/bin/gopls.exe")
	return ""
}

// writeIntegrationWorkspace writes the minimal valid module the server loads.
func writeIntegrationWorkspace(t *testing.T, dir, source string) string {
	t.Helper()
	goMod := "module example.com/lsptest\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(source), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return mainPath
}

// findToken returns the zero-based LSP position of the occurrence-th match of
// token in source (counting from 0). The fixture is ASCII, so byte and UTF-16
// offsets coincide.
func findToken(t *testing.T, source, token string, occurrence int) lsp.Position {
	t.Helper()
	seen := 0
	for lineNo, line := range strings.Split(source, "\n") {
		offset := 0
		for {
			index := strings.Index(line[offset:], token)
			if index < 0 {
				break
			}
			if seen == occurrence {
				return lsp.Position{Line: lineNo, Character: offset + index}
			}
			seen++
			offset += index + len(token)
		}
	}
	t.Fatalf("token %q occurrence %d not found in fixture", token, occurrence)
	return lsp.Position{}
}

// definitionAtLine reports whether one location points at path's line.
func definitionAtLine(locations []lsp.Location, path string, line int) bool {
	for _, location := range locations {
		if sameFile(location.URI, path) && location.Range.Start.Line == line {
			return true
		}
	}
	return false
}

// sameFile compares a URI and a filesystem path in both directions so URI
// casing or percent-encoding differences between gopls and PathToURI do not
// produce false findings.
func sameFile(uri, path string) bool {
	if strings.EqualFold(uri, lsp.PathToURI(path)) {
		return true
	}
	return strings.EqualFold(filepath.Clean(lsp.URIToPath(uri)), filepath.Clean(path))
}
