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
	"sync"
	"testing"
	"time"
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

// baselineQueryConcurrency 限制并发执行 git show 的 worker 数。
// Windows 上进程启动开销大，8 路并发足以把串行 20s 压到几秒，
// 又不会一次拉起上百个 git 进程拖垮机器。
const baselineQueryConcurrency = 8

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
	// 目录改名（如 internal/shell → internal/bash）后，git 把新路径视为全新
	// 文件，按新路径查基线会查不到，导致整包导出函数被误判为「新增原语」。
	// 这里按已知的历史路径别名回退查找，让 moved 文件仍能对上基线的导出名。
	candidates := []string{rel}
	if strings.HasPrefix(rel, "internal/bash/") {
		candidates = append(candidates, "internal/shell/"+strings.TrimPrefix(rel, "internal/bash/"))
	}
	for _, candidate := range candidates {
		if names := baselineExportedNamesAt(root, candidate, rel); len(names) > 0 {
			return names
		}
	}
	return map[string]bool{}
}

// baselineExportedNamesAt 读取基线 commit 中 path 的内容并返回其导出函数名集合。
// rel 仅用于向 exportedFuncSites 说明文件的逻辑位置（决定构建标签等解析）。
func baselineExportedNamesAt(root, path, rel string) map[string]bool {
	names := map[string]bool{}
	out, err := exec.Command("git", "-C", root, "show", primitiveBaseline+":"+path).Output()
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

	type fileSites struct {
		rel   string
		sites []primitiveSymbol
	}
	var pending []fileSites
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
		if !usingGit || len(sites) == 0 {
			out = append(out, sites...)
			continue
		}
		// 基线查询是一次 git show 子进程；串行执行时文件数 × 进程启动开销
		// 会成为门禁耗时大头。先收集起来，下面用有界 worker 池并发查。
		pending = append(pending, fileSites{rel: rel, sites: sites})
	}

	if len(pending) > 0 {
		baselineNames := make([]map[string]bool, len(pending))
		semaphore := make(chan struct{}, baselineQueryConcurrency)
		var wg sync.WaitGroup
		for i := range pending {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				baselineNames[i] = baselineExportedNames(root, pending[i].rel)
			}(i)
		}
		wg.Wait()

		for i, item := range pending {
			base := baselineNames[i]
			for _, s := range item.sites {
				if !base[s.Name] {
					out = append(out, s)
				}
			}
		}
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

// hasProductionCaller 判断 sym 是否有生产调用方。
// 它是单符号便捷入口；批量判定请直接用 buildCallIndex + hasCaller，
// 避免每个符号都重建一次全仓索引。判定语义见 call_index_test.go。
func hasProductionCaller(root string, sym primitiveSymbol) bool {
	idx, err := buildCallIndex(root)
	if err != nil {
		return false
	}
	return idx.hasCaller(sym)
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
	idx, err := buildCallIndex(root)
	if err != nil {
		// 索引建不起来时保守报出全部待查符号，由调用方看到明确失败，
		// 而不是静默放行——门禁失效比误报更危险。空索引让所有未登记符号
		// 都走到违规分支，同时复用下面的排序逻辑。
		return checkNewPrimitivesWithIndex(syms, allow, &callIndex{
			calls:   map[string][]callSite{},
			strings: map[string]bool{},
		})
	}
	return checkNewPrimitivesWithIndex(syms, allow, idx)
}

// checkNewPrimitivesWithIndex 使用预建的调用索引判定，供已建索引的调用方复用。
func checkNewPrimitivesWithIndex(syms []primitiveSymbol, allow []primitiveAllowEntry, idx *callIndex) []primitiveViolation {
	// 先剔除已登记的符号；若没有待查符号，连查询都不必做。
	pending := make([]primitiveSymbol, 0, len(syms))
	for _, sym := range syms {
		if !isPrimitiveAllowed(sym, allow) {
			pending = append(pending, sym)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	var out []primitiveViolation
	for _, sym := range pending {
		if idx.hasCaller(sym) {
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
	started := time.Now()
	syms, mode := newPrimitivesFromIncrement(t, root)
	scanElapsed := time.Since(started)
	started = time.Now()
	idx, err := buildCallIndex(root)
	if err != nil {
		t.Fatalf("构建生产调用索引失败：%v", err)
	}
	indexElapsed := time.Since(started)
	t.Logf("原语门禁增量判定模式：%s；本次新增导出函数 %d 个；增量扫描 %v；调用索引 %v", mode, len(syms), scanElapsed, indexElapsed)
	for _, v := range checkNewPrimitivesWithIndex(syms, primitiveAllowlist, idx) {
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
