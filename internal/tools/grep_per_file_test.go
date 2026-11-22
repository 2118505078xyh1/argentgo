package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGrepExMaxPerFilePreservesDiversity(t *testing.T) {
	root := t.TempDir()
	// 文件 A 有 200 个命中，文件 B 只有 3 个。
	a := ""
	for i := 0; i < 200; i++ {
		a += "NEEDLE line\n"
	}
	b := "NEEDLE 1\nNEEDLE 2\nNEEDLE 3\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}

	// 无单文件上限：全局 50 全被 A 吃掉，B 不出现。
	all, _, err := GrepEx(GrepExOptions{Pattern: "NEEDLE", Path: root, Literal: true, HeadLimit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Path == "b.txt" {
			t.Fatalf("without cap, b.txt should not appear (all budget eaten by a.txt)")
		}
	}

	// 单文件上限 5：A 最多 5 条，B 的 3 条应出现。
	capped, _, err := GrepEx(GrepExOptions{Pattern: "NEEDLE", Path: root, Literal: true, HeadLimit: 50, MaxPerFile: 5})
	if err != nil {
		t.Fatal(err)
	}
	perFile := map[string]int{}
	for _, m := range capped {
		perFile[m.Path]++
	}
	if perFile["a.txt"] != 5 {
		t.Fatalf("a.txt count = %d, want 5", perFile["a.txt"])
	}
	if perFile["b.txt"] != 3 {
		t.Fatalf("b.txt count = %d, want 3 (diversity lost)", perFile["b.txt"])
	}
}

func TestGrepExMaxPerFileZeroKeepsBaseline(t *testing.T) {
	root := t.TempDir()
	data := "NEEDLE\nNEEDLE\nNEEDLE\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _, _ := GrepEx(GrepExOptions{Pattern: "NEEDLE", Path: root, Literal: true})
	b, _, _ := GrepEx(GrepExOptions{Pattern: "NEEDLE", Path: root, Literal: true, MaxPerFile: 0})
	if len(a) != len(b) {
		t.Fatalf("MaxPerFile=0 changed result: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Line != b[i].Line || a[i].Text != b[i].Text {
			t.Fatalf("MaxPerFile=0 changed match at %d", i)
		}
	}
}

func TestGrepPerFileLimitEnv(t *testing.T) {
	t.Setenv(grepPerFileEnv, "7")
	if got := grepPerFileLimit(); got != 7 {
		t.Fatalf("env 7 -> %d", got)
	}
	t.Setenv(grepPerFileEnv, "abc")
	if got := grepPerFileLimit(); got != 0 {
		t.Fatalf("invalid env -> %d, want 0", got)
	}
	t.Setenv(grepPerFileEnv, "")
	if got := grepPerFileLimit(); got != 0 {
		t.Fatalf("empty env -> %d, want 0", got)
	}
}
