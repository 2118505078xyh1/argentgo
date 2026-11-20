package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestNarrowVerifyArgvByChangedFiles 锁定核心优化：全仓库的 `go test ./...` 目标
// 被去重、排序的改动 Go 包集合取代，且配置的 argv 切片保持原样。
// （接上行）
func TestNarrowVerifyArgvByChangedFiles(t *testing.T) {
	root := t.TempDir()
	argv := []string{"go", "test", "./..."}
	got, narrowed, skipped := narrowVerifyArgv(argv,
		[]string{"internal/llm/bar.go", "internal/app/foo.go", "internal/app/baz.go"}, root)
	if !narrowed {
		t.Fatal("narrowed = false, want true")
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	want := []string{"go", "test", "./internal/app", "./internal/llm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(argv, []string{"go", "test", "./..."}) {
		t.Fatalf("configured argv was mutated: %q", argv)
	}
}

// TestVerifyNarrowTargetsRejectsUnsafeAndOutOfWorkspacePaths 锁定安全边界：
// 遍历、绝对/盘符/UNC 路径、shell 元字符、空白、NUL 和空输入都会被跳过而非插值。
// （接上行）
func TestVerifyNarrowTargetsRejectsUnsafeAndOutOfWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	unsafe := []string{
		"../escape.go",                // 词法遍历
		"a/../../escape.go",           // clean 之后的遍历
		`..\evil.go`,                  // Windows 风格的遍历
		"internal/app/..\\secret.go",  // 通过混合分隔符的遍历
		"/etc/passwd",                 // Unix 绝对路径
		`C:\Windows\system32\evil.go`, // Windows 盘符前缀
		`\\server\share\evil.go`,      // UNC 根目录
		"internal/app/x.go; rm -rf /", // shell 分隔符
		"internal/app/$(whoami).go",   // 命令替换
		"internal/app/a.go|cat",       // pipe
		"internal/app/a b.go",         // whitespace
		"internal/app/a.go\x00.go",    // 内嵌 NUL
		"",                            // empty
	}
	targets, skipped := verifyNarrowTargets(root, unsafe)
	if len(targets) != 0 {
		t.Fatalf("targets = %q, want none", targets)
	}
	if skipped != len(unsafe) {
		t.Fatalf("skipped = %d, want %d", skipped, len(unsafe))
	}

	// 即使安全路径与不安全路径并存，也只使用安全路径，
	// 坏路径计为跳过。
	targets, skipped = verifyNarrowTargets(root, append([]string{"ok/ok.go"}, unsafe...))
	if !reflect.DeepEqual(targets, []string{"./ok"}) {
		t.Fatalf("targets = %q, want [./ok]", targets)
	}
	if skipped != len(unsafe) {
		t.Fatalf("skipped = %d, want %d", skipped, len(unsafe))
	}
}

// TestNarrowVerifyArgvNoPathsKeepsFullCommand 锁定没有改动路径时的不变行为：
// 配置的完整命令原样运行。
func TestNarrowVerifyArgvNoPathsKeepsFullCommand(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"go", "build", "./..."},
		{"go", "test"},
		{"./tools/check.exe", "--quiet"},
	} {
		got, narrowed, skipped := narrowVerifyArgv(argv, nil, t.TempDir())
		if narrowed || skipped != 0 {
			t.Fatalf("argv %q: narrowed=%v skipped=%d, want false/0", argv, narrowed, skipped)
		}
		if !reflect.DeepEqual(got, argv) {
			t.Fatalf("argv %q: got %q, want unchanged", argv, got)
		}
	}
}

