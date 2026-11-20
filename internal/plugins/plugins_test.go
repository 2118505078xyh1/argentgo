package plugins

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile creates a file, creating parents as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeBundle creates a bundle directory with a manifest at relative.
func writeBundle(t *testing.T, parent, name, relative, body string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	writeFile(t, filepath.Join(dir, filepath.FromSlash(relative)), body)
	return dir
}

// homeDir points os.UserHomeDir at a scratch directory for the test's duration.
func homeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestLoadManifestForEveryLayout(t *testing.T) {
	const body = `{
		"name":"documents",
		"version":"0.2.2",
		"description":"Word tools",
		"author":"ada",
		"homepage":"https://example.test/documents",
		"skills":["skills"],
		"commands":["commands/open.md"],
		"tools":["word"],
		"hooks":{"paths":["hooks.json"]},
		"mcpServers":{"docs":{"command":"node","args":["server.mjs"],"env":{"A":"B"}}}
	}`
	for _, relative := range ManifestPaths {
		relative := relative
		t.Run(relative, func(t *testing.T) {
			dir := t.TempDir()
			manifestPath := filepath.Join(dir, filepath.FromSlash(relative))
			writeFile(t, manifestPath, body)
			if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
				t.Fatalf("create skills: %v", err)
			}
			for _, created := range []string{"commands/open.md", "hooks.json"} {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(created)), "")
			}

			manifest, err := LoadManifest(dir)
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			if manifest.Name != "documents" || manifest.Version != "0.2.2" {
				t.Fatalf("got %q %q", manifest.Name, manifest.Version)
			}
			if manifest.Author != "ada" || manifest.Homepage != "https://example.test/documents" {
				t.Fatalf("author/homepage: %q %q", manifest.Author, manifest.Homepage)
			}
			if manifest.Path != manifestPath {
				t.Fatalf("manifest path = %q, want %q", manifest.Path, manifestPath)
			}
			if got := manifest.ResolvePath("commands/open.md"); got != filepath.Join(dir, "commands", "open.md") {
				t.Fatalf("resolved command = %q", got)
			}
			server, ok := manifest.MCPServers["docs"]
			if !ok || server.Command != "node" || len(server.Args) != 1 || server.Env["A"] != "B" {
				t.Fatalf("mcp server = %+v", server)
			}
			if problems := manifest.Validate(); len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}
		})
	}
}

func TestParseManifestAcceptsAuthorObjectAndDefaults(t *testing.T) {
	dir := t.TempDir()
	manifest, err := ParseManifest([]byte(`{"author":{"name":"grace","email":"g@example.test"}}`), dir, "")
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if manifest.Name != filepath.Base(dir) {
		t.Fatalf("name = %q, want %q", manifest.Name, filepath.Base(dir))
	}
	if manifest.Version != "0.0.0" {
		t.Fatalf("version = %q", manifest.Version)
	}
	if manifest.Author != "grace" {
		t.Fatalf("author = %q", manifest.Author)
	}
}

func TestParseManifestKeepsInlineHooksAndStringPaths(t *testing.T) {
	manifest, err := ParseManifest([]byte(`{
		"name":"alpha",
		"skills":"skills",
		"commands":["a.md",""],
		"hooks":{"PreToolUse":[{"matcher":"bash","hooks":["echo hi"]}]}
	}`), t.TempDir(), "")
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(manifest.Skills) != 1 || manifest.Skills[0] != "skills" {
		t.Fatalf("skills = %v", manifest.Skills)
	}
	if len(manifest.Commands) != 1 || manifest.Commands[0] != "a.md" {
		t.Fatalf("commands = %v", manifest.Commands)
	}
	if len(manifest.Hooks.Paths) != 0 {
		t.Fatalf("hook paths = %v", manifest.Hooks.Paths)
	}
	var inline map[string]json.RawMessage
	if err := json.Unmarshal(manifest.Hooks.Inline, &inline); err != nil {
		t.Fatalf("inline hooks: %v", err)
	}
	if _, ok := inline["PreToolUse"]; !ok {
		t.Fatalf("inline hooks lost PreToolUse: %s", manifest.Hooks.Inline)
	}
}

