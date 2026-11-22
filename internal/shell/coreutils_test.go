package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newCoreInterp(dir string) *Interp {
	return &Interp{Vars: map[string]string{}, Cwd: dir}
}

// callBuiltin 直接调内建分派，以便同时断言 stdout/stderr/LastStatus。
func callBuiltin(t *testing.T, it *Interp, name string, args []string, stdin string) (string, string, int) {
	t.Helper()
	out, stderr, err := it.runBuiltin(name, args, stdin)
	if err != nil {
		t.Fatalf("%s %v: unexpected error %v", name, args, err)
	}
	return out, stderr, it.LastStatus
}

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------- grep：真正则 ----------

func TestGrepUsesRealRegex(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		args   []string
		stdin  string
		want   string
		status int
		stderr string
	}{
		{"dot is regex not glob", []string{"a.c"}, "abc\naxc\na.c\n", "abc\naxc\na.c\n", 0, ""},
		{"anchor", []string{"^a"}, "ba\nab\n", "ab\n", 0, ""},
		{"character class", []string{"[0-9]+"}, "a1\nb\nc22\n", "a1\nc22\n", 0, ""},
		{"alternation needs no -E", []string{"cat|dog"}, "bird\ncat\ndog\n", "cat\ndog\n", 0, ""},
		{"ignore case", []string{"-i", "HELLO"}, "hello\nHi\n", "hello\n", 0, ""},
		{"invert", []string{"-v", "a"}, "a\nb\na\n", "b\n", 0, ""},
		{"line numbers", []string{"-n", "a"}, "a\nb\na\n", "1:a\n3:a\n", 0, ""},
		{"combined flags -ni", []string{"-ni", "A"}, "a\nB\na\n", "1:a\n3:a\n", 0, ""},
		{"extended flag", []string{"-E", "a|b"}, "c\na\nb\n", "a\nb\n", 0, ""},
		{"fixed strings", []string{"-F", "a.c"}, "abc\na.c\n", "a.c\n", 0, ""},
		{"quiet", []string{"-q", "a"}, "a\n", "", 0, ""},
		{"count matches", []string{"-c", "a"}, "a\nb\na\n", "2\n", 0, ""},
		{"count zero", []string{"-c", "zzz"}, "a\n", "0\n", 1, ""},
		{"no match", []string{"zzz"}, "a\n", "", 1, ""},
		{"bad regex", []string{"("}, "a\n", "", 2, "invalid pattern"},
		{"unsupported short flag", []string{"-P", "a"}, "a\n", "", 2, "invalid option"},
		{"unsupported long flag", []string{"--color=never", "a"}, "a\n", "", 2, "unsupported long option"},
		{"missing pattern", nil, "a\n", "", 2, "missing pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := newCoreInterp(dir)
			out, stderr, status := callBuiltin(t, it, "grep", tc.args, tc.stdin)
			if out != tc.want || status != tc.status {
				t.Fatalf("grep %v => out=%q status=%d, want out=%q status=%d",
					tc.args, out, status, tc.want, tc.status)
			}
			if tc.stderr != "" && !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("grep %v stderr=%q, want contains %q", tc.args, stderr, tc.stderr)
			}
		})
	}
}

func TestGrepFilesAndRecursive(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "f1.txt", "hit\nother\n")
	writeFixture(t, dir, "f2.txt", "nothing\n")
	writeFixture(t, dir, "资料/深层/a.txt", "命中\n")
	writeFixture(t, dir, "资料/b.txt", "无关\n")

	cases := []struct {
		name   string
		args   []string
		want   string
		status int
	}{
		{"single file no prefix", []string{"hit", "f1.txt"}, "hit\n", 0},
		{"filenames only", []string{"-l", "hit", "f1.txt", "f2.txt"}, "f1.txt\n", 0},
		{"multi file prefix and line number", []string{"-n", "hit", "f1.txt", "f2.txt"}, "f1.txt:1:hit\n", 0},
		{"recursive CJK path", []string{"-r", "命中", "资料"}, "资料/深层/a.txt:命中\n", 0},
		{"recursive with invert", []string{"-rv", "无关", "资料"}, "资料/深层/a.txt:命中\n", 0},
		{"directory without -r", []string{"hit", "资料"}, "", 2},
		{"workspace escape", []string{"x", "../secret"}, "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := newCoreInterp(dir)
			out, stderr, status := callBuiltin(t, it, "grep", tc.args, "")
			if out != tc.want || status != tc.status {
				t.Fatalf("grep %v => out=%q status=%d stderr=%q, want out=%q status=%d",
					tc.args, out, status, stderr, tc.want, tc.status)
			}
		})
	}
}

