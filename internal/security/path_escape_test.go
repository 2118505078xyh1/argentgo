package security

import "testing"

// TestLooksLikePathEscapeTraversal 覆盖 .. 段的正斜杠、反斜杠与混合写法，
// 同时确认以点开头的普通名字和名字里含 .. 的文件不会被误报。
func TestLooksLikePathEscapeTraversal(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"bare parent", "..", true},
		{"leading parent", "../secret.txt", true},
		{"leading parent with dot", "./../secret.txt", true},
		{"nested parent", "src/../../secret.txt", true},
		{"trailing parent", "src/..", true},
		{"middle parent", "src/../secret.txt", true},
		{"backslash parent", `..\secret.txt`, true},
		{"backslash nested parent", `src\..\..\secret.txt`, true},
		{"mixed separators parent", `src/..\secret.txt`, true},
		{"mixed separators parent reverse", `src\../secret.txt`, true},
		{"parent after spaces", "  ../secret.txt  ", true},

		{"single dot", ".", false},
		{"dot prefix", "./src/main.go", false},
		{"hidden file", ".gitignore", false},
		{"hidden directory", ".config/app.json", false},
		{"dotdot inside name", "a..b/report.txt", false},
		{"three dots", ".../notes.txt", false},
		{"trailing dots", "report..", false},
		{"dot in version", "v1.2.3/main.go", false},
	}
	for _, tc := range cases {
		if got := looksLikePathEscape(tc.path); got != tc.want {
			t.Fatalf("%s (%q): got %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestLooksLikePathEscapeAbsoluteAndUNC 覆盖 Unix 绝对路径、Windows 根目录
// 与盘符路径，以及反斜杠/正斜杠/混合的 UNC 路径。
func TestLooksLikePathEscapeAbsoluteAndUNC(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"unix absolute", "/etc/passwd", true},
		{"unix absolute file", "/tmp/payload.sh", true},
		{"windows rooted", `\Windows\System32\drivers\etc\hosts`, true},
		{"windows drive backslash", `C:\Windows\System32`, true},
		{"windows drive slash", "C:/Windows/System32", true},
		{"windows drive relative", "D:payload.exe", true},
		{"lowercase drive", `d:\data\secret.txt`, true},
		{"unc backslash", `\\server\share\file.txt`, true},
		{"unc slash", "//server/share/file.txt", true},
		{"unc mixed", `\\server/share\file.txt`, true},
		{"device namespace", `\\?\C:\Windows\System32`, true},

		{"relative file", "src/main.go", false},
		{"relative nested", "a/b/c/d.txt", false},
		{"relative with dashes", "my-project/pkg/mod.go", false},
	}
	for _, tc := range cases {
		if got := looksLikePathEscape(tc.path); got != tc.want {
			t.Fatalf("%s (%q): got %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestLooksLikePathEscapeEncodedForms 覆盖百分号编码、多重编码、过宽 UTF-8
// 编码以及 NUL 字节。编码后的合法内容不应被误报。
func TestLooksLikePathEscapeEncodedForms(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"encoded dotdot", "%2e%2e/secret.txt", true},
		{"encoded dotdot uppercase", "%2E%2E%2Fsecret.txt", true},
		{"encoded slash after dotdot", "..%2fsecret.txt", true},
		{"encoded backslash after dotdot", `..%5csecret.txt`, true},
		{"encoded separator before dotdot", "src%2f..%2fsecret.txt", true},
		{"encoded mixed separator", `src%5c..%2fsecret.txt`, true},
		{"encoded leading slash", "%2fetc%2fpasswd", true},
		{"double encoded dotdot", "%252e%252e%252fsecret.txt", true},
		{"triple encoded dotdot", "%25252e%25252e%25252fsecret.txt", true},
		{"overlong two byte dotdot", "%c0%ae%c0%ae/secret.txt", true},
		{"overlong three byte dotdot", "%e0%80%ae%e0%80%ae%2fsecret.txt", true},
		{"overlong four byte dotdot", "%f0%80%80%ae%f0%80%80%ae/secret.txt", true},
		{"raw overlong dotdot", "\xc0\xae\xc0\xae/secret.txt", true},
		{"null byte", "safe\x00.txt", true},
		{"encoded null byte", "safe%00.txt", true},
		{"encoded leading null", "%00safe.txt", true},

		{"encoded space", "report%20final.txt", false},
		{"encoded plus", "a+b%2bc.txt", false},
		{"incomplete percent", "100%.txt", false},
		{"invalid percent", "%zz/report.txt", false},
		{"percent in name", "50%25done.txt", false},
		{"encoded equals", "query%3dvalue.txt", false},
	}
	for _, tc := range cases {
		if got := looksLikePathEscape(tc.path); got != tc.want {
			t.Fatalf("%s (%q): got %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestLooksLikePathEscapeAllowsCJKPaths 确认 CJK 文件名与编码后的 CJK
// 路径都不会因为包含非 ASCII 字节而被误判为逃逸。
func TestLooksLikePathEscapeAllowsCJKPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"chinese directories", "文档/报告.txt", false},
		{"chinese with spaces", "项目资料/最终 报告 (2).md", false},
		{"japanese directories", "日本語/ファイル.txt", false},
		{"korean directory", "문서/보고서.txt", false},
		{"cjk with dots", "版本.一.二/说明.txt", false},
		{"encoded cjk", "%E4%B8%AD%E6%96%87/报告.txt", false},
		{"encoded cjk with encoded space", "%E6%97%A5%E6%9C%AC%E8%AA%9E%20%E3%83%95%E3%82%A1%E3%82%A4%E3%83%AB.txt", false},
		{"cjk escape still caught", "文档/../密钥.txt", true},
		{"encoded cjk escape still caught", "%E6%96%87%E6%A1%A3%2f..%2f%E5%AF%86%E9%92%A5.txt", true},
	}
	for _, tc := range cases {
		if got := looksLikePathEscape(tc.path); got != tc.want {
			t.Fatalf("%s (%q): got %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestLooksLikePathEscapeIsPure 验证该函数只依赖入参：不读取文件系统，
// 重复调用结果一致，且不修改传入的字符串。
func TestLooksLikePathEscapeIsPure(t *testing.T) {
	for _, path := range []string{"src/main.go", "../secret.txt", `\\server\share\x`, "%2e%2e/x", "文档/报告.txt"} {
		first := looksLikePathEscape(path)
		second := looksLikePathEscape(path)
		if first != second {
			t.Fatalf("%q: repeated calls disagree: %v then %v", path, first, second)
		}
	}
	if !looksLikePathEscape("a/../b") {
		t.Fatal("pure check must not depend on whether the files exist")
	}
	if looksLikePathEscape("does-not-exist/report.txt") {
		t.Fatal("pure check must not require the path to exist on disk")
	}
}

// TestDecodePercentEscapes 直接覆盖百分号解码助手，确认非法转义被保留、
// 大小写十六进制都被识别。
func TestDecodePercentEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain.txt", "plain.txt"},
		{"%2e%2e", ".."},
		{"%2E%2E%2F", "../"},
		{"%5c", `\`},
		{"%00x", "\x00x"},
		{"%zz", "%zz"},
		{"%2", "%2"},
		{"a%25b", "a%b"},
	}
	for _, tc := range cases {
		if got := decodePercentEscapes(tc.in); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCollapseOverlongASCII 确认只有过宽的 ASCII 编码被压回单字节，
// 合法 UTF-8（含 CJK）保持原样。
func TestCollapseOverlongASCII(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"\xc0\xae\xc0\xae", ".."},
		{"\xe0\x80\xaf", "/"},
		{"\xf0\x80\x81\x9c", `\`},
		{"中文", "中文"},
		// E0 A0 80 是 U+0800 的合法编码，不能因为以 E0 开头就被改写。
		{"\xe0\xa0\x80", "\xe0\xa0\x80"},
	}
	for _, tc := range cases {
		if got := collapseOverlongASCII(tc.in); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}
