package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtraBuiltins(t *testing.T) {
	t.Run("cd-dash", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "sub"), 0755)
		res, err := Run(`cd sub && cd - && pwd`, controlOptions(dir))
		if err != nil {
			t.Fatal(err)
		}
		// cd - 打印新目录（根），随后 pwd 也应输出根。
		if got := strings.TrimSpace(res.Output); !strings.Contains(got, dir) {
			t.Fatalf("cd - 输出 = %q, want containing %q", got, dir)
		}
	})

	t.Run("read", func(t *testing.T) {
		res, err := Run(`echo "a b" | read x y; echo $x $y`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Output); got != "a b" {
			t.Fatalf("read 输出 = %q, want 'a b'", got)
		}
	})

	t.Run("eval", func(t *testing.T) {
		res, err := Run(`eval "echo hi"`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Output); got != "hi" {
			t.Fatalf("eval 输出 = %q, want hi", got)
		}
	})

	t.Run("alias", func(t *testing.T) {
		res, err := Run(`alias ll='echo alias-ok'; ll`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Output); got != "alias-ok" {
			t.Fatalf("alias 输出 = %q, want alias-ok", got)
		}
	})

	t.Run("alias-no-recursion", func(t *testing.T) {
		res, err := Run(`alias ls='echo once'; ls`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(res.Output); got != "once" {
			t.Fatalf("alias 自引用应只展开一轮: %q", got)
		}
	})

	t.Run("source", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "s.sh"), []byte("echo sourced\nX=42\n"), 0644)
		res, err := Run(`source s.sh; echo $X`, controlOptions(dir))
		if err != nil {
			t.Fatal(err)
		}
		if got := fields(res.Output); got != "sourced 42" {
			t.Fatalf("source 输出 = %q, want 'sourced 42'", got)
		}
	})

	t.Run("history", func(t *testing.T) {
		res, err := Run(`echo one; echo two; history`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Output, "echo one") || !strings.Contains(res.Output, "history") {
			t.Fatalf("history 输出 = %q", res.Output)
		}
	})

	t.Run("env-prefix", func(t *testing.T) {
		res, err := Run(`env X=1 env`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Output, "X=1") {
			t.Fatalf("env 前缀未生效: %q", res.Output)
		}
	})

	t.Run("which-builtin", func(t *testing.T) {
		res, err := Run(`which echo`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(res.Output) != "echo" {
			t.Fatalf("which echo = %q, want echo", res.Output)
		}
	})

	t.Run("exit-stops-script", func(t *testing.T) {
		res, err := Run(`echo before; exit 5; echo after`, controlOptions(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Output, "after") {
			t.Fatalf("exit 后不应继续执行: %q", res.Output)
		}
		if !strings.Contains(res.Output, "before") {
			t.Fatalf("exit 前输出丢失: %q", res.Output)
		}
	})
}