func TestParseManifestRejectsUnsafeOrBrokenInput(t *testing.T) {
	cases := map[string]string{
		"traversal name": `{"name":"../escape"}`,
		"absolute name":  `{"name":"/tmp/evil"}`,
		"reserved name":  `{"name":"CON.txt"}`,
		"not an object":  `["nope"]`,
		"invalid json":   `{`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if _, err := ParseManifest([]byte(body), t.TempDir(), ""); err == nil {
				t.Fatalf("expected an error for %s", label)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"alpha", "my-plugin_v1.2", "scope@plugin", "插件"}
	for _, name := range good {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	bad := []string{"", ".", "..", "../escape", "sub/dir", "sub\\dir", "C:evil", "with space", "name;semi", "nul", "CON.txt", "com1", "lpt9.log", "a\x00b", "a\nb"}
	for _, name := range bad {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", name)
		}
	}
}

func TestValidateVersion(t *testing.T) {
	good := []string{"0.0.0", "1.2.3", "10.20.30-beta.1", "1.0.0+build.5"}
	for _, version := range good {
		if err := ValidateVersion(version); err != nil {
			t.Errorf("ValidateVersion(%q) = %v, want nil", version, err)
		}
	}
	bad := []string{"", "1", "1.2", "1.2.3.4", "a.b.c", "1.2.x", "1.2.3-", "1.2.3+", "1.2.3-rc.1_bad", "1.2.-3"}
	for _, version := range bad {
		if err := ValidateVersion(version); err == nil {
			t.Errorf("ValidateVersion(%q) = nil, want an error", version)
		}
	}
}

func TestValidateReportsMissingFilesAndServerProblems(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.json"), `{
		"name":"alpha",
		"version":"1.0",
		"skills":["skills"],
		"commands":["commands/missing.md"],
		"hooks":{"paths":["../../outside.json"]},
		"mcpServers":{"broken":{"args":["x"]}}
	}`)
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	problems := manifest.Validate()
	joined := ""
	for _, problem := range problems {
		joined += problem.String() + "\n"
	}
	for _, want := range []string{"version", "skills path", "commands path", "escapes the bundle", "mcp server"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}
}

func TestDiscoverPrefersTheProjectRootAndReportsDuplicates(t *testing.T) {
	home := homeDir(t)
	project := t.TempDir()

	writeBundle(t, ProjectPluginsDir(project), "alpha", "plugin.json", `{"name":"alpha","description":"first"}`)
	homePlugins := filepath.Join(home, ".argentgo", "plugins")
	writeBundle(t, homePlugins, "alpha", ".zcode-plugin/plugin.json", `{"name":"alpha","description":"second"}`)
	writeBundle(t, homePlugins, "beta", ".claude-plugin/plugin.json", `{"name":"beta"}`)

	bundles, problems, err := Discover(project)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("got %d bundles: %+v", len(bundles), bundles)
	}
	if bundles[0].Name != "alpha" || bundles[0].Manifest.Description != "first" {
		t.Fatalf("precedence broken: %+v", bundles[0])
	}
	found := false
	for _, problem := range problems {
		if problem.Plugin == "alpha" && strings.Contains(problem.Message, "duplicate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no duplicate problem reported: %v", problems)
	}
	if problems := Validate(bundles); len(problems) != 0 {
		t.Fatalf("merged set is not valid: %v", problems)
	}
}

func TestDiscoverFindsNestedPluginsDirectory(t *testing.T) {
	homeDir(t)
	project := t.TempDir()
	writeBundle(t, filepath.Join(ProjectPluginsDir(project), "marketplace", "plugins"), "gamma", ".codex-plugin/plugin.json", `{"name":"gamma"}`)

	bundles, problems, err := Discover(project)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(bundles) != 1 || bundles[0].Name != "gamma" {
		t.Fatalf("bundles = %+v", bundles)
	}
}

func TestResolveRespectsStateFile(t *testing.T) {
	homeDir(t)
	project := t.TempDir()
	pluginsDir := ProjectPluginsDir(project)
	writeBundle(t, pluginsDir, "alpha", "plugin.json", `{"name":"alpha"}`)
	writeBundle(t, pluginsDir, "beta", "plugin.json", `{"name":"beta"}`)

	// No state file: everything is enabled.
	bundles, err := Resolve(project)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("default discovery = %d bundles, want 2", len(bundles))
	}

	if err := SetEnabled(pluginsDir, "beta", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	bundles, err = Resolve(project)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(bundles) != 1 || bundles[0].Name != "alpha" || !bundles[0].Enabled {
		t.Fatalf("after disabling beta: %+v", bundles)
	}

	state, exists, err := LoadState(pluginsDir)
	if err != nil || !exists {
		t.Fatalf("LoadState: %v exists=%v", err, exists)
	}
	if len(state.Enabled) != 1 || state.Enabled[0] != "alpha" {
		t.Fatalf("state = %+v", state)
	}
}

func TestSetEnabledSeedsStateWithInstalledBundles(t *testing.T) {
	pluginsDir := t.TempDir()
	writeBundle(t, pluginsDir, "alpha", "plugin.json", `{"name":"alpha"}`)
	writeBundle(t, pluginsDir, "beta", "plugin.json", `{"name":"beta"}`)

	if err := SetEnabled(pluginsDir, "alpha", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	state, exists, err := LoadState(pluginsDir)
	if err != nil || !exists {
		t.Fatalf("LoadState: %v exists=%v", err, exists)
	}
	if len(state.Enabled) != 1 || state.Enabled[0] != "beta" {
		t.Fatalf("state = %+v, want only beta", state)
	}
}

func TestMergeBuildsWiringFromEnabledBundles(t *testing.T) {
	root := t.TempDir()
	alphaDir := writeBundle(t, root, "alpha", "plugin.json", `{
		"name":"alpha",
		"skills":["skills"],
		"commands":["commands/open.md"],
		"hooks":{"paths":["hooks.json"]},
		"mcpServers":{"shared":{"command":"alpha-server","args":["--stdio"],"env":{"K":"V"}}}
	}`)
	betaDir := writeBundle(t, root, "beta", "plugin.json", `{
		"name":"beta",
		"skills":["skills"],
		"mcpServers":{"shared":{"command":"beta-server"}}
	}`)

	alpha, err := LoadManifest(alphaDir)
	if err != nil {
		t.Fatalf("LoadManifest alpha: %v", err)
	}
	beta, err := LoadManifest(betaDir)
	if err != nil {
		t.Fatalf("LoadManifest beta: %v", err)
	}

	wiring := Merge([]Bundle{
		{Name: alpha.Name, Dir: alpha.Root, Manifest: alpha, Enabled: true},
		{Name: beta.Name, Dir: beta.Root, Manifest: beta, Enabled: false},
	})

	if len(wiring.MCP) != 1 {
		t.Fatalf("mcp = %+v", wiring.MCP)
	}
	spec := wiring.MCP[0]
	if spec.Plugin != "alpha" || spec.Command != "alpha-server" || spec.Env["K"] != "V" || spec.Dir != alpha.Root {
		t.Fatalf("spec = %+v", spec)
	}
	if len(wiring.Skills) != 1 || wiring.Skills[0] != filepath.Join(alphaDir, "skills") {
		t.Fatalf("skills = %v", wiring.Skills)
	}
	if len(wiring.Commands) != 1 || wiring.Commands[0] != filepath.Join(alphaDir, "commands", "open.md") {
		t.Fatalf("commands = %v", wiring.Commands)
	}
	if len(wiring.HooksPaths) != 1 || wiring.HooksPaths[0] != filepath.Join(alphaDir, "hooks.json") {
		t.Fatalf("hooks = %v", wiring.HooksPaths)
	}
}

func TestInstallFromDirectoryEnablesTheBundle(t *testing.T) {
	pluginsDir := t.TempDir()
	source := writeBundle(t, t.TempDir(), "alpha", ".zcode-plugin/plugin.json", `{"name":"alpha","version":"1.2.3"}`)
	writeFile(t, filepath.Join(source, "skills", "SKILL.md"), "# skill\n")

	bundle, err := Install(source, pluginsDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !bundle.Enabled || bundle.Dir != filepath.Join(pluginsDir, "alpha") {
		t.Fatalf("bundle = %+v", bundle)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha", "skills", "SKILL.md")); err != nil {
		t.Fatalf("skills not copied: %v", err)
	}
	state, exists, err := LoadState(pluginsDir)
	if err != nil || !exists || len(state.Enabled) != 1 || state.Enabled[0] != "alpha" {
		t.Fatalf("state = %+v exists=%v err=%v", state, exists, err)
	}

	if _, err := Install(source, pluginsDir); err == nil {
		t.Fatal("second install must fail without Force")
	}
	if _, err := InstallWith(source, pluginsDir, InstallOptions{Force: true}); err != nil {
		t.Fatalf("forced install: %v", err)
	}

	removed, err := Uninstall("alpha", pluginsDir)
	if err != nil || !removed {
		t.Fatalf("Uninstall: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin directory survived uninstall: %v", err)
	}
}

func TestCopyTreeSkipsSymlinks(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(source, "plugin.json"), `{"name":"alpha"}`)
	writeFile(t, filepath.Join(source, "normal.txt"), "normal")
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret")

	// A link to another directory: following it would copy files from outside
	// the bundle. A self link: following it would recurse forever.
	if err := os.Symlink(outside, filepath.Join(source, "outside")); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if err := os.Symlink(source, filepath.Join(source, "loop")); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "alpha")
	done := make(chan error, 1)
	go func() { done <- copyTree(source, dest) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copyTree: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyTree did not return; it is following a directory symlink")
	}

	if _, err := os.Stat(filepath.Join(dest, "normal.txt")); err != nil {
		t.Fatalf("regular file not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("link to outside directory was copied, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "loop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("self-referential link was copied, err=%v", err)
	}
	if err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "secret.txt" {
			t.Errorf("file from outside the bundle was copied: %s", p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk destination: %v", err)
	}
}

// makeZip writes a zip archive with the given member names and contents.
func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, content := range entries {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := member.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}

func TestInstallFromZipWithTopLevelDirectory(t *testing.T) {
	pluginsDir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "alpha.zip")
	makeZip(t, archive, map[string]string{
		"alpha/plugin.json":     `{"name":"alpha","version":"2.0.0"}`,
		"alpha/skills/SKILL.md": "# skill\n",
	})

	bundle, err := Install(archive, pluginsDir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if bundle.Name != "alpha" || bundle.Version != "2.0.0" {
		t.Fatalf("bundle = %+v", bundle)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha", "skills", "SKILL.md")); err != nil {
		t.Fatalf("zip content missing: %v", err)
	}
	// The staging directory must not survive a successful install.
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		t.Fatalf("read plugins dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			t.Fatalf("staging directory %q left behind", entry.Name())
		}
	}
}

func TestInstallRejectsZipSlip(t *testing.T) {
	for _, entry := range []string{"../escape.txt", "alpha/../../escape.txt", "/absolute.txt", "C:/windows.txt"} {
		entry := entry
		t.Run(entry, func(t *testing.T) {
			pluginsDir := filepath.Join(t.TempDir(), "plugins")
			archive := filepath.Join(t.TempDir(), "evil.zip")
			makeZip(t, archive, map[string]string{
				entry:               "owned",
				"alpha/plugin.json": `{"name":"alpha"}`,
			})

			_, err := Install(archive, pluginsDir)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("Install error = %v, want ErrUnsafeArchive", err)
			}
			outside := filepath.Join(filepath.Dir(pluginsDir), "escape.txt")
			if _, statErr := os.Stat(outside); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("archive escaped to %s", outside)
			}
			if _, statErr := os.Stat(filepath.Join(pluginsDir, "alpha")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected archive was partially installed")
			}
		})
	}
}

func TestInstallRejectsArchiveWithoutManifest(t *testing.T) {
	pluginsDir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "empty.zip")
	makeZip(t, archive, map[string]string{"readme.md": "nothing here"})
	if _, err := Install(archive, pluginsDir); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("Install error = %v, want ErrNoManifest", err)
	}
}

func TestUninstallRejectsUnsafeNames(t *testing.T) {
	pluginsDir := t.TempDir()
	writeBundle(t, pluginsDir, "alpha", "plugin.json", `{"name":"alpha"}`)
	for _, name := range []string{"", ".", "..", "../alpha", "nested/alpha", "C:alpha", "con"} {
		if _, err := Uninstall(name, pluginsDir); err == nil {
			t.Errorf("Uninstall(%q) accepted an unsafe name", name)
		}
	}
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha")); err != nil {
		t.Fatalf("alpha was removed: %v", err)
	}
}