// ---------- ls ----------

func TestLsFlags(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.txt", "hello\n")
	writeFixture(t, dir, "b.txt", "world\n")
	writeFixture(t, dir, ".hidden", "secret\n")
	writeFixture(t, dir, "子目录/inner.txt", "x\n")

	// 默认隐藏点文件；-a/-l/-R 的格式含时间戳，单独做包含式断言。
	out, _, status := callBuiltin(t, newCoreInterp(dir), "ls", nil, "")
	if out != "a.txt\nb.txt\n子目录\n" || status != 0 {
		t.Fatalf("ls default => %q status=%d", out, status)
	}

	out, _, status = callBuiltin(t, newCoreInterp(dir), "ls", []string{"-a"}, "")
	for _, frag := range []string{".\n", "..\n", ".hidden\n", "a.txt\n", "b.txt\n", "子目录\n"} {
		if !strings.Contains(out, frag) {
			t.Fatalf("ls -a => %q, missing %q", out, frag)
		}
	}

	out, _, status = callBuiltin(t, newCoreInterp(dir), "ls", []string{"-l", "a.txt"}, "")
	if status != 0 || !strings.HasPrefix(out, "-rw-") || !strings.Contains(out, "6 ") || !strings.Contains(out, "a.txt") {
		t.Fatalf("ls -l => %q status=%d", out, status)
	}

	out, _, status = callBuiltin(t, newCoreInterp(dir), "ls", []string{"-R", "."}, "")
	if status != 0 || !strings.Contains(out, ".:") || !strings.Contains(out, "./子目录:") || !strings.Contains(out, "inner.txt") {
		t.Fatalf("ls -R => %q status=%d", out, status)
	}
	// 组合短选项 -laR 同时生效。
	out, _, status = callBuiltin(t, newCoreInterp(dir), "ls", []string{"-laR", "."}, "")
	if status != 0 || !strings.Contains(out, ".hidden") || !strings.Contains(out, "./子目录:") ||
		!strings.Contains(out, "-rw-") {
		t.Fatalf("ls -laR => %q status=%d", out, status)
	}

	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "ls", []string{"-Z"}, "")
	if status != 2 || !strings.Contains(stderr, "invalid option") {
		t.Fatalf("ls -Z => status=%d stderr=%q", status, stderr)
	}
	_, stderr, status = callBuiltin(t, newCoreInterp(dir), "ls", []string{"../outside"}, "")
	if status != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("ls escape => status=%d stderr=%q", status, stderr)
	}
}

// ---------- cat ----------

func TestCatMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "一.txt", "你好\n")
	writeFixture(t, dir, "二.txt", "世界\n")

	out, _, status := callBuiltin(t, newCoreInterp(dir), "cat", []string{"一.txt", "二.txt"}, "")
	if out != "你好\n世界\n" || status != 0 {
		t.Fatalf("cat multi => %q status=%d", out, status)
	}
	out, _, status = callBuiltin(t, newCoreInterp(dir), "cat", []string{"一.txt", "-", "二.txt"}, "中\n")
	if out != "你好\n中\n世界\n" || status != 0 {
		t.Fatalf("cat with stdin => %q status=%d", out, status)
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "cat", []string{"../secret"}, "")
	if status != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("cat escape => status=%d stderr=%q", status, stderr)
	}
}

// ---------- wc ----------

