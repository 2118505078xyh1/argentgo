package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newRedirectWorkspace 构造 base/ws 两级目录。以 ws 为工作区时，
// "../x" 精确指向 base/x，便于断言逃逸目标未被创建。
func newRedirectWorkspace(t *testing.T) (ws, base string) {
	t.Helper()
	base = t.TempDir()
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	return ws, base
}

// assertRedirectRejected 断言错误来自 resolvePath 的工作区边界校验，
// 且带有稳定的重定向错误前缀。
func assertRedirectRejected(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected redirect rejection, got nil error")
	}
	if !strings.Contains(err.Error(), prefix) {
		t.Fatalf("error %q missing prefix %q", err.Error(), prefix)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("error %q not from resolvePath boundary", err.Error())
	}
}

// 输出重定向的两种逃逸形态：字面 ../ 与命令替换产出 ../。
// 两者都必须在写入前被拒绝，工作区外的目标文件不得被创建。
func TestRedirectOutputEscapeRejected(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		target string // 相对 base 的逃逸目标
	}{
		{"dotdot", "echo x > ../outside", "outside"},
		{"command-subst", `echo x > $(echo ../x)`, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, base := newRedirectWorkspace(t)
			_, err := Run(tc.cmd, testOptions(ws))
			assertRedirectRejected(t, err, "shell: redirect to")
			target := filepath.Join(base, tc.target)
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("escape target %q created (stat err=%v)", target, statErr)
			}
		})
	}
}

// 输入重定向逃逸：工作区外的秘密文件不得被读取。
func TestRedirectInputEscapeRejected(t *testing.T) {
	ws, base := newRedirectWorkspace(t)
	secret := filepath.Join(base, "secret")
	const payload = "TOP-SECRET-DO-NOT-READ"
	if err := os.WriteFile(secret, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run("cat < ../secret", testOptions(ws))
	assertRedirectRejected(t, err, "shell: redirect from")
	if strings.Contains(res.Output, payload) {
		t.Fatalf("secret leaked into output: %q", res.Output)
	}
}

// 工作区内的正常重定向（覆盖、追加、输入）行为保持不变。
func TestRedirectInsideWorkspaceStillWorks(t *testing.T) {
	ws, _ := newRedirectWorkspace(t)
	if _, err := Run("echo hello > ok.txt", testOptions(ws)); err != nil {
		t.Fatal(err)
	}
	if _, err := Run("echo again >> ok.txt", testOptions(ws)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "ok.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\nagain\n" {
		t.Fatalf("redirect wrote %q", data)
	}

	if err := os.WriteFile(filepath.Join(ws, "in.txt"), []byte("from file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run("cat < in.txt", testOptions(ws))
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "from file\n" {
		t.Fatalf("input redirect => %q", res.Output)
	}
}

// CJK 路径的重定向须正常解析（工作区内）且不误判为逃逸。
func TestRedirectCJKPath(t *testing.T) {
	ws, _ := newRedirectWorkspace(t)
	if err := os.MkdirAll(filepath.Join(ws, "中文目录"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := "中文目录/文件.txt"
	if _, err := Run(`echo 你好 > "`+rel+`"`, testOptions(ws)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "中文目录", "文件.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "你好\n" {
		t.Fatalf("CJK redirect wrote %q", data)
	}
	res, err := Run(`cat < "`+rel+`"`, testOptions(ws))
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "你好\n" {
		t.Fatalf("CJK input redirect => %q", res.Output)
	}
}

// 经工作区内符号链接/junction 逃到工作区外的写入须被拒绝。
// 平台不支持创建符号链接（如未开启开发者模式的 Windows）时跳过。
func TestRedirectSymlinkEscapeRejected(t *testing.T) {
	ws, base := newRedirectWorkspace(t)
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	target := filepath.Join(outside, "escape.txt")
	_, err := Run(`echo x > link/escape.txt`, testOptions(ws))
	assertRedirectRejected(t, err, "shell: redirect to")
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("symlink escape target %q created (stat err=%v)", target, statErr)
	}
}
