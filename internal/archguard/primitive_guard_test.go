package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// primitiveBaseline 是引入本门禁时的「存量快照」commit（当前 HEAD）。
// 所有在该 commit 已存在的导出函数一律视为存量、不回溯豁免；
// 只有相对它新增的导出函数才需要「有生产调用方」或「登记理由」。
// 把它固化为一个 commit 而非 HEAD~N，是为了让门禁随提交持续生效，
// 而不是每次提交后自动把刚加的原语洗成存量。
const primitiveBaseline = "a8f63f5aa93def1f80c98c6e433fa80f7a3067b9"

// incrementalDirs 是 git 基线不可用时的退化方案：只扫描这里明确列出的
// 目录（相对仓库根）。门禁引入时清单里只有本包自身——它仅含测试文件与
// 清单数据文件，没有导出生产函数，因此退化模式下也稳定为空结果。
// 若今后在缺少 git 历史的环境中新增原语，需把对应目录补进此清单。
var incrementalDirs = []string{
	"internal/archguard",
}

// primitiveSymbol 是一个「新增导出函数」的定位信息。
type primitiveSymbol struct {
	Name      string // 导出函数名（含方法名）
	RelPath   string // 相对仓库根的斜杠路径
	StartLine int    // 声明起始行（用于排除自身递归调用）
	EndLine   int    // 声明结束行
}

// primitiveViolation 是一条门禁违规。
type primitiveViolation struct {
	Symbol  string
	RelPath string
	Line    int
}

// hasBuildConstraint 判断源码是否带构建标签（//go:build 或 // +build）。
// 带标签的文件是平台/条件特定的，其「新增原语」不做自动判定，
// 避免交叉编译时因某平台缺失调用方而产生误报。
func hasBuildConstraint(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "package ") {
			break // 构建标签必须出现在 package 之前
		}
		if strings.HasPrefix(t, "//go:build") || strings.HasPrefix(t, "// +build") {
			return true
		}
	}
	return false
}

// exportedFuncSites 解析源码并返回所有顶层导出函数/方法的声明位置。
// 接口方法（InterfaceType 里的 Field）和函数别名（var F = func(){}）
// 都不是 FuncDecl，因此天然被跳过，不会被当成新增原语。
func exportedFuncSites(src []byte, relPath string) []primitiveSymbol {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, src, 0)
	if err != nil {
		return nil // 解析失败时保守跳过，由正常编译流程报错
	}
	var out []primitiveSymbol
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || !ast.IsExported(fd.Name.Name) {
			continue
		}
		out = append(out, primitiveSymbol{
			Name:      fd.Name.Name,
			RelPath:   relPath,
			StartLine: fset.Position(fd.Pos()).Line,
			EndLine:   fset.Position(fd.End()).Line,
		})
	}
	return out
}

// collectGoFiles 递归收集 dir 下的 .go 文件，剪掉版本控制/构建产物目录。
func collectGoFiles(dir string) ([]string, error) {
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
			return nil
		}
		if strings.HasSuffix(info.Name(), ".go") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// relPath 返回 abs 相对 root 的斜杠路径；失败时退回原路径。
