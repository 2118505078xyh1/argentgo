package bash

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func testOptions(dir string) Options {
	return Options{
		Cwd: dir,
		Vars: map[string]string{
			"FOO": "bar", "EMPTY": "", "NAME": "world",
		},
	}
}

func TestEchoAndQuotes(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{`echo hello`, "hello\n"},
		{`echo hello world`, "hello world\n"},
		{`echo "hello world"`, "hello world\n"},
		{`echo 'hello world'`, "hello world\n"},
		{`echo a\ b`, "a b\n"},
		{`echo -n hello`, "hello"},
		{`echo ""`, "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, err := Run(tc.cmd, testOptions(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			if got.Output != tc.want {
				t.Fatalf("%q => %q, want %q", tc.cmd, got.Output, tc.want)
			}
		})
	}
}

func TestVariableExpansion(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		cmd  string
		want string
	}{
		{`echo $FOO`, "bar\n"},
		{`echo ${FOO}`, "bar\n"},
		{`echo "$FOO"`, "bar\n"},
		{`echo '$FOO'`, "$FOO\n"},
		{`echo $EMPTY`, "\n"},
		{`echo ${MISSING:-fallback}`, "fallback\n"},
		{`echo ${FOO:-fallback}`, "bar\n"},
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
}

func TestLastStatus(t *testing.T) {
	dir := t.TempDir()
	got, err := Run("false; echo $?", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "1\n" {
		t.Fatalf("false; echo $? => %q, want 1", got.Output)
	}
	got, err = Run("true && echo $?", testOptions(dir))
	if err != nil || got.Output != "0\n" {
		t.Fatalf("true && echo $? => %q err=%v", got.Output, err)
	}
}

func TestCommandSubstitution(t *testing.T) {
	dir := t.TempDir()
	got, err := Run(`echo "hello $(echo world)"`, testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "hello world\n" {
		t.Fatalf("cmd subst => %q", got.Output)
	}
	got, err = Run(`echo $(echo a b)`, testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "a b\n" {
		t.Fatalf("cmd subst split => %q", got.Output)
	}
}

func TestLogicAndPipe(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		cmd  string
		want string
	}{
		{`true && echo yes`, "yes\n"},
		{`false && echo no`, ""},
		{`false || echo yes`, "yes\n"},
		{`true || echo no`, ""},
		{`echo hi | cat`, "hi\n"}, // echo 输出带换行，cat 原样保留
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
}

func TestBuiltins(t *testing.T) {
	dir := t.TempDir()
	// 准备文件
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("apple\nbanana\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)

	got, err := Run("ls", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(got.Output)
	sort.Strings(lines)
	if !reflect.DeepEqual(lines, []string{"a.txt", "sub"}) {
		t.Fatalf("ls => %v", lines)
	}

	got, err = Run("cat a.txt", testOptions(dir))
	if err != nil || got.Output != "apple\nbanana\n" {
		t.Fatalf("cat => %q err=%v", got.Output, err)
	}

	got, err = Run("echo hello | grep hello", testOptions(dir))
	if err != nil || got.Output != "hello\n" {
		t.Fatalf("grep => %q err=%v", got.Output, err)
	}

	got, err = Run("echo hello | wc", testOptions(dir))
	if err != nil || !strings.Contains(got.Output, "1") {
		t.Fatalf("wc => %q err=%v", got.Output, err)
	}

	// head 对齐 GNU：保留输入行的尾随换行，不再截成 "hi"。
	got, err = Run("echo hi | head -n 1", testOptions(dir))
	if err != nil || got.Output != "hi\n" {
		t.Fatalf("head => %q err=%v", got.Output, err)
	}
}

func TestRedirection(t *testing.T) {
	dir := t.TempDir()
	_, err := Run("echo hello > out.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("redirect wrote %q", data)
	}

	_, err = Run("echo again >> out.txt", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "out.txt"))
	if string(data) != "hello\nagain\n" {
		t.Fatalf("append wrote %q", data)
	}
}

func TestGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c"), 0o644)
	got, err := Run("echo *.go", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(got.Output)
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"a.go", "b.go"}) {
		t.Fatalf("glob => %v", fields)
	}
	// 引号内不 glob。
	got, err = Run(`echo "*.go"`, testOptions(dir))
	if err != nil || got.Output != "*.go\n" {
		t.Fatalf("quoted glob => %q", got.Output)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := Run("cat ../secret", testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	// 用变量绕过的路径逃逸也要拒绝。
	got, err := Run(`rm ../outside`, testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitStatus != 1 {
		t.Fatalf("rm escape exit = %d, want 1", got.ExitStatus)
	}
}

func TestFailClosedUnsupported(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{
		"echo hi &",
		"( echo hi )",
		"select x in a b; do echo $x; done",
	} {
		got, err := Run(cmd, testOptions(dir))
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%q: expected ErrUnsupported, got exit=%d err=%v", cmd, got.ExitStatus, err)
		}
		if Supported(cmd) {
			t.Fatalf("%q: Supported() should be false", cmd)
		}
	}
	// 算术展开在词法/语法阶段就被拒绝（错误类型可能是 ParseError，关键是 fail-closed）。
	if _, err := Run("echo $((1+2))", testOptions(dir)); err == nil {
		t.Fatal("arithmetic expansion must be rejected")
	}
	if Supported("echo $((1+2))") {
		t.Fatal("arithmetic expansion: Supported() should be false")
	}
}

func TestSafetyOracle(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(dir)
	opts.Safety = func(cmd string) bool { return !strings.Contains(cmd, "danger") }
	_, err := Run("echo danger", opts)
	if err == nil || !strings.Contains(err.Error(), "safety policy") {
		t.Fatalf("safety oracle should reject, got err=%v", err)
	}
}
