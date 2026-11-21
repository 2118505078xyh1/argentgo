package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件是纯测试守护层：它不引入任何生产逻辑，只对既有工具的关键
// 不变量（分页边界、错误文案、上限、路径与目录顺序）做二次锁定。
// 与既有测试辅助（pagingRoot/runPagingRead/numberedLines/writeFixture 等）
// 复用；本文件新增的辅助统一以 guard 前缀命名，避免与同包符号冲突。

// guardRunRaw 以 yolo 模式运行一个已注册工具，接收原始 JSON 字符串，
// 便于精确构造边界参数（例如超出 int 字面量表达范围的巨大 limit）。
func guardRunRaw(t *testing.T, root, name, raw string) (string, error) {
	t.Helper()
	return New(root, true).Run(context.Background(), name, raw)
}

// guardWantErr 断言错误精确等于 golden 文案（逐字节一致），
// 用于锁定模型的工具错误契约，防止措辞被误改。
func guardWantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want exactly %q", want)
	}
	if err.Error() != want {
		t.Fatalf("err = %q, want exactly %q", err.Error(), want)
	}
}

// guardWriteFile 写入一个可能位于嵌套（含 CJK）目录下的文本文件，
// 自动创建父目录；既有 writeFixture 不会创建父目录，故此处单独封装。
func guardWriteFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// guardLineCount 统计无尾随换行的工具输出中的行数。
func guardLineCount(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// guardValidUTF8 断言输出是有效 UTF-8 且不含替换符（防止 CJK 路径被切坏）。
func guardValidUTF8(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) || strings.ContainsRune(s, utf8.RuneError) {
		t.Fatalf("output contains broken UTF-8: %q", s)
	}
}

// ---------------------------------------------------------------------------
// 一、read 的分页 / 行窗口 / 溢出保护
// ---------------------------------------------------------------------------