func relPath(root, abs string) string {
	r, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// gitOutput 在 root 下执行 git 子命令并返回标准输出。
func gitOutput(root string, args ...string) (string, error) {
	full := append([]string{"-C", root}, args...)
	out, err := exec.Command("git", full...).Output()
	return string(out), err
}

// gitBaselineAvailable 报告 git 是否可用且基线 commit 确实存在。
// 浅克隆/无 git 历史时返回 false，触发目录清单退化方案。
func gitBaselineAvailable(root string) bool {
	_, err := exec.Command("git", "-C", root, "rev-parse", "--verify", primitiveBaseline+"^{commit}").Output()
	return err == nil
}

// incrementFiles 返回本次视作「增量」的 Go 文件（斜杠相对路径）。
// 第二个返回值表示是否走 git 基线方案（false 表示退化到目录清单）。
func incrementFiles(t *testing.T, root string) ([]string, bool) {
	t.Helper()
	if gitBaselineAvailable(root) {
		seen := map[string]bool{}
		var files []string
		add := func(blob string) {
			for _, line := range strings.Split(blob, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || seen[line] {
					continue
				}
				seen[line] = true
				files = append(files, filepath.ToSlash(line))
			}
		}
		// 已跟踪的改动（含尚未提交的新增/修改/删除，删除文件后续读取会跳过）。
		if out, err := gitOutput(root, "diff", "--name-only", primitiveBaseline, "--", "*.go"); err == nil {
			add(out)
		}
		// 未跟踪的新文件也要检查，否则「新增原语」在 git add 前会被漏掉。
		if out, err := gitOutput(root, "ls-files", "--others", "--exclude-standard", "--", "*.go"); err == nil {
			add(out)
		}
		return files, true
	}

	// 退化方案：git 历史不可用时只扫描明确清单。
	var files []string
	for _, dir := range incrementalDirs {
		absDir := filepath.Join(root, filepath.FromSlash(dir))
		absFiles, err := collectGoFiles(absDir)
		if err != nil {
			t.Logf("退化清单目录 %s 不可读：%v", dir, err)
			continue
		}
		for _, abs := range absFiles {
			files = append(files, relPath(root, abs))
		}
	}
	return files, false
}

// baselineExportedNames 返回文件在基线 commit 中已有的导出函数名集合。
// 文件在基线不存在（新文件）时返回空集合，使其全部导出函数都算新增。
func baselineExportedNames(root, rel string) map[string]bool {
	names := map[string]bool{}
	out, err := exec.Command("git", "-C", root, "show", primitiveBaseline+":"+rel).Output()
	if err != nil {
		return names
	}
	for _, s := range exportedFuncSites(out, rel) {
		names[s.Name] = true
	}
	return names
}

// newPrimitivesFromIncrement 在增量文件中找出「新增导出函数」：
//   - 跳过 _test.go（测试函数天然导出，且不算生产原语）；
//   - 跳过带构建标签的文件（平台特定，见 hasBuildConstraint）；
//   - 跳过已删除/不可读的文件（并发改动下常见）；
//   - git 方案下用「当前导出名 − 基线导出名」判定新增，
//     避免把仅为移动位置的旧函数误判为新原语。
func newPrimitivesFromIncrement(t *testing.T, root string) ([]primitiveSymbol, string) {
	t.Helper()
	files, usingGit := incrementFiles(t, root)
	var out []primitiveSymbol
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if hasBuildConstraint(src) {
			continue
		}
		sites := exportedFuncSites(src, rel)
		if usingGit {
			base := baselineExportedNames(root, rel)
			kept := sites[:0]
			for _, s := range sites {
				if !base[s.Name] {
					kept = append(kept, s)
				}
			}
			sites = kept
		}
		out = append(out, sites...)
	}
	mode := "git 基线增量"
	if !usingGit {
		mode = "目录清单退化（git 历史/基线不可用）"
	}
	return out, mode
}

// callExprName 提取调用表达式的目标名字，支持：
//   - 直接调用 Foo(...)；
//   - 选择器调用 pkg.Foo(...) / x.Foo(...)（含接口方法、链式调用的中间环节）；
//   - 泛型调用 Foo[T](...) 及其括号包裹形式。
//
// 保守起见只取名字，不解析真实包/类型，宁可多认调用方也不误报。
func callExprName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr:
		return callExprName(f.X)
	case *ast.IndexListExpr:
		return callExprName(f.X)
	case *ast.ParenExpr:
		return callExprName(f.X)
	}
	return ""
}

