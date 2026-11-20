package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestFindFileEditingCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // "" 表示无匹配；否则为 token 的子串
	}{
		{"rm flagged", "rm -rf x", "rm"},
		{"git status safe", "git status", ""},
		{"quoted rm safe", `echo "rm -rf"`, ""},
		{"single quoted rm safe", `echo 'rm -rf'`, ""},
		{"heredoc body safe", "cat <<EOF\nrm -rf x\nEOF\n", ""},
		{"powershell flagged", "Remove-Item -Force x", "Remove-Item"},
		{"set-content flagged", "Set-Content -Path x -Value y", "Set-Content"},
		{"redirection outside temp flagged", "echo hi > /etc/x", ">/etc/x"},
		{"redirection inside temp safe", "echo hi > /tmp/x", ""},
		{"go test safe", "go test ./...", ""},
		{"go build flagged", "go build ./...", "go build"},
		{"empty safe", "", ""},
		{"second command flagged", "ls; rm x", "rm"},
		{"comment safe", "ls # rm x", ""},
		{"get-content safe", "Get-Content x", ""},
		{"git push flagged", "git push origin main", "git push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("FindFileEditingCommand(%q) = %q, want no match", tc.command, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want a match containing %q", tc.command, got, tc.want)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestIsTempPathCollapsesTraversalBeforePrefixCheck 验证：一个仅写法上指向临时
// 目录、但用 ".." 逃逸出去的目标不会被当作无害的临时写入。旧的裸前缀检查会
// 把它加入白名单。
func TestIsTempPathCollapsesTraversalBeforePrefixCheck(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"tmp root itself", "/tmp", true},
		{"tmp child", "/tmp/build/out.txt", true},
		{"var tmp child", "/var/tmp/x", true},
		{"windows temp child", `C:\Windows\Temp\out.txt`, true},
		{"temp placeholder child", `%TEMP%\out.txt`, true},
		{"dot segments stay inside", "/tmp/a/../b", true},
		{"tmp traversal to etc", "/tmp/../etc/passwd", false},
		{"var tmp traversal", "/var/tmp/../etc/shadow", false},
		{"windows temp traversal", `C:\Windows\Temp\..\..\evil.txt`, false},
		{"temp placeholder traversal", `%TEMP%\..\..\evil.txt`, false},
		{"outside without traversal", "/etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTempPath(tc.path); got != tc.want {
				t.Fatalf("isTempPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestFindFileEditingCommandRejectsTempTraversalRedirect 验证守卫的端到端决定：
// 用 ".." 逃出临时目录的重定向会被当作修改类拦截，而真正的临时写入仍不受影响。
func TestFindFileEditingCommandRejectsTempTraversalRedirect(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // 违规 token 中期望的子串；"" 表示无匹配
	}{
		{"posix temp traversal", "echo hi > /tmp/../etc/passwd", "etc/passwd"},
		{"var tmp traversal", "echo hi > /var/tmp/../etc/shadow", "etc/shadow"},
		{"temp placeholder traversal", "echo hi > %TEMP%/../evil.txt", "evil.txt"},
		{"genuine temp write stays clear", "echo hi > /tmp/build/out.txt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("FindFileEditingCommand(%q) = %q, want no match", tc.command, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestFindFileEditingCommandSeesThroughWrappers 验证包装程序
// （sudo/env/nohup/command/builtin/exec）无法隐藏本可被检测到的修改类命令。
// 威胁分类器已经归约包装器；token 分类器也必须这样做，否则 `sudo rm file.txt`
// 会漏过，而普通的 `rm file.txt` 却被拦截。
func TestFindFileEditingCommandSeesThroughWrappers(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // 违规 token 中期望的子串
	}{
		{"sudo rm", "sudo rm file.txt", "rm"},
		{"env cp", "env cp a b", "cp"},
		{"nohup mv", "nohup mv a b", "mv"},
		{"command rmdir", "command rmdir old", "rmdir"},
		{"builtin set-content", "builtin Set-Content -Path x -Value y", "Set-Content"},
		{"exec chmod", "exec chmod +x script.sh", "chmod"},
		{"stacked wrappers", "sudo env mv a b", "mv"},
		{"second command wrapped", "ls; sudo rm file.txt", "rm"},
		{"env assignment still gated", "env FOO=1 rm file.txt", "rm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindFileEditingCommand(tc.command)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("FindFileEditingCommand(%q) = %q, want it to contain %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestGuardShellCommandOnlyInspectsShellTools(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	args := json.RawMessage(`{"command":"rm -rf x"}`)
	if token := guardShellCommand("bash", args); token != "rm" {
		t.Fatalf("bash token = %q, want rm", token)
	}
	if token := guardShellCommand("write", args); token != "" {
		t.Fatalf("non-shell tool token = %q, want none", token)
	}
}
func TestChainPlanCommandGuardBlocksBeforeApproval(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "enforce")
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	audit := NewAuditLog(dir)
	guarded := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	guarded.PlanCommandGuard = true

	err := guarded.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
	if err == nil {
		t.Fatal("a mutating command must be blocked when the guard is enabled")
	}
	if !errors.Is(err, ErrRecoverablePlanBlock) {
		t.Fatalf("got %v, want ErrRecoverablePlanBlock", err)
	}
	if errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
		t.Fatalf("a recoverable block must not also be a hard denial: %v", err)
	}
	var stop *StopError
	if errors.As(err, &stop) {
		t.Fatalf("a recoverable block must not be a StopError: %#v", err)
	}
	if !strings.Contains(err.Error(), "rm") {
		t.Fatalf("error %q does not name the offending token", err)
	}
	if approver.calls != 0 {
		t.Fatalf("guard must run before approval, approver.calls = %d", approver.calls)
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("guard must run before audit, audit: %v entries=%d", readErr, len(entries))
	}
}

func TestChainWithoutPlanGuardUsesNormalPath(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	chain := NewChain(DefaultPolicy(dir), approver, NewAuditLog(dir), "s1")
	if err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`)); err != nil {
		t.Fatalf("with the guard off a mutating command follows the normal path: %v", err)
	}
	if approver.calls != 1 {
		t.Fatalf("approver.calls = %d, want 1", approver.calls)
	}
}

// TestGuardrailMode 验证环境变量解析：shadow/enforce/off 不区分大小写，
// 未设置、空值和未知值都会回退到默认的 enforce。
func TestGuardrailMode(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset or empty defaults to enforce", "", "enforce"},
		{"enforce", "enforce", "enforce"},
		{"enforce is case-insensitive", "EnFoRcE", "enforce"},
		{"shadow", "shadow", "shadow"},
		{"shadow trims whitespace and case", "  SHADOW ", "shadow"},
		{"off", "off", "off"},
		{"off is case-insensitive", "OFF", "off"},
		{"unknown value falls back to enforce", "banana", "enforce"},
		{"whitespace-only value falls back to enforce", "   ", "enforce"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_GUARDRAILS", tc.env)
			if got := GuardrailMode(); got != tc.want {
				t.Fatalf("GuardrailMode() with ARGENT_GUARDRAILS=%q = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// TestGuardrailModeGuardShellCommand 检查交给拦截决策点的 token：enforce
// （以及默认/未知回退）返回违规 token，shadow 和 off 不返回任何东西。
func TestGuardrailModeGuardShellCommand(t *testing.T) {
	mutating := json.RawMessage(`{"command":"rm -rf x"}`)
	cases := []struct {
		name      string
		env       string
		wantToken string
	}{
		{"default blocks", "", "rm"},
		{"enforce blocks", "enforce", "rm"},
		{"enforce case-insensitive blocks", "ENFORCE", "rm"},
		{"shadow does not block", "shadow", ""},
		{"off does not block", "off", ""},
		{"unknown falls back to enforce", "banana", "rm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_GUARDRAILS", tc.env)
			if got := guardShellCommand("bash", mutating); got != tc.wantToken {
				t.Fatalf("guardShellCommand token = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

// TestGuardrailShadowLogsWithoutBlocking 断言 shadow 模式会记录本会被拦截的内容，
// 但仍不向调用方返回任何 token。
func TestGuardrailShadowLogsWithoutBlocking(t *testing.T) {
	t.Setenv("ARGENT_GUARDRAILS", "shadow")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if token := guardShellCommand("bash", json.RawMessage(`{"command":"rm -rf x"}`)); token != "" {
		t.Fatalf("shadow mode must not return a blocking token, got %q", token)
	}
	if !strings.Contains(buf.String(), "rm") {
		t.Fatalf("shadow mode must log the detected command, got %q", buf.String())
	}
}

// TestGuardrailModeChainBlocking 检验真正的拦截决策点：enforce 模式
// （以及默认/未知回退）在审批/审计之前拦截，而 shadow 和 off 让调用走正常的
// 策略 -> 审批 -> 审计链。
func TestGuardrailModeChainBlocking(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		wantBlock bool
	}{
		{"default blocks", "", true},
		{"enforce blocks", "enforce", true},
		{"shadow passes through", "shadow", false},
		{"off passes through", "off", false},
		{"unknown falls back to enforce", "banana", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_GUARDRAILS", tc.env)
			dir := t.TempDir()
			approver := &countingApprover{allow: true}
			audit := NewAuditLog(dir)
			chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
			chain.PlanCommandGuard = true

			err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf x"}`))
			if tc.wantBlock {
				if !errors.Is(err, ErrRecoverablePlanBlock) {
					t.Fatalf("got %v, want ErrRecoverablePlanBlock", err)
				}
				if approver.calls != 0 {
					t.Fatalf("blocked guard must run before approval, approver.calls = %d", approver.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("non-enforce mode must follow the normal path: %v", err)
			}
			if approver.calls != 1 {
				t.Fatalf("approver.calls = %d, want 1", approver.calls)
			}
			entries, readErr := ReadAudit(audit.Path())
			if readErr != nil || len(entries) != 1 || entries[0].Decision != "allow" {
				t.Fatalf("non-enforce mode must record one allow decision, entries=%v err=%v", entries, readErr)
			}
		})
	}
}
