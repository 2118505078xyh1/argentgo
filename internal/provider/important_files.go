package provider

import "strings"

// IsImportantProjectFile reports whether path names a high-value project file
// that a repository map should keep even when a symbol/token budget would
// otherwise crowd it out (the aider "important files first" guarantee).
//
// It is a pure classification helper: no I/O, no environment, no mutation of
// its input. Matching is case-insensitive and looks only at the file's base
// name, so directory prefixes (including CJK path segments and either slash
// style) do not affect the result. It is meant to be called by the repo-map
// tool when deciding which entries to pin into the map; keeping the policy here
// lets that decision stay consistent across frontends without importing config.
//
// Two rules are combined:
//   - an exact set of well-known manifest / metadata file names;
//   - a set of "families" matched on a base-name prefix followed by a name
//     separator (., -, _), covering README/license/Dockerfile/Makefile variants.
//
// CI workflow files under a .github/workflows directory are always important
// regardless of their individual file name.
func IsImportantProjectFile(path string) bool {
	// Normalize both slash styles so Windows and POSIX paths classify the same.
	normalized := strings.ReplaceAll(path, "\\", "/")
	lower := strings.ToLower(normalized)

	// CI workflows are important by location, not by file name.
	if strings.HasPrefix(lower, ".github/workflows/") ||
		strings.Contains(lower, "/.github/workflows/") {
		return true
	}

	// Extract the base name, ignoring trailing slashes and "."/".." segments.
	trimmed := strings.TrimSuffix(normalized, "/")
	if trimmed == "" {
		return false
	}
	base := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		base = trimmed[i+1:]
	}
	if base == "" || base == "." || base == ".." {
		return false
	}
	lowerBase := strings.ToLower(base)

	if _, ok := importantProjectFiles[lowerBase]; ok {
		return true
	}
	for _, family := range importantFileFamilies {
		if matchesFileFamily(lowerBase, family) {
			return true
		}
	}
	return false
}

// matchesFileFamily reports whether base is prefix itself or starts with
// prefix followed by a name separator. The separator requirement avoids false
// positives such as "licenseplate.go" for the "license" family while keeping
// README.md, README-zh.md, Dockerfile.dev and LICENSE-MIT.
func matchesFileFamily(base, prefix string) bool {
	if base == prefix {
		return true
	}
	if !strings.HasPrefix(base, prefix) {
		return false
	}
	rest := base[len(prefix):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case '.', '-', '_':
		return true
	default:
		return false
	}
}

// importantProjectFiles is the exact, case-folded set of manifest, metadata and
// guidance file names worth pinning into a repository map. Read-only.
var importantProjectFiles = map[string]struct{}{
	// Go
	"go.mod": {}, "go.sum": {}, "go.work": {}, "go.work.sum": {},
	// Rust
	"cargo.toml": {}, "cargo.lock": {},
	// JavaScript / TypeScript
	"package.json": {}, "package-lock.json": {}, "npm-shrinkwrap.json": {},
	"yarn.lock": {}, "pnpm-lock.yaml": {}, "bun.lock": {}, "bun.lockb": {},
	"tsconfig.json": {},
	// Python
	"pyproject.toml": {}, "requirements.txt": {}, "setup.py": {}, "setup.cfg": {},
	"pipfile": {}, "pipfile.lock": {}, "poetry.lock": {}, "tox.ini": {},
	// JVM
	"pom.xml": {}, "build.gradle": {}, "build.gradle.kts": {},
	"settings.gradle": {}, "settings.gradle.kts": {}, "gradlew": {},
	// Ruby / PHP
	"gemfile": {}, "gemfile.lock": {}, "composer.json": {}, "composer.lock": {},
	// Docs and agent guidance
	"agents.md": {}, "claude.md": {}, "contributing.md": {}, "changelog.md": {},
	"code_of_conduct.md": {},
	// Repository / tool dotfile config
	".gitignore": {}, ".gitattributes": {}, ".gitmodules": {},
	".dockerignore": {}, ".editorconfig": {}, ".env.example": {},
	".golangci.yml": {}, ".golangci.yaml": {},
	// Build systems
	"meson.build": {}, "build.bazel": {}, "workspace": {}, "workspace.bazel": {},
	// Task runners
	"justfile": {}, "taskfile.yml": {}, "taskfile.yaml": {},
	// CI config (location-based .github/workflows is handled above)
	".gitlab-ci.yml": {}, ".travis.yml": {}, "jenkinsfile": {},
	"azure-pipelines.yml": {}, "appveyor.yml": {}, "circle.yml": {},
}

// importantFileFamilies are base-name prefixes (already lowercase) whose
// variants are all considered important. See matchesFileFamily.
var importantFileFamilies = []string{
	"readme", "license", "licence", "copying", "dockerfile",
	"docker-compose", "compose", "makefile", "gnumakefile", "cmakelists",
}