// TestVerifyNarrowTargetsCJKPaths 确认 Unicode（CJK）路径组件被字符白名单接受并作为有效包模式保留，
// 而同一批次中的非 Go 文件仍被跳过。
// （接上行）
func TestVerifyNarrowTargetsCJKPaths(t *testing.T) {
	root := t.TempDir()
	paths := []string{"内部/模块/工具.go", "内部/模块/另一个.go", "docs/说明.md"}
	targets, skipped := verifyNarrowTargets(root, paths)
	if !reflect.DeepEqual(targets, []string{"./内部/模块"}) {
		t.Fatalf("targets = %q, want [./内部/模块]", targets)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the .md file)", skipped)
	}

	got, narrowed, _ := narrowVerifyArgv([]string{"go", "test", "./..."}, paths, root)
	if !narrowed {
		t.Fatal("narrowed = false, want true")
	}
	want := []string{"go", "test", "./内部/模块"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestNarrowVerifyArgvModesAndNoOps 锁定两种收窄模式以及所有必须拒绝收窄的情形：
// 显式操作人员目标、未知命令形状和 .exe 命令都保持其精确配置的 argv。
// （接上行）
func TestNarrowVerifyArgvModesAndNoOps(t *testing.T) {
	root := t.TempDir()
	paths := []string{"a/a.go"}
	tests := []struct {
		name     string
		argv     []string
		want     []string
		narrowed bool
	}{
		{
			name:     "append when no target declared",
			argv:     []string{"go", "test"},
			want:     []string{"go", "test", "./a"},
			narrowed: true,
		},
		{
			name:     "replace wildcard and keep flags",
			argv:     []string{"go", "test", "-race", "./..."},
			want:     []string{"go", "test", "-race", "./a"},
			narrowed: true,
		},
		{
			name:     "operator explicit target is not overridden",
			argv:     []string{"go", "test", "./pkg/..."},
			want:     []string{"go", "test", "./pkg/..."},
			narrowed: false,
		},
		{
			name:     "flag with separate value is not overridden",
			argv:     []string{"go", "test", "-count", "1"},
			want:     []string{"go", "test", "-count", "1"},
			narrowed: false,
		},
		{
			name:     "exe command is never narrowed",
			argv:     []string{"./check.exe", "--quiet"},
			want:     []string{"./check.exe", "--quiet"},
			narrowed: false,
		},
		{
			name:     "go run is not a verification target",
			argv:     []string{"go", "run", "."},
			want:     []string{"go", "run", "."},
			narrowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, narrowed, _ := narrowVerifyArgv(tc.argv, paths, root)
			if narrowed != tc.narrowed {
				t.Fatalf("narrowed = %v, want %v", narrowed, tc.narrowed)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeNarrowProbeModule 创建一个微型模块，包含一个通过的包 (a) 和一个失败的包 (b)。
// 完整的 `go test ./...` 会失败；收窄后的 `go test ./a` 会通过，
// 使测试能端到端观察收窄。
func writeNarrowProbeModule(t *testing.T, workspace string) {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module narrowprobe\n\ngo 1.21\n")
	if err := os.MkdirAll(filepath.Join(workspace, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "b"), 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	writeVerifyFile(t, filepath.Join(workspace, "a"), "a_test.go",
		"package a\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	writeVerifyFile(t, filepath.Join(workspace, "b"), "b_test.go",
		"package b\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"BOOM_B\") }\n")
}

// TestNewVerifyFuncNarrowsGoTestToChangedPackage 是端到端证明：包 a 中有改动路径时运行器通过（只测试 ./a）；
// 没有路径时回退到 ./... 并看到故意失败的包 b。
// （接上行）
func TestNewVerifyFuncNarrowsGoTestToChangedPackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeNarrowProbeModule(t, workspace)
	verify := NewVerifyFunc([]string{"go", "test", "./..."}, 120000, workspace)

	_, passed, _, ok := verify(context.Background(), []string{"a/a.go"})
	if !ok || !passed {
		t.Fatalf("narrowed run = (passed=%v, ok=%v), want a passing verdict", passed, ok)
	}
	_, passed, _, ok = verify(context.Background(), nil)
	if !ok || passed {
		t.Fatalf("full run = (passed=%v, ok=%v), want a failing verdict", passed, ok)
	}
}

// TestVerifyDispatchForwardsChangedPaths 守护分派器接线：按语言的运行器必须接收检查点路径，
// 而非它自己的命令 argv。有了下面的通配符命令，转发的 argv 会被误认为包名
// （./go、./test），检查会虚假失败。
// （接上行）
func TestVerifyDispatchForwardsChangedPaths(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeNarrowProbeModule(t, workspace)
	dispatch := newVerifyDispatchFunc(map[string]string{"*": "go test ./..."}, nil, 120000, workspace)

	_, passed, _, ok := dispatch(context.Background(), []string{"a/a.go"})
	if !ok || !passed {
		t.Fatalf("dispatch narrowed run = (passed=%v, ok=%v), want a passing verdict", passed, ok)
	}
	_, passed, _, ok = dispatch(context.Background(), nil)
	if !ok || passed {
		t.Fatalf("dispatch full run = (passed=%v, ok=%v), want a failing verdict", passed, ok)
	}
}
