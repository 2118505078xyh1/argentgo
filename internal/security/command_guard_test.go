package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFindFileEditingCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string // "" means no match; otherwise a substring of the token
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

func TestGuardShellCommandOnlyInspectsShellTools(t *testing.T) {
	args := json.RawMessage(`{"command":"rm -rf x"}`)
	if token := guardShellCommand("bash", args); token != "rm" {
		t.Fatalf("bash token = %q, want rm", token)
	}
	if token := guardShellCommand("write", args); token != "" {
		t.Fatalf("non-shell tool token = %q, want none", token)
	}
}
func TestChainPlanCommandGuardBlocksBeforeApproval(t *testing.T) {
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