// stringLitValue 返回字符串字面量的解码值。
func stringLitValue(lit *ast.BasicLit) (string, bool) {
	if lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// hasProductionCaller 扫描全部非测试 .go 文件，判断 sym 是否有生产调用方。
// 带构建标签的文件仍参与调用方扫描（多认调用方 = 少误报）。
// 两处保守处理：
//   - 声明自身的递归调用不算调用方；
//   - 名字出现在字符串字面量中视为已接线（覆盖 RPC/反射按名注册）。
func hasProductionCaller(root string, sym primitiveSymbol) bool {
	files, err := collectGoFiles(root)
	if err != nil {
		return false
	}
	for _, abs := range files {
		rel := relPath(root, abs)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			continue
		}
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			if found {
				return false
			}
			switch x := n.(type) {
			case *ast.CallExpr:
				if callExprName(x.Fun) != sym.Name {
					return true
				}
				pos := fset.Position(x.Pos())
				if rel == sym.RelPath && pos.Line >= sym.StartLine && pos.Line <= sym.EndLine {
					return true // 自身递归，不算外部调用方
				}
				found = true
			case *ast.BasicLit:
				if v, ok := stringLitValue(x); ok && v == sym.Name {
					found = true // RPC/反射按名字注册
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// isPrimitiveAllowed 判断 sym 是否已在清单登记。理由为空的条目不生效，
// 防止「先占个位、理由以后补」绕过门禁。
func isPrimitiveAllowed(sym primitiveSymbol, allow []primitiveAllowEntry) bool {
	for _, e := range allow {
		if strings.TrimSpace(e.Reason) == "" {
			continue
		}
		if e.Symbol == sym.Name || e.Symbol == sym.RelPath+":"+sym.Name {
			return true
		}
	}
	return false
}

// checkNewPrimitives 是门禁的纯逻辑核心：每个新增导出函数要么有生产调用方，
// 要么在原语清单登记「保留未启用 + 理由」，否则产生违规。
func checkNewPrimitives(root string, syms []primitiveSymbol, allow []primitiveAllowEntry) []primitiveViolation {
	var out []primitiveViolation
	for _, sym := range syms {
		if isPrimitiveAllowed(sym, allow) {
			continue
		}
		if hasProductionCaller(root, sym) {
			continue
		}
		out = append(out, primitiveViolation{Symbol: sym.Name, RelPath: sym.RelPath, Line: sym.StartLine})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RelPath != out[j].RelPath {
			return out[i].RelPath < out[j].RelPath
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// TestNewPrimitivesAreWired 是实际门禁：只在增量上生效，不回溯存量。
func TestNewPrimitivesAreWired(t *testing.T) {
	root := findRepoRoot(t)
	syms, mode := newPrimitivesFromIncrement(t, root)
	t.Logf("原语门禁增量判定模式：%s；本次新增导出函数 %d 个", mode, len(syms))
	for _, v := range checkNewPrimitives(root, syms, primitiveAllowlist) {
		t.Errorf("%s:%d: 新增导出原语 %q 未接线：既无生产调用方，也未在原语清单登记。"+
			"修复建议：① 在非测试文件为它添加生产调用；② 若确属「保留未启用」，"+
			"在 internal/archguard/primitive_allowlist.go 登记 {Symbol: %q, Reason: \"保留原因与接线计划\"}。",
			v.RelPath, v.Line, v.Symbol, v.RelPath+":"+v.Symbol)
	}
}

// ---- 门禁自身测试：用临时仓库注入「新增未接线原语」等场景 ----

func newTempRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPrimitiveGuardRejectsUnwiredNewPrimitive 注入一个无调用方的新原语，门禁应失败。
func TestPrimitiveGuardRejectsUnwiredNewPrimitive(t *testing.T) {
	root := newTempRepo(t, map[string]string{
		"internal/foo/widget.go": "package foo\n\nfunc NewWidget() {}\n",
	})
	syms := []primitiveSymbol{{Name: "NewWidget", RelPath: "internal/foo/widget.go", StartLine: 3, EndLine: 3}}
	violations := checkNewPrimitives(root, syms, nil)
	if len(violations) != 1 || violations[0].Symbol != "NewWidget" {
		t.Fatalf("期望 1 条未接线违规，得到 %+v", violations)
	}
}

// TestPrimitiveGuardAllowsRegisteredPrimitive 同一原语登记理由后应通过。
func TestPrimitiveGuardAllowsRegisteredPrimitive(t *testing.T) {
	root := newTempRepo(t, map[string]string{
		"internal/foo/widget.go": "package foo\n\nfunc NewWidget() {}\n",
	})
	syms := []primitiveSymbol{{Name: "NewWidget", RelPath: "internal/foo/widget.go", StartLine: 3, EndLine: 3}}
	allow := []primitiveAllowEntry{{
		Symbol: "internal/foo/widget.go:NewWidget",
		Reason: "为下一阶段插件 API 预留，将通过 RPC 反射接入",
	}}
	if v := checkNewPrimitives(root, syms, allow); len(v) != 0 {
		t.Fatalf("登记后应通过，仍违规：%+v", v)
	}
}

// TestPrimitiveGuardAllowsWiredPrimitive 存在生产调用方时应通过。
func TestPrimitiveGuardAllowsWiredPrimitive(t *testing.T) {
	root := newTempRepo(t, map[string]string{
		"internal/foo/widget.go": "package foo\n\nfunc NewWidget() {}\n",
		"internal/app/wire.go":   "package app\n\nimport \"example/internal/foo\"\n\nfunc Wire() { foo.NewWidget() }\n",
	})
	syms := []primitiveSymbol{{Name: "NewWidget", RelPath: "internal/foo/widget.go", StartLine: 3, EndLine: 3}}
	if v := checkNewPrimitives(root, syms, nil); len(v) != 0 {
		t.Fatalf("有生产调用方应通过，仍违规：%+v", v)
	}
}

// TestPrimitiveGuardAllowsReflectionRegistration 按名字字符串反射注册也应视为已接线。
func TestPrimitiveGuardAllowsReflectionRegistration(t *testing.T) {
	root := newTempRepo(t, map[string]string{
		"internal/foo/widget.go":   "package foo\n\nfunc NewWidget() {}\n",
		"internal/app/registry.go": "package app\n\nconst registration = \"NewWidget\"\n",
	})
	syms := []primitiveSymbol{{Name: "NewWidget", RelPath: "internal/foo/widget.go", StartLine: 3, EndLine: 3}}
	if v := checkNewPrimitives(root, syms, nil); len(v) != 0 {
		t.Fatalf("反射按名注册应通过，仍违规：%+v", v)
	}
}

// TestExportedFuncSitesSkipsInterfaceMethodsAndAliases 接口方法与别名不应被当成新增原语。
func TestExportedFuncSitesSkipsInterfaceMethodsAndAliases(t *testing.T) {
	src := []byte("package foo\n\ntype Reader interface {\n\tRead() error\n}\n\nvar Alias = func() {}\n\nfunc Real() {}\n")
	sites := exportedFuncSites(src, "foo.go")
	if len(sites) != 1 || sites[0].Name != "Real" {
		t.Fatalf("期望仅识别 Real，得到 %+v", sites)
	}
}

// TestHasBuildConstraint 构建标签识别。
func TestHasBuildConstraint(t *testing.T) {
	cases := map[string]bool{
		"//go:build windows\n\npackage foo\n": true,
		"// +build linux\n\npackage foo\n":    true,
		"package foo\n\n//go:build linux\n":   false, // package 之后不算
		"package foo\n\nfunc F() {}\n":        false,
	}
	for src, want := range cases {
		if got := hasBuildConstraint([]byte(src)); got != want {
			t.Errorf("hasBuildConstraint(%q) = %v, want %v", src, got, want)
		}
	}
}

// TestIncrementFilesFallsBackWithoutGit 验证无 git 历史时退化为明确的目录清单，
// 且只扫描清单内文件（临时目录不在任何 git 仓库中）。
func TestIncrementFilesFallsBackWithoutGit(t *testing.T) {
	root := newTempRepo(t, map[string]string{
		"internal/archguard/primitive_allowlist.go": "package archguard\n\nvar x = 1\n",
		"internal/foo/other.go":                     "package foo\n\nfunc NewOther() {}\n",
	})
	files, usingGit := incrementFiles(t, root)
	if usingGit {
		t.Fatal("临时目录不应存在 git 基线，应触发退化方案")
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "internal/archguard/primitive_allowlist.go") {
		t.Fatalf("退化方案应只扫描目录清单，得到 %+v", files)
	}
}

// TestPrimitiveAllowlistEntriesHaveReason 真实清单里的每条登记都必须有理由与符号。
func TestPrimitiveAllowlistEntriesHaveReason(t *testing.T) {
	for _, e := range primitiveAllowlist {
		if strings.TrimSpace(e.Symbol) == "" {
			t.Error("原语清单条目 Symbol 为空")
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("原语清单条目 %q 的 Reason 为空", e.Symbol)
		}
	}
}