func TestWcAlignment(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "f1", "a\nb\nc\n")
	writeFixture(t, dir, "f2", "x\n")
	writeFixture(t, dir, "中文.txt", "你好\n")

	cases := []struct {
		name   string
		args   []string
		stdin  string
		want   string
		status int
	}{
		{"one file -l", []string{"-l", "f1"}, "", "3 f1\n", 0},
		{"two files total", []string{"-l", "f1", "f2"}, "", "3 f1\n1 f2\n4 total\n", 0},
		{"two columns", []string{"-lw", "f1"}, "", "3 3 f1\n", 0},
		{"stdin no name", []string{"-l"}, "hello\n", "1\n", 0},
		{"CJK bytes", []string{"中文.txt"}, "", "1 1 7 中文.txt\n", 0},
		{"unsupported flag", []string{"-Z"}, "", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, status := callBuiltin(t, newCoreInterp(dir), "wc", tc.args, tc.stdin)
			if out != tc.want || status != tc.status {
				t.Fatalf("wc %v => %q status=%d, want %q status=%d", tc.args, out, status, tc.want, tc.status)
			}
		})
	}
	// 位数不同时右对齐（GNU 用最大数字宽度）。
	writeFixture(t, dir, "big", strings.Repeat("x\n", 123))
	out, _, _ := callBuiltin(t, newCoreInterp(dir), "wc", []string{"-l", "big", "f2"}, "")
	if out != "123 big\n  1 f2\n124 total\n" {
		t.Fatalf("wc alignment => %q", out)
	}
}

// ---------- head / tail ----------

func TestHeadTailPreserveTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "f", "a\nb\nc\nd\n")
	writeFixture(t, dir, "noeol", "a\nb")
	cases := []struct {
		cmd  string
		want string
	}{
		{`head -n 1 f`, "a\n"},
		{`head -3 f`, "a\nb\nc\n"},
		{`head -n -1 f`, "a\nb\nc\n"},
		{`tail -n 2 f`, "c\nd\n"},
		{`tail -2 f`, "c\nd\n"},
		{`tail -n +3 f`, "c\nd\n"},
		{`head -1 noeol`, "a\n"},
		{`tail -1 noeol`, "b"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, err := Run(tc.cmd, testOptions(dir))
			if err != nil {
				t.Fatal(err)
			}
			if got.Output != tc.want {
				t.Fatalf("%q => %q, want %q", tc.cmd, got.Output, tc.want)
			}
		})
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "tail", []string{"-f", "f"}, "")
	if status != 2 || !strings.Contains(stderr, "invalid option") {
		t.Fatalf("tail -f => status=%d stderr=%q", status, stderr)
	}
	_, stderr, status = callBuiltin(t, newCoreInterp(dir), "head", []string{"../secret"}, "")
	if status != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("head escape => status=%d stderr=%q", status, stderr)
	}
}

func TestHeadTailMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "一.txt", "甲\n乙\n")
	writeFixture(t, dir, "二.txt", "丙\n丁\n")
	out, _, status := callBuiltin(t, newCoreInterp(dir), "head", []string{"-n", "1", "一.txt", "二.txt"}, "")
	want := "==> 一.txt <==\n甲\n\n==> 二.txt <==\n丙\n"
	if out != want || status != 0 {
		t.Fatalf("head multi => %q status=%d, want %q", out, status, want)
	}
}

// ---------- mkdir / rm / touch ----------

