package astedit

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRewriteSymbolTopLevelFunc(t *testing.T) {
	src := []byte(`package p

// 问候返回一个字符串。
func old(a int) int { return a + 1 }

func keep() {}
`)
	out, err := RewriteSymbol(src, "old", []byte("func fresh(a int) int { return a + 9 }"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "func fresh(a int) int { return a + 9 }") {
		t.Fatalf("替换后的函数未出现：\n%s", text)
	}
	if strings.Contains(text, "func old(") {
		t.Fatalf("旧函数仍存在：\n%s", text)
	}
	// 字节区间 splice 不应触碰其它内容：CJK 注释与无关函数必须原样保留。
	if !strings.Contains(text, "// 问候返回一个字符串。") {
		t.Fatalf("CJK 注释丢失：\n%s", text)
	}
	if !strings.Contains(text, "func keep() {}") {
		t.Fatalf("无关函数丢失：\n%s", text)
	}
}

func TestRewriteSymbolCall(t *testing.T) {
	src := []byte(`package p

func main() {
	old(1, "二")
}
`)
	out, err := RewriteSymbol(src, "old", []byte(`replacement(2)`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "replacement(2)") {
		t.Fatalf("调用未被替换：\n%s", text)
	}
	if strings.Contains(text, "old(1") {
		t.Fatalf("旧调用仍存在：\n%s", text)
	}
}

func TestRewriteSymbolReplacesAllCalls(t *testing.T) {
	src := []byte(`package p

func main() {
	old(1)
	old(2)
}
`)
	out, err := RewriteSymbol(src, "old", []byte(`fresh()`))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "fresh()"); n != 2 {
		t.Fatalf("期望替换 2 处调用，实际 %d：\n%s", n, out)
	}
	if strings.Contains(string(out), "old(") {
		t.Fatalf("仍有旧调用：\n%s", out)
	}
}

func TestRewriteSymbolNotFound(t *testing.T) {
	_, err := RewriteSymbol([]byte("package p\nfunc keep() {}\n"), "missing", []byte("x"))
	if !errors.Is(err, ErrSymbolNotFound) {
		t.Fatalf("期望 ErrSymbolNotFound，实际 %v", err)
	}
}

func TestRewriteSymbolRejectsInvalidSymbol(t *testing.T) {
	_, err := RewriteSymbol([]byte("package p\n"), "not-valid", []byte("x"))
	if !errors.Is(err, ErrInvalidSymbol) {
		t.Fatalf("期望 ErrInvalidSymbol，实际 %v", err)
	}
}

func TestRewriteSymbolRejectsInvalidReplacement(t *testing.T) {
	src := []byte("package p\nfunc old() {}\n")
	_, err := RewriteSymbol(src, "old", []byte("func ??? ("))
	if !errors.Is(err, ErrInvalidReplacement) {
		t.Fatalf("期望 ErrInvalidReplacement，实际 %v", err)
	}
}

func TestRewriteSymbolRejectsLargeFile(t *testing.T) {
	big := append([]byte("package p\n"), bytes.Repeat([]byte("// x\n"), MaxSourceBytes/4+1)...)
	_, err := RewriteSymbol(big, "x", []byte("y"))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("期望 ErrTooLarge，实际 %v", err)
	}
}

func TestFindCalls(t *testing.T) {
	src := []byte(`package p

func main() {
	foo(1, "二")
	bar()
	obj.foo(2)
}
`)
	sites, err := FindCalls(src, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 {
		t.Fatalf("期望 2 个调用点，实际 %d：%+v", len(sites), sites)
	}
	if sites[0].Line != 4 || sites[0].Args != `1, "二"` {
		t.Fatalf("第一个调用点不符：%+v", sites[0])
	}
	if sites[1].Line != 6 || sites[1].Args != "2" {
		t.Fatalf("第二个调用点不符：%+v", sites[1])
	}
	if sites[0].Column < 1 || sites[1].Column < 1 {
		t.Fatalf("列号应为 1 基：%+v", sites)
	}
}

func TestFindCallsEmptyAndInvalidSymbol(t *testing.T) {
	sites, err := FindCalls([]byte("package p\nfunc main() {}\n"), "absent")
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 0 {
		t.Fatalf("期望 0 个调用点，实际 %+v", sites)
	}
	_, err = FindCalls([]byte("package p\n"), "a-b")
	if !errors.Is(err, ErrInvalidSymbol) {
		t.Fatalf("期望 ErrInvalidSymbol，实际 %v", err)
	}
}

func TestDiff(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nB\nc\n"))
	for _, want := range []string{"-b", "+B", " a", " c"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff 缺少 %q：\n%s", want, got)
		}
	}
	if d := Diff([]byte("same\n"), []byte("same\n")); d != "" {
		t.Fatalf("相同内容应产生空 diff，实际 %q", d)
	}
}

func TestDiffCRLF(t *testing.T) {
	got := Diff([]byte("a\r\nb\r\n"), []byte("a\r\nB\r\n"))
	if strings.Contains(got, "\r") {
		t.Fatalf("diff 应归一化 CRLF：%q", got)
	}
	if !strings.Contains(got, "-b") || !strings.Contains(got, "+B") {
		t.Fatalf("diff 内容不符：\n%s", got)
	}
}
