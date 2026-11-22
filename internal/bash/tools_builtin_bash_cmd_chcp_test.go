package bash

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeLookPath 是 shellInvocationFor 的测试接缝：用可控结果替代
// exec.LookPath，从而在任意主机上覆盖 Windows 下的 bash/cmd 分支。
func fakeLookPath(found bool) func(string) (string, error) {
	if found {
		return func(name string) (string, error) { return `C:\fake\` + name + `.exe`, nil }
	}
	return func(string) (string, error) { return "", errors.New("fake: not found") }
}

// TestShellInvocationWindowsCmdSwitchesCodePage 验证 cmd 回退路径把
// 代码页切到 65001，用单个 &（而非 &&）串联，并且原命令逐字节保留在 /c 参数里。
func TestShellInvocationWindowsCmdSwitchesCodePage(t *testing.T) {
	// 前缀本身必须是单个 &：防止常量被改回 && 而静默跳过原命令。
	if cmdChcpPrefix != "chcp 65001>nul&" {
		t.Fatalf("cmdChcpPrefix = %q, want %q", cmdChcpPrefix, "chcp 65001>nul&")
	}
	command := `echo hello`
	path, argv := shellInvocationFor("windows", fakeLookPath(false), command)
	if path != "cmd" {
		t.Fatalf("path = %q, want cmd", path)
	}
	if len(argv) != 3 {
		t.Fatalf("argv = %q, want three entries", argv)
	}
	if argv[0] != "/s" || argv[1] != "/c" {
		t.Fatalf("flags = %q, want /s /c", argv[:2])
	}
	if !strings.HasPrefix(argv[2], "chcp 65001>nul&") {
		t.Fatalf("/c argument = %q, want single-& chcp 65001>nul& prefix", argv[2])
	}
	if strings.Contains(argv[2], "nul&&") {
		t.Fatalf("/c argument = %q, want a single & after nul (not &&)", argv[2])
	}
	if !strings.Contains(argv[2], "65001>nul") {
		t.Fatalf("/c argument = %q, want chcp echo redirected to nul", argv[2])
	}
	if got := strings.TrimPrefix(argv[2], cmdChcpPrefix); got != command {
		t.Fatalf("command after prefix = %q, want %q", got, command)
	}
}

// TestShellInvocationCmdPreservesCommandBytes 验证 /c 参数只在最前面
// 追加 chcp 前缀，不包裹括号或额外引号：以引号开头、含引号、含 ')'、
// 含 cmd 操作符、含 CJK 非 ASCII 的命令，从 argv 里剥掉前缀后都逐字节
// 还原为原命令，因此 /c 的引号剥离语义与字节内容都不因前缀改变。
func TestShellInvocationCmdPreservesCommandBytes(t *testing.T) {
	for _, command := range []string{
		`"C:\Program Files\app.exe" --flag`, // 以引号开头
		`echo "quoted text"`,                // 内嵌引号
		`echo foo)`,                         // 右括号：若用 (...) 包裹会被破坏
		`echo a & echo b`,                   // 命令操作符
		`echo 中文`,                           // CJK 非 ASCII
	} {
		_, argv := shellInvocationFor("windows", fakeLookPath(false), command)
		if len(argv) != 3 {
			t.Fatalf("command %q: argv = %q", command, argv)
		}
		got := strings.TrimPrefix(argv[2], cmdChcpPrefix)
		if !bytes.Equal([]byte(got), []byte(command)) {
			t.Errorf("command %q: round-trip bytes = %v, want %v",
				command, []byte(got), []byte(command))
		}
		if strings.Count(argv[2], "chcp") != 1 {
			t.Errorf("command %q: chcp appears more than once in %q", command, argv[2])
		}
	}
}

// TestShellInvocationBashPathUnchanged 验证找到 bash 时完全沿用
// bash -lc <command>，不带任何 chcp 改动。
func TestShellInvocationBashPathUnchanged(t *testing.T) {
	command := `echo 中文`
	path, argv := shellInvocationFor("windows", fakeLookPath(true), command)
	if path != "bash" {
		t.Fatalf("path = %q, want bash", path)
	}
	if len(argv) != 2 || argv[0] != "-lc" || argv[1] != command {
		t.Fatalf("argv = %q, want [-lc %q]", argv, command)
	}
	for _, arg := range argv {
		if strings.Contains(arg, "chcp") {
			t.Fatalf("bash path must not contain chcp: %q", argv)
		}
	}
}

// TestShellInvocationNonWindowsUnchanged 验证非 Windows 平台仍走 sh -lc，
// 生产入口也保持这一行为。
func TestShellInvocationNonWindowsUnchanged(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		path, argv := shellInvocationFor(goos, fakeLookPath(false), `echo hi`)
		if path != "sh" {
			t.Errorf("%s: path = %q, want sh", goos, path)
		}
		if len(argv) != 2 || argv[0] != "-lc" || argv[1] != `echo hi` {
			t.Errorf("%s: argv = %q, want [-lc echo hi]", goos, argv)
		}
	}
	if runtime.GOOS != "windows" {
		path, argv := shellInvocation(`echo hi`)
		if path != "sh" || len(argv) != 2 || argv[0] != "-lc" || argv[1] != `echo hi` {
			t.Fatalf("production non-Windows invocation = %q %q", path, argv)
		}
	}
}

// TestCmdChcpPrefixKeepsASCIIByteIdentical 在真实 cmd 上验证：加了 chcp
// 前缀后，纯 ASCII 命令的输出与原来 cmd /c 的输出逐字节相同。
func TestCmdChcpPrefixKeepsASCIIByteIdentical(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires real cmd.exe")
	}
	command := `echo hello world`
	_, argv := shellInvocationFor("windows", fakeLookPath(false), command)
	baseline, baselineErr := exec.Command("cmd", "/c", command).CombinedOutput()
	got, gotErr := exec.Command("cmd", argv...).CombinedOutput()
	if baselineErr != nil || gotErr != nil {
		t.Fatalf("baseline err = %v, wrapped err = %v", baselineErr, gotErr)
	}
	if !bytes.Equal(got, baseline) {
		t.Fatalf("wrapped output = %q, want baseline %q", got, baseline)
	}
}

// TestCmdChcpPrefixSwitchesCodePageAndKeepsCJKUTF8 在真实 cmd 上验证
// 代码页确实切到 65001，且中文输出是合法 UTF-8。
func TestCmdChcpPrefixSwitchesCodePageAndKeepsCJKUTF8(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires real cmd.exe")
	}
	_, cpArgv := shellInvocationFor("windows", fakeLookPath(false), `chcp`)
	out, err := exec.Command("cmd", cpArgv...).CombinedOutput()
	if err != nil {
		t.Fatalf("chcp: %v", err)
	}
	if !strings.Contains(string(out), "65001") {
		t.Fatalf("code page output = %q, want 65001", out)
	}

	_, cjkArgv := shellInvocationFor("windows", fakeLookPath(false), `echo 中文`)
	cjk, err := exec.Command("cmd", cjkArgv...).CombinedOutput()
	if err != nil {
		t.Fatalf("echo CJK: %v", err)
	}
	if !utf8.Valid(cjk) {
		t.Fatalf("CJK output is not valid UTF-8: %q", cjk)
	}
	if !strings.Contains(string(cjk), "中文") {
		t.Fatalf("CJK output = %q, want 中文", cjk)
	}
}