func TestMkdirRmTouch(t *testing.T) {
	dir := t.TempDir()
	it := newCoreInterp(dir)

	_, _, status := callBuiltin(t, it, "mkdir", []string{"-p", "项目/子目录"}, "")
	if status != 0 {
		t.Fatalf("mkdir -p status=%d", status)
	}
	if info, err := os.Stat(filepath.Join(dir, "项目", "子目录")); err != nil || !info.IsDir() {
		t.Fatalf("mkdir -p did not create CJK dir: %v", err)
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "mkdir", []string{"缺/父"}, "")
	if status != 1 || !strings.Contains(stderr, "缺/父") {
		t.Fatalf("mkdir without -p => status=%d stderr=%q", status, stderr)
	}

	writeFixture(t, dir, "项目/子目录/a.txt", "x\n")
	_, _, status = callBuiltin(t, newCoreInterp(dir), "touch", []string{"项目/新.txt"}, "")
	if status != 0 {
		t.Fatalf("touch status=%d", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "项目", "新.txt")); err != nil {
		t.Fatalf("touch did not create file: %v", err)
	}
	before := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "项目", "新.txt"), before, before)
	_, _, status = callBuiltin(t, newCoreInterp(dir), "touch", []string{"项目/新.txt"}, "")
	if status != 0 {
		t.Fatalf("touch existing status=%d", status)
	}
	if info, _ := os.Stat(filepath.Join(dir, "项目", "新.txt")); !info.ModTime().After(before) {
		t.Fatal("touch should update mtime")
	}
	_, _, status = callBuiltin(t, newCoreInterp(dir), "touch", []string{"-c", "不会创建.txt"}, "")
	if status != 0 {
		t.Fatalf("touch -c status=%d", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "不会创建.txt")); !os.IsNotExist(err) {
		t.Fatal("touch -c must not create")
	}

	_, _, status = callBuiltin(t, newCoreInterp(dir), "rm", []string{"-rf", "项目"}, "")
	if status != 0 {
		t.Fatalf("rm -rf status=%d", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "项目")); !os.IsNotExist(err) {
		t.Fatal("rm -rf did not remove CJK dir")
	}
	_, _, status = callBuiltin(t, newCoreInterp(dir), "rm", []string{"-f", "不存在.txt"}, "")
	if status != 0 {
		t.Fatalf("rm -f missing status=%d", status)
	}
	_, stderr, status = callBuiltin(t, newCoreInterp(dir), "rm", []string{"-rf", "../outside"}, "")
	if status != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("rm escape => status=%d stderr=%q", status, stderr)
	}
	_, stderr, status = callBuiltin(t, newCoreInterp(dir), "rm", []string{"-rf", "."}, "")
	if status != 1 || !strings.Contains(stderr, "workspace root") {
		t.Fatalf("rm root => status=%d stderr=%q", status, stderr)
	}
}

// TestMkdirRmTouchFlags 以表驱动覆盖三个文件管理命令的标志与退出码。
func TestMkdirRmTouchFlags(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		args   []string
		status int
		check  func(t *testing.T, dir string)
	}{
		{"mkdir -p", "mkdir", []string{"-p", "甲/乙"}, 0, func(t *testing.T, dir string) {
			if info, err := os.Stat(filepath.Join(dir, "甲", "乙")); err != nil || !info.IsDir() {
				t.Fatalf("mkdir -p failed: %v", err)
			}
		}},
		{"mkdir without -p", "mkdir", []string{"丙/丁"}, 1, nil},
		{"mkdir bad flag", "mkdir", []string{"-Z", "x"}, 2, nil},
		{"touch create", "touch", []string{"新建.txt"}, 0, func(t *testing.T, dir string) {
			if _, err := os.Stat(filepath.Join(dir, "新建.txt")); err != nil {
				t.Fatalf("touch failed: %v", err)
			}
		}},
		{"touch -c skip", "touch", []string{"-c", "未建.txt"}, 0, func(t *testing.T, dir string) {
			if _, err := os.Stat(filepath.Join(dir, "未建.txt")); !os.IsNotExist(err) {
				t.Fatal("touch -c must not create")
			}
		}},
		{"touch bad flag", "touch", []string{"-Z", "x"}, 2, nil},
		{"rm file", "rm", []string{"待删.txt"}, 0, func(t *testing.T, dir string) {
			if _, err := os.Stat(filepath.Join(dir, "待删.txt")); !os.IsNotExist(err) {
				t.Fatal("rm should delete file")
			}
		}},
		{"rm -r dir", "rm", []string{"-r", "待删目录"}, 0, func(t *testing.T, dir string) {
			if _, err := os.Stat(filepath.Join(dir, "待删目录")); !os.IsNotExist(err) {
				t.Fatal("rm -r should delete dir")
			}
		}},
		{"rm dir without -r", "rm", []string{"待删目录"}, 1, nil},
		{"rm bad flag", "rm", []string{"-Z", "x"}, 2, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "待删.txt", "x\n")
			writeFixture(t, dir, "待删目录/a.txt", "x\n")
			_, _, status := callBuiltin(t, newCoreInterp(dir), tc.cmd, tc.args, "")
			if status != tc.status {
				t.Fatalf("%s %v => status=%d, want %d", tc.cmd, tc.args, status, tc.status)
			}
			if tc.check != nil {
				tc.check(t, dir)
			}
		})
	}
}

