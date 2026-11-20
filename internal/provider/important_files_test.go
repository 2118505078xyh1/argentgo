package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestIsImportantProjectFileManifestsAndMetadata(t *testing.T) {
	// One representative from every ecosystem plus the guidance files that a
	// repo map should pin regardless of symbol budget.
	for _, path := range []string{
		"go.mod", "go.sum", "go.work", "Cargo.toml", "Cargo.lock",
		"package.json", "package-lock.json", "yarn.lock", "tsconfig.json",
		"pyproject.toml", "requirements.txt", "setup.py", "poetry.lock",
		"pom.xml", "build.gradle", "gradlew", "Gemfile", "composer.json",
		"AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md",
		".gitignore", ".editorconfig", ".env.example", ".golangci.yml",
		"Justfile", "Taskfile.yml", "CMakeLists.txt", "meson.build",
		".gitlab-ci.yml", "Jenkinsfile", "azure-pipelines.yml",
	} {
		if !provider.IsImportantProjectFile(path) {
			t.Errorf("IsImportantProjectFile(%q) = false, want true", path)
		}
	}
}

func TestIsImportantProjectFileFamilies(t *testing.T) {
	// README/license/Dockerfile/Makefile variants share a prefix plus a name
	// separator, so every spelling and extension is recognized.
	for _, path := range []string{
		"README", "README.md", "readme.rst", "README.zh-CN.md", "README_OLD.txt",
		"LICENSE", "LICENSE.md", "LICENSE-MIT", "LICENCE.txt", "COPYING.LESSER",
		"Dockerfile", "Dockerfile.dev", "dockerfile.prod",
		"docker-compose.yml", "compose.yaml", "composer.json",
		"Makefile", "makefile.am", "GNUmakefile", "cmakelists.txt",
	} {
		if !provider.IsImportantProjectFile(path) {
			t.Errorf("IsImportantProjectFile(%q) = false, want true", path)
		}
	}
}

func TestIsImportantProjectFileIsCaseInsensitive(t *testing.T) {
	for _, path := range []string{
		"GO.MOD", "Go.Mod", "CARGO.TOML", "Cargo.TOML", "PACKAGE.JSON",
		"README.MD", "DOCKERFILE", "MAKEFILE", "AGENTS.MD", ".GITIGNORE",
	} {
		if !provider.IsImportantProjectFile(path) {
			t.Errorf("IsImportantProjectFile(%q) = false, want true", path)
		}
	}
}

func TestIsImportantProjectFileIgnoresDirectoryAndSlashStyle(t *testing.T) {
	for _, path := range []string{
		"internal/provider/go.mod",
		"internal\\provider\\go.mod", // Windows separators
		"deploy/Dockerfile.prod",
		"docs\\README.md",
		"C:\\work\\project\\Cargo.toml", // drive-qualified path
		"a/b/c/./go.mod",
		"nested/.github/workflows/release.yml",
		".github/workflows/ci.yml",
	} {
		if !provider.IsImportantProjectFile(path) {
			t.Errorf("IsImportantProjectFile(%q) = false, want true", path)
		}
	}
}

func TestIsImportantProjectFileAcceptsCJKPaths(t *testing.T) {
	// CJK directory segments are irrelevant; the base name decides.
	for _, path := range []string{
		"文档/README.md",
		"项目\\配置\\go.mod",
		"源码/组件/Cargo.toml",
		"测试/.github/workflows/构建.yml",
	} {
		if !provider.IsImportantProjectFile(path) {
			t.Errorf("IsImportantProjectFile(%q) = false, want true", path)
		}
	}
}

func TestIsImportantProjectFileRejectsOrdinaryFiles(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"go source", "main.go"},
		{"test source", "internal/provider/validate_test.go"},
		{"nested ordinary file", "docs/notes.txt"},
		{"image", "assets/logo.png"},
		{"lock-like but not manifest", "random.lock"},
		{"license lookalike", "licenseplate.go"}, // prefix without separator
		{"readme lookalike", "readmeGenerator.go"},
		{"cjk ordinary file", "文档/普通文件.go"},
		{"cjk ordinary nested", "源码/组件/实现.go"},
		{"empty", ""},
		{"root", "/"},
		{"dot", "."},
		{"dotdot", ".."},
		{"trailing slash on dir", "internal/provider/"},
	}
	for _, tc := range cases {
		if provider.IsImportantProjectFile(tc.path) {
			t.Errorf("IsImportantProjectFile(%q) [%s] = true, want false", tc.path, tc.name)
		}
	}
}