// TestDepthGuardReadOffsetLimitBoundaries 锁定 offset/limit 的边界语义：
// offset 下溢钳到 1、上溢返回空、limit 非正表示读到末尾。
func TestDepthGuardReadOffsetLimitBoundaries(t *testing.T) {
	const content = "l1\nl2\nl3\nl4\nl5\n"  // 5 个真实行，尾随换行不产生幻影行
	root := pagingRoot(t, "f.txt", content) // 复用同包辅助

	notice12 := fmt.Sprintf(pagingNoticeFmt, 1, 2, 5)
	cases := []struct {
		name, args, want string
	}{
		{"offset zero clamps to one", `{"path":"f.txt","offset":0,"limit":2}`, "l1\nl2\n" + notice12},
		{"negative offset clamps to one", `{"path":"f.txt","offset":-10,"limit":2}`, "l1\nl2\n" + notice12},
		{"limit one", `{"path":"f.txt","offset":2,"limit":1}`, "l2\n" + fmt.Sprintf(pagingNoticeFmt, 2, 2, 5)},
		{"offset beyond last line is empty", `{"path":"f.txt","offset":99,"limit":1}`, ""},
		{"limit zero reads to end", `{"path":"f.txt","offset":3,"limit":0}`, "l3\nl4\nl5\n"},
		{"negative limit reads to end", `{"path":"f.txt","offset":3,"limit":-5}`, "l3\nl4\nl5\n"},
		{"last real line is quiet", `{"path":"f.txt","offset":5,"limit":1}`, "l5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runPagingRead(t, root, tc.args); got != tc.want { // 复用同包辅助
				t.Fatalf("read %s = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestDepthGuardReadHugeLimitNoPanic 守护整数溢出防线：巨大 limit
// 表现为"从 offset 读到末尾"，既不得 panic，也不得触发 runTool 的
// panic 恢复边界（后者会把 panic 变成含 "panicked" 的错误文案）。
func TestDepthGuardReadHugeLimitNoPanic(t *testing.T) {
	root := pagingRoot(t, "f.txt", "a\nb\nc\n")
	for _, huge := range []string{
		"9223372036854775807", // math.MaxInt64
		"9223372036854775806", // MaxInt64-1
		"4611686018427387904", // 1<<62，start-1 相加即溢出
		"1000000000000000000",
	} {
		t.Run(huge, func(t *testing.T) {
			args := `{"path":"f.txt","offset":2,"limit":` + huge + `}`
			if got := runPagingRead(t, root, args); got != "b\nc\n" {
				t.Fatalf("read with huge limit = %q, want %q", got, "b\nc\n")
			}
		})
	}

	// 通过注册表路径再跑一次：确保 panic 边界同样未被触发。
	out, err := guardRunRaw(t, root, "read", `{"path":"f.txt","offset":2,"limit":9223372036854775807}`)
	if err != nil {
		t.Fatalf("read via registry: %v", err)
	}
	if strings.Contains(out, "panicked") {
		t.Fatalf("huge limit triggered the panic boundary: %q", out)
	}
	if out != "b\nc\n" {
		t.Fatalf("read via registry = %q, want %q", out, "b\nc\n")
	}
}

// TestDepthGuardReadWindowCapInvariant 锁定以行中心的窗口不变量：
// before/after 独立钳制在 [0, readWindowCap]，负数归零、超限等于上限。
func TestDepthGuardReadWindowCapInvariant(t *testing.T) {
	if readWindowCap != 200 {
		t.Fatalf("readWindowCap = %d, want 200", readWindowCap)
	}
	root := pagingRoot(t, "f.txt", numberedLines(500))

	// 恰好到达上限 200/200 的窗口。
	atCap := runPagingRead(t, root, `{"path":"f.txt","line":250,"before":200,"after":200}`)
	wantCap := fmt.Sprintf(pagingNoticeFmt, 50, 450, 500)
	if !strings.HasPrefix(atCap, "l50\n") || !strings.HasSuffix(atCap, "\nl450\n"+wantCap) {
		t.Fatalf("window at cap is wrong: %q", atCap)
	}

	// 远超上限必须与恰好到达上限完全等价（逐字节）。
	overCap := runPagingRead(t, root, `{"path":"f.txt","line":250,"before":9999,"after":9999}`)
	if overCap != atCap {
		t.Fatalf("window over cap differs from window at cap:\n at cap: %q\n over:   %q", atCap, overCap)
	}

	// 负数钳到 0：只剩目标行本身。
	neg := runPagingRead(t, root, `{"path":"f.txt","line":250,"before":-5,"after":-5}`)
	wantNeg := "l250\n" + fmt.Sprintf(pagingNoticeFmt, 250, 250, 500)
	if neg != wantNeg {
		t.Fatalf("negative window = %q, want %q", neg, wantNeg)
	}
}

// ---------------------------------------------------------------------------
// 二、edit / multi_edit 错误文案逐字节稳定（golden）
// ---------------------------------------------------------------------------

// TestDepthGuardEditErrorStringsAreByteStable 锁定 edit 的核心错误文案。
// eval 套件对这些字节做精确断言，任何措辞漂移都必须显式更新 golden。
func TestDepthGuardEditErrorStringsAreByteStable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "x.txt", "unique\n")

	_, err := guardRunRaw(t, root, "edit", `{"path":"x.txt","new":"y"}`)
	guardWantErr(t, err, "edit: old must not be empty")

	_, err = guardRunRaw(t, root, "edit", `{"path":"x.txt","old":"missing","new":"y"}`)
	guardWantErr(t, err, "edit: old text not found in x.txt")

	writeFixture(t, root, "dup.txt", "dup\ndup\n")
	_, err = guardRunRaw(t, root, "edit", `{"path":"dup.txt","old":"dup","new":"y"}`)
	guardWantErr(t, err, "edit: old text appears 2 times in dup.txt (not unique); a unique match is required")

	// 成功文案同样是稳定契约。
	out, err := guardRunRaw(t, root, "edit", `{"path":"x.txt","old":"unique","new":"changed"}`)
	if err != nil || out != "edited x.txt" {
		t.Fatalf("edit success = %q, %v; want %q, nil", out, err, "edited x.txt")
	}
}

// TestDepthGuardMultiEditErrorStringsAreByteStable 锁定 multi_edit 的核心
// 错误文案（含批次序号与计数短语），防止误改破坏 eval 基准。
func TestDepthGuardMultiEditErrorStringsAreByteStable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "x.txt", "dup\ndup\n")

	_, err := guardRunRaw(t, root, "multi_edit", `{"path":"x.txt","edits":[]}`)
	guardWantErr(t, err, "multi_edit: edits must not be empty")

	_, err = guardRunRaw(t, root, "multi_edit", `{"path":"x.txt","edits":[{"new":"y"}]}`)
	guardWantErr(t, err, "multi_edit: edit 1 has an empty old text")

	_, err = guardRunRaw(t, root, "multi_edit", `{"path":"x.txt","edits":[{"old":"missing","new":"y"}]}`)
	guardWantErr(t, err, "multi_edit: edit 1: old text not found in x.txt")

	_, err = guardRunRaw(t, root, "multi_edit", `{"path":"x.txt","edits":[{"old":"dup","new":"y"}]}`)
	guardWantErr(t, err, "multi_edit: edit 1: old text appears 2 times in x.txt (not unique); a unique match is required")

	// 成功文案同样锁定（含编辑计数）。
	writeFixture(t, root, "y.txt", "b\n")
	out, err := guardRunRaw(t, root, "multi_edit",
		`{"path":"y.txt","edits":[{"old":"b","new":"c"},{"old":"c","new":"d"}]}`)
	if err != nil || out != "edited y.txt (2 edits)" {
		t.Fatalf("multi_edit success = %q, %v; want %q, nil", out, err, "edited y.txt (2 edits)")
	}
}