// ---------- cp / mv / tee ----------

func TestCpMvTee(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "源文件.txt", "内容\n")
	writeFixture(t, dir, "树/子/深层.txt", "深\n")
	writeFixture(t, dir, "树/顶.txt", "顶\n")

	it := newCoreInterp(dir)
	_, _, status := callBuiltin(t, it, "cp", []string{"源文件.txt", "副本.txt"}, "")
	if status != 0 {
		t.Fatalf("cp status=%d", status)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "副本.txt")); err != nil || string(data) != "内容\n" {
		t.Fatalf("cp content=%q err=%v", data, err)
	}
	_, _, status = callBuiltin(t, newCoreInterp(dir), "cp", []string{"-r", "树", "树副本"}, "")
	if status != 0 {
		t.Fatalf("cp -r status=%d", status)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "树副本", "子", "深层.txt")); err != nil || string(data) != "深\n" {
		t.Fatalf("cp -r content=%q err=%v", data, err)
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "cp", []string{"树", "树直接"}, "")
	if status != 1 || !strings.Contains(stderr, "omitting directory") {
		t.Fatalf("cp dir without -r => status=%d stderr=%q", status, stderr)
	}

	_, _, status = callBuiltin(t, newCoreInterp(dir), "mv", []string{"副本.txt", "移动后.txt"}, "")
	if status != 0 {
		t.Fatalf("mv status=%d", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "副本.txt")); !os.IsNotExist(err) {
		t.Fatal("mv should remove source")
	}
	if _, err := os.Stat(filepath.Join(dir, "移动后.txt")); err != nil {
		t.Fatal("mv should create target")
	}
	writeFixture(t, dir, "保留.txt", "旧\n")
	_, _, status = callBuiltin(t, newCoreInterp(dir), "mv", []string{"-n", "移动后.txt", "保留.txt"}, "")
	if status != 0 {
		t.Fatalf("mv -n status=%d", status)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "保留.txt")); string(data) != "旧\n" {
		t.Fatalf("mv -n overwrote target: %q", data)
	}

	out, _, status := callBuiltin(t, newCoreInterp(dir), "tee", []string{"输出.txt"}, "第一行\n")
	if out != "第一行\n" || status != 0 {
		t.Fatalf("tee => %q status=%d", out, status)
	}
	out, _, status = callBuiltin(t, newCoreInterp(dir), "tee", []string{"-a", "输出.txt"}, "第二行\n")
	if out != "第二行\n" || status != 0 {
		t.Fatalf("tee -a => %q status=%d", out, status)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "输出.txt")); string(data) != "第一行\n第二行\n" {
		t.Fatalf("tee append content=%q", data)
	}
	for _, tc := range []struct {
		cmd  string
		args []string
	}{
		{"cp", []string{"源文件.txt", "../outside"}},
		{"mv", []string{"源文件.txt", "../outside"}},
		{"tee", []string{"../outside"}},
		{"touch", []string{"../outside"}},
	} {
		_, stderr, status := callBuiltin(t, newCoreInterp(dir), tc.cmd, tc.args, "x\n")
		if status == 0 || !strings.Contains(stderr, "outside the workspace") {
			t.Fatalf("%s escape => status=%d stderr=%q", tc.cmd, status, stderr)
		}
	}
}

// ---------- printf / test ----------

