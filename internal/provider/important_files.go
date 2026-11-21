package provider

import "strings"

// IsImportantProjectFile 报告 path 是否命名一个高价值项目文件，即使符号/token 预算本会把它挤掉，
// 仓库地图也应保留它（aider 的"重要文件优先"保证）。
//
// 它是纯分类辅助函数：无 I/O、无环境、不修改输入。匹配不区分大小写且只看文件的基础名，
// 因此目录前缀（包括 CJK 路径段和任一斜杠风格）不影响结果。它供 repo-map 工具在决定哪些条目钉入地图时调用；
// 把策略放在这里使该决策在前端保持一致而无需导入 config。
//
// 组合两条规则：精确的知名清单/元数据文件名集合；以及按基础名前缀后跟名称
// 分隔符（.、-、_）匹配的"家族"集合，覆盖 README/license/Dockerfile/Makefile 变体。
// .github/workflows 目录下的 CI 工作流文件始终重要，无论其单个文件名如何。
func IsImportantProjectFile(path string) bool {
	// 规范化两种斜杠风格，使 Windows 和 POSIX 路径分类一致。
	normalized := strings.ReplaceAll(path, "\\", "/")
	lower := strings.ToLower(normalized)

	// CI 工作流因位置而重要，而非文件名。
	if strings.HasPrefix(lower, ".github/workflows/") ||
		strings.Contains(lower, "/.github/workflows/") {
		return true
	}

	// 提取基础名，忽略末尾斜杠和 "."/".." 段。
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

// matchesFileFamily 报告 base 是否就是 prefix 或以 prefix 后跟名称分隔符开头。
// 分隔符要求避免误报，例如把 "licenseplate.go" 当作 "license" 家族，同时保留
// README.md、README-zh.md、Dockerfile.dev 和 LICENSE-MIT。
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

// importantProjectFiles 是值得钉入仓库地图的清单、元数据和指导文件名的精确大小写折叠集合。只读。
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
	// 文档和 agent 指导
	"agents.md": {}, "claude.md": {}, "contributing.md": {}, "changelog.md": {},
	"code_of_conduct.md": {},
	// 仓库/工具 dotfile 配置
	".gitignore": {}, ".gitattributes": {}, ".gitmodules": {},
	".dockerignore": {}, ".editorconfig": {}, ".env.example": {},
	".golangci.yml": {}, ".golangci.yaml": {},
	// 构建系统
	"meson.build": {}, "build.bazel": {}, "workspace": {}, "workspace.bazel": {},
	// 任务运行器
	"justfile": {}, "taskfile.yml": {}, "taskfile.yaml": {},
	// CI 配置（基于位置的 .github/workflows 在上面处理）
	".gitlab-ci.yml": {}, ".travis.yml": {}, "jenkinsfile": {},
	"azure-pipelines.yml": {}, "appveyor.yml": {}, "circle.yml": {},
}

// importantFileFamilies 是其变体都被视为重要的基础名前缀（已小写）。见 matchesFileFamily。
var importantFileFamilies = []string{
	"readme", "license", "licence", "copying", "dockerfile",
	"docker-compose", "compose", "makefile", "gnumakefile", "cmakelists",
}
