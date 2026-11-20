package security

import "testing"

// TestScriptAliasCannotBypassDenyCommands 验证 `script` 别名（powershell 工具接受）
// 与 `command` 一样受 denyCommands 红线约束。规范化之前，硬 deny 在该别名下会
// 悄悄降级为 Ask。
func TestScriptAliasCannotBypassDenyCommands(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommands: []string{"Remove-Item"}})
	cases := []struct {
		name string
		args string
		want Decision
	}{
		{"command field", `{"command":"Remove-Item C:\\temp\\file.txt -Force"}`, Deny},
		{"script alias", `{"script":"Remove-Item C:\\temp\\file.txt -Force"}`, Deny},
		{"empty command falls back to script", `{"command":"","script":"Remove-Item C:\\temp\\file.txt -Force"}`, Deny},
		{"whitespace command falls back to script", `{"command":"   ","script":"Remove-Item C:\\temp\\file.txt -Force"}`, Deny},
		{"benign script", `{"script":"Get-ChildItem C:\\temp"}`, Ask},
	}
	for _, tc := range cases {
		if got := policy.Check("powershell", mustArgs(t, tc.args)); got != tc.want {
			t.Fatalf("%s: %s -> got %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// TestScriptAliasCannotBypassRuleDecision 验证权限表的命令规则匹配规范化后的
// command 字段，而不只是字面量那个字段。
func TestScriptAliasCannotBypassRuleDecision(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Deny, Tool: "powershell", Command: "Remove-Item*"},
		{Decision: Deny, Command: "Stop-Process*"},
	}})
	cases := []struct {
		name string
		tool string
		args string
		want Decision
	}{
		{"deny rule via command", "powershell", `{"command":"Remove-Item C:\\temp"}`, Deny},
		{"deny rule via script", "powershell", `{"script":"Remove-Item C:\\temp"}`, Deny},
		{"tool-agnostic deny rule via script", "powershell", `{"script":"Stop-Process -Id 42"}`, Deny},
		{"unmatched rule via script", "powershell", `{"script":"Get-Date"}`, Ask},
	}
	for _, tc := range cases {
		if got := policy.Check(tc.tool, mustArgs(t, tc.args)); got != tc.want {
			t.Fatalf("%s: %s -> got %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// TestScriptAliasCannotBypassDenyCommandPaths 验证 denyCommandPaths 也能看到
// 规范化后的 command 值。
func TestScriptAliasCannotBypassDenyCommandPaths(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommandPaths: []string{"~/.ssh"}})
	cases := []struct {
		name string
		args string
		want Decision
	}{
		{"denied path via command", `{"command":"Get-Content ~/.ssh/id_rsa"}`, Deny},
		{"denied path via script", `{"script":"Get-Content ~/.ssh/id_rsa"}`, Deny},
		{"benign path via script", `{"script":"Get-Content ./README.md"}`, Ask},
	}
	for _, tc := range cases {
		if got := policy.Check("powershell", mustArgs(t, tc.args)); got != tc.want {
			t.Fatalf("%s: %s -> got %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// TestExplicitCommandWinsOverScript 确保规范化绝不覆盖显式非空的 command。
// 策略必须判断工具实际执行的内容（工具本身优先使用非空 command 而非 script）。
func TestExplicitCommandWinsOverScript(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommands: []string{"Remove-Item"}})
	if got := policy.Check("powershell", mustArgs(t,
		`{"command":"Get-Date","script":"Remove-Item C:\\temp"}`)); got != Ask {
		t.Fatalf("explicit benign command with malicious script: got %v, want ask", got)
	}
	if got := policy.Check("powershell", mustArgs(t,
		`{"command":"Remove-Item C:\\temp","script":"Get-Date"}`)); got != Deny {
		t.Fatalf("explicit malicious command with benign script: got %v, want deny", got)
	}
}

// TestScriptAliasLeavesOtherToolsUnaffected 确认规范化不会改变不使用 script 别名
// 的工具的策略决定。
func TestScriptAliasLeavesOtherToolsUnaffected(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommands: []string{"rm -rf /"}})
	cases := []struct {
		name string
		tool string
		args string
		want Decision
	}{
		{"bash denied via command", "bash", `{"command":"rm -rf /"}`, Deny},
		{"bash benign via command", "bash", `{"command":"git status"}`, Ask},
		{"read tool unaffected", "read", `{"path":"a.txt"}`, Allow},
	}
	for _, tc := range cases {
		if got := policy.Check(tc.tool, mustArgs(t, tc.args)); got != tc.want {
			t.Fatalf("%s: %s %s -> got %v, want %v", tc.name, tc.tool, tc.args, got, tc.want)
		}
	}
}

// TestScriptAliasNormalizationUnit 直接测试规范化辅助函数，包括完全没有命令字段的
// 普通文本。
func TestScriptAliasNormalizationUnit(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantCmd string
	}{
		{"script only", `{"script":"Get-Date"}`, "Get-Date"},
		{"empty command with script", `{"command":"","script":"Get-Date"}`, "Get-Date"},
		{"explicit command wins", `{"command":"Get-ChildItem","script":"Get-Date"}`, "Get-ChildItem"},
		{"no command fields", `{"timeout_seconds":5}`, ""},
		{"command only", `{"command":"Get-Date"}`, "Get-Date"},
	}
	for _, tc := range cases {
		fields, err := parseArgs(mustArgs(t, tc.args))
		if err != nil {
			t.Fatalf("%s: parseArgs: %v", tc.name, err)
		}
		got, _ := fields["command"].(string)
		if got != tc.wantCmd {
			t.Fatalf("%s: command = %q, want %q", tc.name, got, tc.wantCmd)
		}
	}
}
