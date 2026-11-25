package app

// R25 自动验证 opt-in 的接线测试。所有用例只构造 Resolved 与临时目录，
// 不派生验证子进程、不读进程环境、不用真实模型。

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"example.com/argentgo/internal/config"
)

// withGoMod 在临时工作区写入一个最小 go.mod，返回工作区根目录。
func withGoMod(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/auto-verify\n"), 0o644); err != nil {
		t.Fatalf("写入 go.mod: %v", err)
	}
	return root
}

// TestVerifyAutoOffWithoutConfigIsNil 锁定默认关闭路径：即使工作区有 go.mod，
// 不传 auto 或显式传 false 时仍返回 nil，Options 逐字节不变。
func TestVerifyAutoOffWithoutConfigIsNil(t *testing.T) {
	res := Resolved{Root: withGoMod(t)}
	if got := verifyFuncFor(res); got != nil {
		t.Fatal("verifyFuncFor(res) 未传 auto 时应返回 nil")
	}
	if got := verifyFuncFor(res, false); got != nil {
		t.Fatal("verifyFuncFor(res, false) 应返回 nil")
	}
	if got := verifyFuncForAuto(res, false); got != nil {
		t.Fatal("verifyFuncForAuto(res, false) 应返回 nil")
	}
	if got := autoVerifyArgv(res, false, func(string) bool { return true }); got != nil {
		t.Fatalf("autoVerifyArgv(auto=false) = %v, want nil", got)
	}
}

// TestVerifyAutoOnSynthesizesGoTest 验证开启且无显式配置、工作区有 go.mod 时，
// 合成走白名单的 `go test ./...` argv，且接线产出非 nil 回调。
func TestVerifyAutoOnSynthesizesGoTest(t *testing.T) {
	res := Resolved{Root: withGoMod(t)}
	want := []string{"go", "test", "./..."}
	if got := autoVerifyArgv(res, true, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("autoVerifyArgv(res, true) = %v, want %v", got, want)
	}
	if got := verifyFuncFor(res, true); got == nil {
		t.Fatal("verifyFuncFor(res, true) 应返回自动验证回调")
	}
	if got := verifyFuncForAuto(res, true); got == nil {
		t.Fatal("verifyFuncForAuto(res, true) 应返回自动验证回调")
	}
}

// TestVerifyAutoOnWithoutGoModIsNil 验证工作区没有 go.mod 时即使 auto 开启也不接线，
// 避免在非 Go 项目里派生无意义的 go test。
func TestVerifyAutoOnWithoutGoModIsNil(t *testing.T) {
	res := Resolved{Root: t.TempDir()}
	if got := autoVerifyArgv(res, true, nil); got != nil {
		t.Fatalf("无 go.mod 时 autoVerifyArgv = %v, want nil", got)
	}
	if got := verifyFuncFor(res, true); got != nil {
		t.Fatal("无 go.mod 时 verifyFuncFor(res, true) 应返回 nil")
	}
}

// TestVerifyAutoExplicitConfigWins 验证显式配置优先于自动合成：
// 无论旧版 VerifyCommand 还是按语言映射，只要配置了就不合成 argv，同时保留原接线。
func TestVerifyAutoExplicitConfigWins(t *testing.T) {
	root := withGoMod(t)
	alwaysGoMod := func(string) bool { return true }

	cmdRes := Resolved{
		Root: root,
		Cfg:  config.Config{VerifyCommand: []string{"go", "test", "./..."}},
	}
	if got := autoVerifyArgv(cmdRes, true, alwaysGoMod); got != nil {
		t.Fatalf("已有 VerifyCommand 时不应合成 argv: %v", got)
	}
	if got := verifyFuncForAuto(cmdRes, true); got == nil {
		t.Fatal("已有 VerifyCommand 时仍应返回原回调")
	}

	mapRes := Resolved{
		Root: root,
		Cfg:  config.Config{VerifyCommands: map[string]string{"go": "go test ./..."}},
	}
	if got := autoVerifyArgv(mapRes, true, alwaysGoMod); got != nil {
		t.Fatalf("已有 VerifyCommands 时不应合成 argv: %v", got)
	}
	if got := verifyFuncForAuto(mapRes, true); got == nil {
		t.Fatal("已有 VerifyCommands 时仍应返回原分派器")
	}
}

// TestVerifyAutoGoModMustBeFile 验证 go.mod 必须是普通文件而非同名目录。
func TestVerifyAutoGoModMustBeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "go.mod"), 0o755); err != nil {
		t.Fatalf("创建同名目录: %v", err)
	}
	res := Resolved{Root: root}
	if got := autoVerifyArgv(res, true, nil); got != nil {
		t.Fatalf("go.mod 是目录时不应合成 argv: %v", got)
	}
}
