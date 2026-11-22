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
	"sort"
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

	// 从仓库根开始扫描，使 cmd/ 也受规则 (a)(b) 约束。
	// native/ 是唯一允许外部依赖的目录，由下面单独的
	// 白名单校验；若在这里也按规则 (a) 检查会误杀 mygo。
	for _, abs := range goFiles(t, root) {
		rel := relSlash(t, root, abs)
		if strings.HasPrefix(rel, "native/") {
			continue
		}
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

			// 规则 (c)、(d)、(e)：包分层。用目录边界匹配，
			// 防止 internal/toolsfoo 之类前缀相近的包被误命中。
			for _, r := range internalLayerRules {
				if (pkgDir == r.pkgRel || strings.HasPrefix(pkgDir, r.pkgRel+"/")) && imp == mod+r.forbiddenImport {
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

// TestNoInternalImportCycles 用静态解析检测自有模块内的包级 import 环。
// 与 go build 的编译期检查互补：它能在交叉编译目标上工作，且不依赖
// 构建标签组合。刻意跳过 _test.go——外部测试包（package xxx_test）
// 会反向 import 被测包，形成大量并非真实依赖环的假阳性。
func TestNoInternalImportCycles(t *testing.T) {
	root := findRepoRoot(t)
	mod := readModulePath(t, root)
	prefix := mod + "/"

	// graph: 包目录 -> 它 import 的自有模块包集合。
	graph := map[string]map[string]bool{}
	for _, abs := range goFiles(t, root) {
		rel := relSlash(t, root, abs)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", rel, err)
			continue
		}
		for _, imp := range importPaths(file) {
			// 只关心自有模块内的边；外部模块不是本仓库能解开的环。
			if !strings.HasPrefix(imp, prefix) {
				continue
			}
			dep := strings.TrimPrefix(imp, prefix)
			// 自环（包 import 自身）不是跨包依赖环，跳过。
			if dep == pkg {
				continue
			}
			if graph[pkg] == nil {
				graph[pkg] = map[string]bool{}
			}
			graph[pkg][dep] = true
		}
	}

	// 三色 DFS：0=未访问（白），1=在栈中（灰），2=已完成（黑）。
	// 遇到灰节点即找到一条回边，从它在栈中的位置到栈顶加上回边
	// 构成完整环路。
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var cycles []string

	var visit func(string)
	visit = func(n string) {
		color[n] = gray
		stack = append(stack, n)

		deps := make([]string, 0, len(graph[n]))
		for d := range graph[n] {
			deps = append(deps, d)
		}
		sort.Strings(deps) // 稳定输出，便于复现和比对
		for _, d := range deps {
			switch color[d] {
			case gray:
				idx := 0
				for i, s := range stack {
					if s == d {
						idx = i
						break
					}
				}
				cycle := append(append([]string{}, stack[idx:]...), d)
				cycles = append(cycles, strings.Join(cycle, " -> "))
			case white:
				visit(d)
			}
		}

		stack = stack[:len(stack)-1]
		color[n] = black
	}

	nodes := make([]string, 0, len(graph))
	for n := range graph {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if color[n] == white {
			visit(n)
		}
	}

	for _, c := range cycles {
		t.Errorf("import cycle: %s", c)
	}
}

// goFiles 递归返回 dir 下的每个 .go 文件，不按
// 构建标签或文件名过滤（严格：包括 _test.go 和平台变体）。
// 版本控制、构建产物与依赖目录整体剪枝，避免扫描无关文件。
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "dist", "vendor", "node_modules":
				return filepath.SkipDir
			}
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
