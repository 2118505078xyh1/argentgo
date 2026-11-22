package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinGroups 验证内建分成 shell 内建与 coreutils 两组，复杂工具刻意不内建。
func TestBuiltinGroups(t *testing.T) {
	for _, name := range []string{"cd", "pwd", "echo", "export", "unset", "true", "false"} {
		if !shellBuiltinNames[name] || coreutilNames[name] {
			t.Fatalf("%q 应只属于 shell 内建组", name)
		}
	}
	for _, name := range []string{"ls", "cat", "grep", "wc", "head", "tail", "mkdir", "rm",
		"printf", "test", "[", "touch", "cp", "mv", "tee"} {
		if !coreutilNames[name] || shellBuiltinNames[name] {
			t.Fatalf("%q 应只属于 coreutils 组", name)
		}
	}
	for _, name := range []string{"sed", "awk", "curl", "find", "xargs", "sort", "git"} {
		if isBuiltin(name) {
			t.Fatalf("%q 不该内建，应留给外部进程", name)
		}
	}
	if !isBuiltin("grep") || !isBuiltin("echo") {
		t.Fatal("isBuiltin 应合并两组")
	}
}

func TestShellBuiltinsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "子目录"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd  string
		want string
	}{
		{`cd 子目录 && pwd`, filepath.Join(dir, "子目录") + "\n"},
		{`export FOO=bar; echo $FOO; unset FOO; echo ${FOO:-none}`, "bar\nnone\n"},
		{`true && echo yes`, "yes\n"},
		{`false || echo yes`, "yes\n"},
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

// TestNonzeroExitViaLastStatus 确认命令失败是结果（LastStatus/ExitStatus），不是 Go error。
func TestNonzeroExitViaLastStatus(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		cmd  string
		code int
	}{
		{`false`, 1},
		{`grep zzz_not_here`, 1},
		{`test -f 不存在.txt`, 1},
		{`printf '%f' x`, 1},
		{`grep '('`, 2},
		{`grep --bogus pat`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got, err := Run(tc.cmd, testOptions(dir))
			if err != nil {
				t.Fatal(err)
			}
			if got.ExitStatus != tc.code {
				t.Fatalf("%q exit=%d, want %d (output=%q)", tc.cmd, got.ExitStatus, tc.code, got.Output)
			}
		})
	}
}

func TestShellBuiltinErrorsCarryStderr(t *testing.T) {
	dir := t.TempDir()
	it := &Interp{Vars: map[string]string{}, Cwd: dir}
	_, stderr, err := it.runBuiltin("cd", []string{"../outside"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if it.LastStatus != 1 || !strings.Contains(stderr, "outside the workspace") {
		t.Fatalf("cd escape: status=%d stderr=%q", it.LastStatus, stderr)
	}
}
