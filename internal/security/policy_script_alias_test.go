package security

import "testing"

// TestScriptAliasCannotBypassDenyCommands verifies that the `script` alias
// (accepted by the powershell tool) is subject to the same denyCommands red
// line as `command`. Before normalization, a hard deny silently degraded to
// Ask under the alias.
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

// TestScriptAliasCannotBypassRuleDecision verifies that permission-table
// command rules match the normalized command field, not just the literal one.
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

// TestScriptAliasCannotBypassDenyCommandPaths verifies denyCommandPaths sees
// the normalized command value too.
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

// TestExplicitCommandWinsOverScript ensures normalization never overwrites an
// explicit, non-empty command. The policy must judge exactly what the tool
// executes (the tool itself prefers a non-empty command over script).
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

// TestScriptAliasLeavesOtherToolsUnaffected confirms normalization does not
// change policy decisions for tools that do not use the script alias.
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

// TestScriptAliasNormalizationUnit tests the normalization helper directly,
// including normal text with no command fields at all.
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
