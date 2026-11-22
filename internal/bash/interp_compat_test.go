package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本节固定内置解释器与系统 shell（exec.Cmd.CombinedOutput）对齐的关键语义：
// stderr 默认可见、重定向只改各自的流、命令替换不污染 $?、管道退出码取末段。

// TestStderrMergedByDefault 验证内置路径默认把 stderr 并入输出，而不是像
// 旧实现那样静默丢弃。对齐 tools_builtin_bash.go 默认路径的 CombinedOutput。
func TestStderrMergedByDefault(t *testing.T) {
	dir := t.TempDir()
	res, err := Run("cat missing.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "shell: cat:") {
		t.Fatalf("默认合并 stderr：输出缺少 cat 的错误信息，got %q", res.Output)
	}
	if res.ExitStatus != 1 {
		t.Fatalf("cat 失败退出码 = %d, want 1", res.ExitStatus)
	}

	// 正常命令不应凭空多出 stderr。
	res, err = Run("echo ok", testOptions(dir))
	if err != nil || res.Output != "ok\n" {
		t.Fatalf("echo => %q err=%v", res.Output, err)
	}
}

// TestStderrRedirectCombinations 覆盖 > / >> / 2>&1 的组合：
// stdout 进文件时，未被重定向的 stderr 仍可见；2>&1 让 stderr 跟随 stdout 进文件。
func TestStderrRedirectCombinations(t *testing.T) {
	dir := t.TempDir()

	// 2>&1 与默认合并等价：stderr 仍可见。
	res, err := Run("cat missing.txt 2>&1", testOptions(dir))
	if err != nil || !strings.Contains(res.Output, "shell: cat:") {
		t.Fatalf("2>&1 stderr 应可见，got %q err=%v", res.Output, err)
	}

	// stdout 进文件，stderr 未被重定向 → stderr 仍可见，文件只含 stdout。
	out := filepath.Join(dir, "out.txt")
	res, err = Run("echo hi > out.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "hi\n" {
		t.Fatalf("> 文件 = %q err=%v", data, err)
	}
	if res.Output != "" {
		t.Fatalf("> 后可见输出应为空，got %q", res.Output)
	}

	// stdout 进文件 + 2>&1 → stderr 也进文件，不再出现在可见输出。
	errFile := filepath.Join(dir, "err.txt")
	res, err = Run("cat missing.txt > err.txt 2>&1", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(errFile)
	if err != nil || !strings.Contains(string(data), "shell: cat:") {
		t.Fatalf("2>&1 应把 stderr 写入文件，got %q err=%v", data, err)
	}
	if res.Output != "" {
		t.Fatalf("2>&1 重定向到文件后可见输出应为空，got %q", res.Output)
	}

	// 仅 stdout 进文件时，stderr 不进文件但保持可见。
	errOnly := filepath.Join(dir, "stderr-only.txt")
	res, err = Run("cat missing.txt > stderr-only.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(errOnly)
	if err != nil || len(data) != 0 {
		t.Fatalf("stderr 不应进文件，got %q err=%v", data, err)
	}
	if !strings.Contains(res.Output, "shell: cat:") {
		t.Fatalf("未重定向的 stderr 应可见，got %q", res.Output)
	}

	// 追加语义保持不变。
	res, err = Run("echo again >> out.txt", testOptions(dir))
	if err != nil || res.Output != "" {
		t.Fatalf(">> => %q err=%v", res.Output, err)
	}
	data, _ = os.ReadFile(out)
	if string(data) != "hi\nagain\n" {
		t.Fatalf(">> 追加 = %q", data)
	}
}

// TestCommandSubstitutionDoesNotPolluteLastStatus 验证命令替换在局部保存/恢复
// LastStatus 后不再改变外层 $?。
func TestCommandSubstitutionDoesNotPolluteLastStatus(t *testing.T) {
	dir := t.TempDir()

	// 任务给出的直接用例：替换体内的 false 不应泄漏到外层。
	res, err := Run(`$(false); echo $?`, testOptions(dir))
	if err != nil || res.Output != "0\n" {
		t.Fatalf("$(false); echo $? => %q err=%v, want 0", res.Output, err)
	}

	// 仅重定向的空命令会保留既有状态（提前 return 不改写 LastStatus），
	// 因此替换体成功也不能抹掉前一条 false 的失败状态。
	res, err = Run(`false; > $(true); echo $?`, testOptions(dir))
	if err != nil || res.Output != "1\n" {
		t.Fatalf("false; > $(true); echo $? => %q err=%v, want 1", res.Output, err)
	}

	// 反向：替换体失败也不应把失败状态塞进外层空命令。
	res, err = Run(`> $(false); echo $?`, testOptions(dir))
	if err != nil || res.Output != "0\n" {
		t.Fatalf("> $(false); echo $? => %q err=%v, want 0", res.Output, err)
	}
}

// TestPipelineExitStatus 固定管道默认退出码取最后一段（系统 shell 的默认语义）。
func TestPipelineExitStatus(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		cmd  string
		want int
	}{
		{`true | false`, 1},
		{`false | true`, 0},
		{`false | true | false`, 1},
		{`echo hi | grep hi`, 0},
		{`echo hi | grep nomatch`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			res, err := Run(tc.cmd, testOptions(dir))
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitStatus != tc.want {
				t.Fatalf("%q exit = %d, want %d (output %q)", tc.cmd, res.ExitStatus, tc.want, res.Output)
			}
		})
	}
}

// TestStderrNotPipedToNextStage 验证 stderr 不会借字符串接力流入下一段管道的 stdin。
// 与 bash 一致：左段 stderr 保持可见（不进管道），但绝不成为右段的输入。
func TestStderrNotPipedToNextStage(t *testing.T) {
	t.Run("left-stderr-stays-visible", func(t *testing.T) {
		// bash 中 `cat missing | cat` 的左段报错可见，但右段只读到空 stdin。
		dir := t.TempDir()
		res, err := Run("cat missing.txt | cat", testOptions(dir))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Output, "shell: cat:") {
			t.Fatalf("左段 stderr 应可见（与 bash 一致），got %q", res.Output)
		}
	})

	t.Run("stderr-does-not-enter-stdin", func(t *testing.T) {
		// 把左段 stderr 重定向到文件，隔离其可见性；此时右段 grep 若从 stdin
		// 读到错误文本就会输出。正确行为：错误文本只进 err.log，不进管道。
		dir := t.TempDir()
		res, err := Run("cat missing.txt 2>err.log | grep 'shell: cat:'", testOptions(dir))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Output, "shell: cat:") {
			t.Fatalf("stderr 不应流入管道下一段，got %q", res.Output)
		}
	})
}

// TestCJKStderrVisible 验证中文 stderr 不因合并/裁剪而丢失乱码或消失。
func TestCJKStderrVisible(t *testing.T) {
	dir := t.TempDir()
	res, err := Run("cd 缺失目录", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "缺失目录") {
		t.Fatalf("CJK stderr 应可见，got %q", res.Output)
	}
	if res.ExitStatus != 1 {
		t.Fatalf("cd 失败退出码 = %d, want 1", res.ExitStatus)
	}
}
