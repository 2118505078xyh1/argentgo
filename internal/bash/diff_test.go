package bash

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBashCompatibility 用系统 bash 作为 oracle，对同一批命令比对自研 shell 的
// 输出与退出码。这是"兼容 shell 语法"的客观验收标准：任何原生接管却产生
// 与 bash 不同结果的命令都会在这里暴露。
//
// 无 bash 的环境（CI 容器等）自动跳过，不影响常规测试。
func TestBashCompatibility(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("系统无 bash，跳过差分测试")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\nfoo bar\nhello again\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, cmd string
	}{
		{"echo", `echo hello`},
		{"printf", `printf 'x=%s\n' 1`},
		{"cat", `cat a.txt`},
		{"grep", `grep -n hello a.txt`},
		{"wc", `wc -l a.txt`},
		{"head", `head -n 2 a.txt`},
		{"tail", `tail -n 2 a.txt`},
		{"ls", `ls`},
		{"test-and", `test -f a.txt && echo yes`},
		{"pipeline", `cat a.txt | grep hello | wc -l`},
		{"assignment", `X=42; echo $X`},
		{"if", `if true; then echo yes; else echo no; fi`},
		{"if-elif", `if false; then echo A; elif true; then echo B; else echo C; fi`},
		{"for", `for i in a b c; do echo $i; done`},
		{"while", `i=0; while test $i = 2; do echo $i; i=2; done`},
		{"case", `case b in a) echo A;; b) echo B;; esac`},
		{"function", `f() { echo hi; }; f`},
		{"func-args", `f() { echo $1 $2; }; f aa bb`},
	}

	var diffs int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotErr := Run(tc.cmd, controlOptions(dir))

			c := exec.Command(bash, "-lc", tc.cmd)
			c.Dir = dir
			raw, bashErr := c.CombinedOutput()
			want := normDiffOutput(string(raw), dir)
			wantCode := 0
			if bashErr != nil {
				if ee, ok := bashErr.(*exec.ExitError); ok {
					wantCode = ee.ExitCode()
				} else {
					t.Fatalf("启动 bash 失败: %v", bashErr)
				}
			}
			if gotErr != nil {
				t.Fatalf("自研执行 %q 出错: %v", tc.cmd, gotErr)
			}
			if normDiffOutput(got.Output, dir) != want {
				t.Errorf("输出不一致:\n  自研 = %q\n  bash = %q", normDiffOutput(got.Output, dir), want)
				diffs++
			}
			if got.ExitStatus != wantCode {
				t.Errorf("退出码不一致: 自研=%d bash=%d", got.ExitStatus, wantCode)
				diffs++
			}
		})
	}
	if diffs > 0 {
		t.Logf("共 %d 处差异（见上面的子测试）", diffs)
	}
}

func normDiffOutput(s, dir string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, dir, "<DIR>")
	slash := strings.ReplaceAll(dir, "\\", "/")
	if len(slash) >= 2 && slash[1] == ':' {
		s = strings.ReplaceAll(s, "/"+strings.ToLower(string(slash[0]))+slash[2:], "<DIR>")
	}
	return strings.TrimSpace(s)
}
