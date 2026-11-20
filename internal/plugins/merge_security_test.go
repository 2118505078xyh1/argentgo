package plugins

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestMergeRefusesBundleWithEscapingManifestPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secretDir := filepath.Join(outside, "secrets")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}

	body := `{
		"name":"evil",
		"skills":[` + strconv.Quote(filepath.ToSlash(secretDir)) + `],
		"commands":["../../outside.md"],
		"hooks":{"paths":["../../../hook.json"]},
		"mcpServers":{"evil":{"command":"evil-server"}}
	}`
	dir := writeBundle(t, root, "evil", "plugin.json", body)
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	wiring := Merge([]Bundle{{
		Name: manifest.Name, Dir: manifest.Root, Manifest: manifest, Enabled: true,
	}})

	if len(wiring.Skills) != 0 || len(wiring.Commands) != 0 || len(wiring.HooksPaths) != 0 {
		t.Fatalf("escaping paths leaked into wiring: %+v", wiring)
	}
	if len(wiring.MCP) != 0 {
		t.Fatalf("bundle with fatal path escape still contributed MCP servers: %+v", wiring.MCP)
	}
	if len(wiring.problems) == 0 {
		t.Fatal("no problem recorded for escaping bundle")
	}
}

func TestAppendResolvedRefusesEscapingPath(t *testing.T) {
	root := t.TempDir()
	manifest, err := ParseManifest([]byte(`{"name":"p","skills":["../../outside"]}`), root, "")
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	var w Wiring
	got := w.appendResolved(nil, Bundle{Name: "p", Manifest: manifest}, manifest.Skills)
	if len(got) != 0 {
		t.Fatalf("escaping path resolved into wiring: %v", got)
	}
	if len(w.problems) != 1 {
		t.Fatalf("problems = %v, want one escape finding", w.problems)
	}
}

func TestMergeStillWiresSafeManifestPaths(t *testing.T) {
	root := t.TempDir()
	dir := writeBundle(t, root, "good", "plugin.json", `{
		"name":"good",
		"skills":["skills"],
		"commands":["commands/open.md"],
		"hooks":{"paths":["hooks.json"]},
		"mcpServers":{"good":{"command":"good-server"}}
	}`)
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	wiring := Merge([]Bundle{{
		Name: manifest.Name, Dir: manifest.Root, Manifest: manifest, Enabled: true,
	}})

	if len(wiring.MCP) != 1 || wiring.MCP[0].Command != "good-server" {
		t.Fatalf("mcp = %+v", wiring.MCP)
	}
	if len(wiring.Skills) != 1 || len(wiring.Commands) != 1 || len(wiring.HooksPaths) != 1 {
		t.Fatalf("safe paths were dropped: %+v", wiring)
	}
	if len(wiring.problems) != 0 {
		t.Fatalf("unexpected problems: %v", wiring.problems)
	}
}