// ---------------------------------------------------------------------------
// 三、grep / find / glob 的 200 上限、NUL 跳过与 CJK 路径
// ---------------------------------------------------------------------------

// TestDepthGuardCapsAreStable 锁定上限常量本身，避免其被悄悄改动后
// 下面基于 maxMatches 的断言失去意义。
func TestDepthGuardCapsAreStable(t *testing.T) {
	if maxMatches != 200 {
		t.Fatalf("maxMatches = %d, want 200", maxMatches)
	}
}

// TestDepthGuardSearchMatchesCapAt200 验证三种搜索工具都把结果数
// 限制在 maxMatches(200)，不会倾泻无界输出。
func TestDepthGuardSearchMatchesCapAt200(t *testing.T) {
	root := t.TempDir()

	// grep：单文件中 250 行全部命中，输出恰好 200 行。
	var sb strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&sb, "line %d needle\n", i)
	}
	writeFixture(t, root, "g.txt", sb.String())
	out, err := guardRunRaw(t, root, "grep", `{"pattern":"needle","path":"g.txt"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if n := guardLineCount(out); n != maxMatches {
		t.Fatalf("grep returned %d lines, want %d", n, maxMatches)
	}
	if !strings.HasPrefix(out, "g.txt:1: ") {
		t.Fatalf("grep hit format drifted: %q", guardFirstLine(out))
	}

	// find / glob：各 250 个匹配文件，输出都恰好 200 行。
	dir := filepath.Join(root, "many")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		for _, ext := range []string{"txt", "go"} {
			name := fmt.Sprintf("match%03d.%s", i, ext)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	found, err := guardRunRaw(t, root, "find", `{"pattern":"*.txt","path":"many"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if n := guardLineCount(found); n != maxMatches {
		t.Fatalf("find returned %d lines, want %d", n, maxMatches)
	}
	globbed, err := guardRunRaw(t, root, "glob", `{"pattern":"*.go","path":"many"}`)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if n := guardLineCount(globbed); n != maxMatches {
		t.Fatalf("glob returned %d lines, want %d", n, maxMatches)
	}
}

// guardFirstLine 返回首行，用于简短的错误诊断。
func guardFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestDepthGuardGrepSkipsNULBinary 验证 grep 跳过含 NUL 的二进制文件，
// 只报告同目录下的正常文本文件。
func TestDepthGuardGrepSkipsNULBinary(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "good.txt", "needle in text\n")
	writeFixture(t, root, "blob.bin", "needle\x00binary junk\n")

	out, err := guardRunRaw(t, root, "grep", `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "good.txt:1:") {
		t.Fatalf("grep missed the text file: %q", out)
	}
	if strings.Contains(out, "blob.bin") {
		t.Fatalf("grep leaked the NUL binary file: %q", out)
	}
}

// TestDepthGuardSearchToolsHandleCJKPaths 验证 grep/find/glob 在 CJK
// 路径下正常工作，且输出保持有效 UTF-8（路径字节不被切断）。
func TestDepthGuardSearchToolsHandleCJKPaths(t *testing.T) {
	root := t.TempDir()
	guardWriteFile(t, root, "中文/检索文件.txt", "中文命中行\n第二行\n")

	out, err := guardRunRaw(t, root, "grep", `{"pattern":"命中","path":"中文"}`)
	if err != nil {
		t.Fatalf("grep CJK: %v", err)
	}
	if !strings.Contains(out, "中文/检索文件.txt:1:") || !strings.Contains(out, "中文命中行") {
		t.Fatalf("grep CJK path/content missing: %q", out)
	}
	guardValidUTF8(t, out)

	out, err = guardRunRaw(t, root, "find", `{"pattern":"*.txt","path":"中文"}`)
	if err != nil {
		t.Fatalf("find CJK: %v", err)
	}
	if !strings.Contains(out, "中文/检索文件.txt") {
		t.Fatalf("find CJK path missing: %q", out)
	}
	guardValidUTF8(t, out)

	out, err = guardRunRaw(t, root, "glob", `{"pattern":"**/*.txt"}`)
	if err != nil {
		t.Fatalf("glob CJK: %v", err)
	}
	if !strings.Contains(out, "中文/检索文件.txt") {
		t.Fatalf("glob CJK path missing: %q", out)
	}
	guardValidUTF8(t, out)
}

// ---------------------------------------------------------------------------
// 四、Specs() 的顺序与名字稳定（golden）
// ---------------------------------------------------------------------------

// TestDepthGuardSpecsOrderAndNamesStable 锁定核心既有工具在 Specs()
// 中的相对顺序与名字。使用子序列断言而非全量精确列表：新增工具可以
// 插入，但这些工具自身的名字和相对次序不得漂移。
func TestDepthGuardSpecsOrderAndNamesStable(t *testing.T) {
	// golden：受守护核心工具在目录中的相对顺序。
	golden := []string{"read", "write", "edit", "ls", "grep", "find", "glob", "multi_edit"}

	// 每个工具类型的 Name() 必须与 golden 对齐。
	toolsByName := []struct {
		tool Tool
		want string
	}{
		{readTool{}, "read"},
		{writeTool{}, "write"},
		{editTool{}, "edit"},
		{lsTool{}, "ls"},
		{grepTool{}, "grep"},
		{findTool{}, "find"},
		{globTool{}, "glob"},
		{multiEditTool{}, "multi_edit"},
	}
	for _, entry := range toolsByName {
		if got := entry.tool.Name(); got != entry.want {
			t.Errorf("tool.Name() = %q, want %q", got, entry.want)
		}
	}

	r := New(t.TempDir(), true)
	specs := r.Specs()
	names := r.Names()

	// Specs() 与 Names() 必须产出相同的名字序列。
	if len(specs) != len(names) {
		t.Fatalf("Specs() has %d entries, Names() has %d", len(specs), len(names))
	}
	for i := range specs {
		if specs[i].Function.Name != names[i] {
			t.Fatalf("spec[%d] = %q, Names()[%d] = %q", i, specs[i].Function.Name, i, names[i])
		}
	}

	// 子序列 + 唯一性 + spec 基本不变量。
	pos := -1
	for _, want := range golden {
		at, count := -1, 0
		for i, spec := range specs {
			if spec.Function.Name == want {
				at, count = i, count+1
			}
		}
		if count != 1 {
			t.Fatalf("spec %q appears %d times, want exactly 1", want, count)
		}
		if at <= pos {
			t.Fatalf("spec %q at index %d broke the golden order (previous index %d)", want, at, pos)
		}
		pos = at

		spec := specs[at].Function
		if specs[at].Type != "function" {
			t.Errorf("spec %q type = %q, want function", want, specs[at].Type)
		}
		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("spec %q has an empty description", want)
		}
		if len(spec.Parameters) == 0 {
			t.Errorf("spec %q has empty parameters", want)
		}
	}
}
