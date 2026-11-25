package extension

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"example.com/argentgo/internal/plugins"
)

// makeBundleDir 造一个 bundle 目录。清单里的路径是相对 bundle 根的（真实约定），
// Manifest.Root 必须设为该目录，plugins.Merge 的 appendResolved 才能解析且通过越界检查。
func makeBundleDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPluginCommandSourcesPerBundlePrefix(t *testing.T) {
	root := t.TempDir()
	d1 := makeBundleDir(t, root, "alpha")
	d2 := makeBundleDir(t, root, "beta")
	cmdFile := "hello.md" // 相对 bundle 根
	for _, dir := range []string{d1, d2} {
		if err := os.WriteFile(filepath.Join(dir, cmdFile), []byte("---\ndescription: hi\n---\nHi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	b1 := plugins.Bundle{Name: "alpha", Dir: d1, Enabled: true, Manifest: plugins.Manifest{Name: "alpha", Root: d1, Commands: []string{cmdFile}}}
	b2 := plugins.Bundle{Name: "beta", Dir: d2, Enabled: true, Manifest: plugins.Manifest{Name: "beta", Root: d2, Commands: []string{cmdFile}}}
	sources := PluginCommandSources([]plugins.Bundle{b1, b2})
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want 2", sources)
	}
	// 逐包 Merge：每个包名绑定到自己的来源目录（单文件归一到所在目录）。
	prefixes := map[string]string{}
	for _, s := range sources {
		prefixes[s.Prefix] = s.Root
	}
	if prefixes["alpha"] != d1 || prefixes["beta"] != d2 {
		t.Fatalf("per-bundle prefix binding wrong: %+v", prefixes)
	}
}

func TestPluginHookPathsPerBundle(t *testing.T) {
	root := t.TempDir()
	d1 := makeBundleDir(t, root, "alpha")
	d2 := makeBundleDir(t, root, "beta")
	hook := "hook.json"
	for _, dir := range []string{d1, d2} {
		if err := os.WriteFile(filepath.Join(dir, hook), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b1 := plugins.Bundle{Name: "alpha", Dir: d1, Enabled: true, Manifest: plugins.Manifest{Name: "alpha", Root: d1, Hooks: plugins.Hooks{Paths: []string{hook}}}}
	b2 := plugins.Bundle{Name: "beta", Dir: d2, Enabled: true, Manifest: plugins.Manifest{Name: "beta", Root: d2, Hooks: plugins.Hooks{Paths: []string{hook}}}}
	paths := PluginHookPaths([]plugins.Bundle{b1, b2})
	want := []string{filepath.Join(d1, hook), filepath.Join(d2, hook)}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("hook paths = %v, want %v", paths, want)
	}
}
