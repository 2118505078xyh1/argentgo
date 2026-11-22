package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runWithDir 在独立临时目录执行一条命令，返回可见输出与目录（供检查产物文件）。
func runWithDir(t *testing.T, cmd string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	res, err := Run(cmd, controlOptions(dir))
	if err != nil {
		t.Fatalf("Run(%q) error = %v", cmd, err)
	}
	return strings.TrimSpace(res.Output), dir
}

func readAt(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// TestRedirectFD 覆盖显式 fd 重定向：2>/2>> 单独重定向 stderr，&> 合并两流。
// 这两类在 P1 之前分别被静默误解（2> 被当成参数 + stdout 重定向）和解析失败。
func TestRedirectFD(t *testing.T) {
	t.Run("stderr-only", func(t *testing.T) {
		out, dir := runWithDir(t, `cat missing.txt 2>err.log`)
		if out != "" {
			t.Fatalf("stderr 被重定向后不应出现在可见输出: %q", out)
		}
		if got := readAt(t, dir, "err.log"); !strings.Contains(got, "missing.txt") {
			t.Fatalf("err.log = %q, want containing missing.txt", got)
		}
	})

	t.Run("stderr-only-keeps-stdout", func(t *testing.T) {
		out, dir := runWithDir(t, `echo visible 2>err.log`)
		if out != "visible" {
			t.Fatalf("可见输出 = %q, want visible", out)
		}
		if got := readAt(t, dir, "err.log"); strings.TrimSpace(got) != "" {
			t.Fatalf("err.log 应为空（echo 无 stderr）: %q", got)
		}
	})

	t.Run("both-streams", func(t *testing.T) {
		out, dir := runWithDir(t, `cat missing.txt &>all.log`)
		if out != "" {
			t.Fatalf("&> 后可见输出应为空: %q", out)
		}
		if got := readAt(t, dir, "all.log"); !strings.Contains(got, "missing.txt") {
			t.Fatalf("all.log = %q, want containing missing.txt", got)
		}
	})

	t.Run("both-streams-with-stdout", func(t *testing.T) {
		out, dir := runWithDir(t, `echo hello &>all.log`)
		if out != "" {
			t.Fatalf("&> 后可见输出应为空: %q", out)
		}
		if got := readAt(t, dir, "all.log"); strings.TrimSpace(got) != "hello" {
			t.Fatalf("all.log = %q, want hello", got)
		}
	})

	t.Run("stdout-and-dup", func(t *testing.T) {
		out, dir := runWithDir(t, `echo hello >out.txt 2>&1`)
		if out != "" {
			t.Fatalf("重定向后可见输出应为空: %q", out)
		}
		if got := readAt(t, dir, "out.txt"); strings.TrimSpace(got) != "hello" {
			t.Fatalf("out.txt = %q, want hello", got)
		}
	})

	t.Run("append-stderr", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "err.log"), []byte("old\n"), 0644)
		res, err := Run(`cat missing.txt 2>>err.log`, controlOptions(dir))
		if err != nil {
			t.Fatal(err)
		}
		if res.Output != "" {
			t.Fatalf("可见输出应为空: %q", res.Output)
		}
		if got := readAt(t, dir, "err.log"); !strings.HasPrefix(got, "old\n") || !strings.Contains(got, "missing.txt") {
			t.Fatalf("2>> 未追加: %q", got)
		}
	})

	// 函数/别名的输出重定向必须落到文件，而不应打到可见输出。
	t.Run("function-redirect", func(t *testing.T) {
		out, dir := runWithDir(t, `f() { echo hi; }; f >out.txt`)
		if out != "" {
			t.Fatalf("函数重定向后可见输出应为空: %q", out)
		}
		if got := strings.TrimSpace(readAt(t, dir, "out.txt")); got != "hi" {
			t.Fatalf("out.txt = %q, want hi", got)
		}
	})

	t.Run("alias-redirect", func(t *testing.T) {
		out, dir := runWithDir(t, `alias a='echo hi'; a >out.txt`)
		if out != "" {
			t.Fatalf("别名重定向后可见输出应为空: %q", out)
		}
		if got := strings.TrimSpace(readAt(t, dir, "out.txt")); got != "hi" {
			t.Fatalf("out.txt = %q, want hi", got)
		}
	})
}
