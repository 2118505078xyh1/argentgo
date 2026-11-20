// Package archguard 把仓库 AGENTS.md 中的依赖规则编码为
// 自动化测试，使架构漂移由
// `go test ./internal/archguard` 捕获，而非仅靠代码审查。
//
// 该测试刻意只用标准库（go/parser + AST 遍历），不添加新
// 模块，且从不编译它检查的包：它只解析 import。
// 因此它能在任何主机上工作，包括交叉编译目标。
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

// allowedExternalModule 是 AGENTS.md 允许的唯一第三方模块，
// 且仅在 native/（桌面壳）内。internal/ 下的一切
// 必须只用标准库。本模块的子包（如 mygo/ui）被允许，
// 因为它们共享同一模块前缀。
//
// 允许列表理由：
//   - 标准库 import（第一段路径无 "."）到处都允许；
//   - 本仓库自有模块的 import（<module>/...）不是第三方
//     且在 internal/ 下作为仓库内引用允许；
//   - 唯一的外部例外是 mygo，且仅在 native/ 下；
//   - archguard 测试本身只 import 标准库；
//
// 允许列表理由：标准库 import（第一段路径无 "."）到处都允许；本仓库自有模块的 import（<module>/...）不是第三方且在 internal/ 下作为仓库内引用允许；唯一的外部例外是 mygo，且仅在 native/ 下；archguard 测试本身只 import 标准库；所有 .go 文件都被检查，包括 _test.go、构建标签变体和生成/mock 源码 —— 没有静默例外。
//
//	且生成/mock 源码 —— 没有静默例外。
const allowedExternalModule = "github.com/egoist/mygo"

// internalLayerRules 编码包级分层规则（AGENTS.md）：
//
//   - tools 从不 import agent；
//
//     （它依赖 ToolRunner 接口，而非 tools 包）；
//
//   - llm 是线上层且必须保持在 agent/tools/app 之下。
//
// import 与 "<module path> + forbiddenImport" 匹配。
var internalLayerRules = []struct {
	pkgRel          string // package directory, relative to repo root, slash-separated
	forbiddenImport string // suffix appended to the module path
	rule            string // human-readable rule, used in failures
}{
	{"internal/tools", "/internal/agent", "internal/tools must not import internal/agent"},
	{"internal/agent", "/internal/app", "internal/agent must not import internal/app (app is the top-level wirer)"},
	{"internal/agent", "/internal/tools", "agent depends on ToolRunner interface, not tools package"},
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
			// 规则 (a)：内部包只用标准库 + 自有模块。
			if isExternal(imp) && !strings.HasPrefix(imp, prefix) && imp != mod {
				t.Errorf("%s imports forbidden %q (rule: internal packages are stdlib-only; the only external module %q is allowed, and only in native/)",
					rel, imp, allowedExternalModule)
			}

			// 规则 (b)：任何地方都不用 cgo（"C" 无点，因此规则 (a) 忽略它）。
			if imp == "C" {
				t.Errorf("%s imports forbidden %q (rule: no cgo anywhere; everything must cross-compile)", rel, imp)
			}

			// 规则 (c)、(d)、(e)：包分层。
			for _, r := range internalLayerRules {
				if pkgDir == r.pkgRel && imp == mod+r.forbiddenImport {
					t.Errorf("%s imports forbidden %q (rule: %s)", rel, imp, r.rule)
				}
			}
		}
	}

	// native/ 是唯一的外部例外。强制其外部
	// import 停留在允许列表中的单一模块内，且没有 cgo。
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

// goFiles 递归返回 dir 下的每个 .go 文件，不按
// 构建标签或文件名过滤（严格：包括 _test.go 和平台变体）。
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

// importPaths 返回文件声明的未加引号的 import 路径。引号用
// strings.Trim 去掉，因此不需要额外包（strconv）。
func importPaths(file *ast.File) []string {
	var out []string
	for _, spec := range file.Imports {
		if spec.Path != nil {
			out = append(out, strings.Trim(spec.Path.Value, "`\""))
		}
	}
	return out
}

// isExternal 报告 imp 是否表示非标准库模块，即其第一段
func isExternal(imp string) bool {
	first := imp
	if i := strings.Index(imp, "/"); i >= 0 {
		first = imp[:i]
	}
	return strings.Contains(first, ".")
}

// findRepoRoot 从测试工作目录向上走到 go.mod 文件。
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

// readModulePath 从 go.mod 发现模块路径而非硬编码，
// 使模块重命名时守卫仍工作。
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
