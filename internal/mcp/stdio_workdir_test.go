package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sameDir 比较两个路径是否指向同一目录，消除符号链接与 Windows 大小写差异。
func sameDir(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return strings.EqualFold(absA, absB)
}

// cwdViaHelper 让假 stdio 服务器报告它自己子进程的工作目录。
func cwdViaHelper(t *testing.T, spec Spec) string {
	t.Helper()
	c, err := Connect(context.Background(), spec)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	text, err := c.CallTool(context.Background(), "cwd", nil)
	if err != nil {
		t.Fatalf("call cwd: %v", err)
	}
	return strings.TrimSpace(text)
}

// TestStdioWorkDir 守护 Spec.Dir 的 stdio 工作目录接线：
// 空值保持 agent 的 cwd，非空值成为子进程 cwd，非 ASCII 路径同样生效。
func TestStdioWorkDir(t *testing.T) {
	agentCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	t.Run("empty keeps agent cwd", func(t *testing.T) {
		got := cwdViaHelper(t, helperSpec("echo", 5*time.Second))
		if !sameDir(got, agentCwd) {
			t.Fatalf("empty Dir: cwd = %q, want agent cwd %q", got, agentCwd)
		}
	})

	t.Run("set dir", func(t *testing.T) {
		dir := t.TempDir()
		spec := helperSpec("echo", 5*time.Second)
		spec.Dir = dir
		got := cwdViaHelper(t, spec)
		if !sameDir(got, dir) {
			t.Fatalf("Dir set: cwd = %q, want %q", got, dir)
		}
	})

	t.Run("cjk path", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "插件目录", "工具")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		spec := helperSpec("echo", 5*time.Second)
		spec.Dir = dir
		got := cwdViaHelper(t, spec)
		if !sameDir(got, dir) {
			t.Fatalf("CJK Dir: cwd = %q, want %q", got, dir)
		}
	})
}
