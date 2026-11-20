package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeResource creates a file at rel inside dir (creating parent directories)
// and returns its byte size.
func writeResource(t *testing.T, dir, rel, content string) int64 {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return int64(len(content))
}

func resourcePaths(resources []ResourceInfo) []string {
	paths := make([]string, len(resources))
	for i, r := range resources {
		paths[i] = r.Path
	}
	return paths
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListSkillResourcesListsAuxiliaryFiles(t *testing.T) {
	dir := t.TempDir()
	writeResource(t, dir, "SKILL.md", "---\nname: deploy\n---\nbody")
	deploySize := writeResource(t, dir, "scripts/deploy.sh", "#!/bin/sh\necho deploy")
	apiSize := writeResource(t, dir, "references/api.md", "# API")
	readmeSize := writeResource(t, dir, "README.md", "notes")

	got := ListSkillResources(dir, 1<<20)
	want := []ResourceInfo{
		{Path: "README.md", Size: readmeSize},
		{Path: "references/api.md", Size: apiSize},
		{Path: "scripts/deploy.sh", Size: deploySize},
	}
	if len(got) != len(want) {
		t.Fatalf("resources = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resource[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListSkillResourcesBudget(t *testing.T) {
	dir := t.TempDir()
	writeResource(t, dir, "SKILL.md", "doc")
	aSize := writeResource(t, dir, "a.txt", strings.Repeat("a", 10))
	bSize := writeResource(t, dir, "b.txt", strings.Repeat("b", 20))
	writeResource(t, dir, "big.bin", strings.Repeat("x", 100))
	cSize := writeResource(t, dir, "c.txt", strings.Repeat("c", 30))

	cases := []struct {
		name     string
		maxBytes int64
		want     []string
	}{
		{"negative budget", -1, nil},
		{"zero budget", 0, nil},
		{"below smallest file", 5, nil},
		{"exactly smallest file", aSize, []string{"a.txt"}},
		{"second file does not fit", 15, []string{"a.txt"}},
		{"two files fit", aSize + bSize, []string{"a.txt", "b.txt"}},
		{"oversized file skipped, later file fits", aSize + bSize + cSize, []string{"a.txt", "b.txt", "c.txt"}},
		{"oversized file skipped with tight budget", 35, []string{"a.txt", "b.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourcePaths(ListSkillResources(dir, tc.maxBytes))
			if !equalStrings(got, tc.want) {
				t.Errorf("paths = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListSkillResourcesSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	writeResource(t, dir, "SKILL.md", "doc")
	writeResource(t, dir, ".hidden.txt", "x")
	writeResource(t, dir, ".git/config", "x")
	writeResource(t, dir, "sub/.secret", "x")
	writeResource(t, dir, "keep.txt", "x")
	writeResource(t, dir, "sub/ok.bin", "x")

	got := resourcePaths(ListSkillResources(dir, 1<<20))
	want := []string{"keep.txt", "sub/ok.bin"}
	if !equalStrings(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

func TestListSkillResourcesSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skill")
	writeResource(t, dir, "SKILL.md", "doc")
	insideSize := writeResource(t, dir, "real.txt", "inside")
	writeResource(t, dir, "sub/target.bin", "target")
	writeResource(t, root, "outside.txt", "outside")

	links := []struct{ from, to string }{
		{"link-inside.txt", "real.txt"},                          // stays inside -> listed
		{"link-outside.txt", filepath.Join("..", "outside.txt")}, // escapes -> skipped
		{"link-dir", "sub"},                                      // points at a directory -> skipped
	}
	for _, link := range links {
		if err := os.Symlink(link.to, filepath.Join(dir, link.from)); err != nil {
			t.Skipf("symlinks are not available on this host: %v", err)
		}
	}

	got := ListSkillResources(dir, 1<<20)
	want := []ResourceInfo{
		{Path: "link-inside.txt", Size: insideSize},
		{Path: "real.txt", Size: insideSize},
		{Path: "sub/target.bin", Size: 6},
	}
	if len(got) != len(want) {
		t.Fatalf("resources = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resource[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListSkillResourcesCJKPaths(t *testing.T) {
	dir := t.TempDir()
	writeResource(t, dir, "SKILL.md", "doc")
	deploySize := writeResource(t, dir, "脚本/部署.sh", "echo hi")
	docSize := writeResource(t, dir, "参考资料/说明.md", "文档")
	writeResource(t, dir, "脚本/.隐藏.txt", "x")

	got := ListSkillResources(dir, 1<<20)
	want := []ResourceInfo{
		{Path: "参考资料/说明.md", Size: docSize},
		{Path: "脚本/部署.sh", Size: deploySize},
	}
	if len(got) != len(want) {
		t.Fatalf("resources = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resource[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Sorting must be deterministic in path order.
	paths := resourcePaths(got)
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	if !equalStrings(paths, sorted) {
		t.Errorf("paths are not sorted: %q", paths)
	}
}

func TestListSkillResourcesInvalidDirectory(t *testing.T) {
	cases := []struct {
		name string
		dir  string
	}{
		{"missing", filepath.Join(t.TempDir(), "nope")},
		{"regular file as dir", func() string {
			p := filepath.Join(t.TempDir(), "file.txt")
			writeResource(t, filepath.Dir(p), filepath.Base(p), "x")
			return p
		}()},
		{"empty dir", t.TempDir()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ListSkillResources(tc.dir, 1<<20); len(got) != 0 {
				t.Errorf("resources = %#v, want none", got)
			}
		})
	}
}