func TestPrintfFormats(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"repeat format", []string{"%s\n", "甲", "乙"}, "甲\n乙\n"},
		{"decimal", []string{"%d\n", "42"}, "42\n"},
		{"zero pad", []string{"a=%05d|%-5s|\n", "-12", "ab"}, "a=-0012|ab   |\n"},
		{"shell quote", []string{"%q\n", "a b"}, "a\\ b\n"},
		{"quote apostrophe", []string{"%q\n", "it's"}, "it\\'s\n"},
		{"quote control", []string{"%q\n", "x\ny"}, "$'x\\ny'\n"},
		{"escape tab", []string{"a\tb\n"}, "a\tb\n"},
		{"octal escape", []string{`\033[2K\r`}, "\x1b[2K\r"},
		{"hex escape", []string{`\x41\n`}, "A\n"},
		{"literal percent", []string{"100%%\n"}, "100%\n"},
		{"no arguments", []string{"hi\n"}, "hi\n"},
		{"CJK", []string{"%s=%d\n", "中文", "7"}, "中文=7\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, status := callBuiltin(t, newCoreInterp(dir), "printf", tc.args, "")
			if out != tc.want || status != 0 {
				t.Fatalf("printf %q => %q status=%d stderr=%q, want %q", tc.args, out, status, stderr, tc.want)
			}
		})
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "printf", []string{"%f", "1"}, "")
	if status != 1 || !strings.Contains(stderr, "unsupported format") {
		t.Fatalf("printf %%f => status=%d stderr=%q", status, stderr)
	}
}

func TestTestBuiltin(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "文件.txt", "内容\n")
	writeFixture(t, dir, "目录/子", "x\n")
	cases := []struct {
		name   string
		args   []string
		status int
	}{
		{"file exists", []string{"-f", "文件.txt"}, 0},
		{"dir exists", []string{"-d", "目录"}, 0},
		{"any exists", []string{"-e", "文件.txt"}, 0},
		{"missing", []string{"-e", "缺失"}, 1},
		{"string equal", []string{"甲", "=", "甲"}, 0},
		{"string not equal", []string{"甲", "!=", "乙"}, 0},
		{"empty string", []string{"-z", ""}, 0},
		{"non empty", []string{"-n", "x"}, 0},
		{"integer compare", []string{"3", "-lt", "5"}, 0},
		{"negation", []string{"!", "-f", "缺失"}, 0},
		{"bracket form", []string{"-f", "文件.txt", "]"}, 0},
		{"no args is false", nil, 1},
		{"unknown operator", []string{"-Q", "x"}, 2},
		{"workspace escape", []string{"-e", "../secret"}, 2},
		{"test form needs no bracket", []string{"-f", "文件.txt"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "test"
			args := tc.args
			if tc.name == "bracket form" {
				name = "["
			}
			_, _, status := callBuiltin(t, newCoreInterp(dir), name, args, "")
			if status != tc.status {
				t.Fatalf("%s %v => status=%d, want %d", name, args, status, tc.status)
			}
		})
	}
	_, stderr, status := callBuiltin(t, newCoreInterp(dir), "[", []string{"-f", "文件.txt"}, "")
	if status != 2 || !strings.Contains(stderr, "missing ']'") {
		t.Fatalf("[ without ] => status=%d stderr=%q", status, stderr)
	}
}

func TestCoreutilsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	got, err := Run("mkdir -p 目录 && printf '%s\\n' 你好 > 目录/文件.txt && wc -l 目录/文件.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "1 目录/文件.txt\n" {
		t.Fatalf("pipeline => %q", got.Output)
	}
	got, err = Run("echo hello | grep -n h | tee 结果.txt && cat 结果.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "1:hello\n1:hello\n" {
		t.Fatalf("grep | tee | cat => %q", got.Output)
	}
	got, err = Run("test -f 结果.txt && echo ok", testOptions(dir))
	if err != nil || got.Output != "ok\n" {
		t.Fatalf("test && echo => %q err=%v", got.Output, err)
	}
}
