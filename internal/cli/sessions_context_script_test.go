package cli

import (
	"strings"
	"testing"
)

func TestRenderContextScriptBasicFormat(t *testing.T) {
	got := renderContextScript([]string{"b.go", "a.go"}, []string{"c.go"})
	want := contextScriptHeader +
		"/drop\n" +
		"/add a.go\n" +
		"/add b.go\n" +
		"/read-only c.go\n"
	if got != want {
		t.Fatalf("脚本格式不符：\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderContextScriptEmpty(t *testing.T) {
	got := renderContextScript(nil, nil)
	want := contextScriptHeader + "/drop\n"
	if got != want {
		t.Fatalf("空文件集脚本 = %q，期望 %q", got, want)
	}
}

func TestRenderContextScriptSortsAndDedupes(t *testing.T) {
	files := []string{"z.go", "a.go", "z.go", "", "m.go"}
	readOnly := []string{"y.go", "b.go", "y.go"}
	got := renderContextScript(files, readOnly)
	want := contextScriptHeader +
		"/drop\n" +
		"/add a.go\n" +
		"/add m.go\n" +
		"/add z.go\n" +
		"/read-only b.go\n" +
		"/read-only y.go\n"
	if got != want {
		t.Fatalf("去重或排序不符：\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderContextScriptQuotesSpacePaths(t *testing.T) {
	got := renderContextScript([]string{"dir with space/file.go"}, []string{`quoted "name".go`})
	if want := "/add \"dir with space/file.go\"\n"; !strings.Contains(got, want) {
		t.Errorf("含空格路径未加引号：%q", got)
	}
	if want := `/read-only "quoted \"name\".go"` + "\n"; !strings.Contains(got, want) {
		t.Errorf("路径内双引号未转义：%q", got)
	}
}

func TestRenderContextScriptBackslashPath(t *testing.T) {
	got := renderContextScript([]string{`src\deep\file.go`}, nil)
	if want := `/add "src\\deep\\file.go"` + "\n"; !strings.Contains(got, want) {
		t.Errorf("反斜杠未转义：%q", got)
	}
}

func TestRenderContextScriptCJK(t *testing.T) {
	got := renderContextScript([]string{"文档/中文 文件.go", "纯中文.go"}, nil)
	if want := "/add 纯中文.go\n"; !strings.Contains(got, want) {
		t.Errorf("无空格 CJK 路径应原样输出：%q", got)
	}
	if want := `/add "文档/中文 文件.go"` + "\n"; !strings.Contains(got, want) {
		t.Errorf("含空格 CJK 路径未正确加引号：%q", got)
	}
	if strings.Contains(got, `\u`) {
		t.Errorf("CJK 不得转义为 \\uXXXX：%q", got)
	}
}

func TestRenderContextScriptStable(t *testing.T) {
	first := renderContextScript([]string{"b", "a", "c"}, []string{"y", "x"})
	second := renderContextScript([]string{"c", "b", "a"}, []string{"x", "y"})
	if first != second {
		t.Fatalf("输入顺序变化导致脚本变化：\n%q\n%q", first, second)
	}
	if again := renderContextScript([]string{"a", "b", "c"}, []string{"x", "y"}); first != again {
		t.Fatalf("重复调用结果不稳定：\n%q\n%q", first, again)
	}
}
