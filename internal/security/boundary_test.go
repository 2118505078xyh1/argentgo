package security

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRestoreGoesThroughPolicyApprovalAndAudit(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), approver, audit, "session-1")

	if err := chain.CheckRestore(context.Background(), []string{"src/a.txt"}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1 for a mutating restore", approver.calls)
	}
	entries, err := ReadAudit(audit.Path())
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", err, len(entries))
	}
	if entries[0].Tool != RestoreToolName || entries[0].Decision != "allow" || entries[0].Session != "session-1" {
		t.Fatalf("audit entry = %+v", entries[0])
	}
	if !strings.Contains(entries[0].Args, "src/a.txt") {
		t.Fatalf("audit args = %q", entries[0].Args)
	}
}

func TestCheckRestoreDeniesWithoutAskingAndRecordsRefusal(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	audit := NewAuditLog(dir)
	chain := NewChain(NewPolicy(dir, PolicyConfig{DenyPaths: []string{"**/secret/**"}}), approver, audit, "s1")
	err := chain.CheckRestore(context.Background(), []string{"secret/key.txt"})
	if !errors.Is(err, ErrDenied) || approver.calls != 0 {
		t.Fatalf("denied restore err=%v approver=%d", err, approver.calls)
	}

	refusing := NewChain(DefaultPolicy(dir), &countingApprover{allow: false}, audit, "s1")
	err = refusing.CheckRestore(context.Background(), []string{"src/a.txt"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("refused restore: %v", err)
	}
	entries, _ := ReadAudit(audit.Path())
	if len(entries) != 2 || entries[0].Decision != "deny" || entries[1].Decision != "refused" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestCheckRestoreRedactsManifestPathsInAudit(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), &countingApprover{allow: true}, audit, "s1")
	paths := []string{"src/token-sk-abcdef123456/.env", "src/secret-password-abcdef456/file"}
	if err := chain.CheckRestore(context.Background(), paths); err != nil {
		t.Fatalf("restore: %v", err)
	}
	entries, err := ReadAudit(audit.Path())
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", err, len(entries))
	}
	for _, secret := range []string{"sk-abcdef123456", "secret-password-abcdef456"} {
		if strings.Contains(entries[0].Args, secret) {
			t.Fatalf("audit leaked %q: %s", secret, entries[0].Args)
		}
	}
	if !strings.Contains(entries[0].Args, "[redacted:") {
		t.Fatalf("audit has no redaction marker: %s", entries[0].Args)
	}
}

func TestRedactManifestPathsLeavesOrdinaryPathsIntact(t *testing.T) {
	out := string(RedactManifestPaths(json.RawMessage(`{"paths":["src/a.go","docs/readme.md"]}`)))
	if !strings.Contains(out, "src/a.go") || !strings.Contains(out, "docs/readme.md") {
		t.Fatalf("redacted ordinary paths: %s", out)
	}
}

func TestPolicyFromJSONAcceptsSnakeCaseAndCamelCaseKeys(t *testing.T) {
	dir := t.TempDir()
	snake, err := PolicyFromJSON(dir, []byte(`{
		"deny_tools":["bash"],
		"deny_paths":["**/.env"],
		"deny_commands":"git push --force",
		"allow_tools":["read","bash","custom_read"],
		"read_only_tools":["custom_read"],
		"rules":[{"decision":"ask","tool":"write"}]
	}`))
	if err != nil {
		t.Fatalf("snake_case parse: %v", err)
	}
	if snake.Check("bash", mustArgs(t, `{"command":"ls"}`)) != Deny {
		t.Fatal("snake_case deny_tools was ignored")
	}
	if snake.Check("read", mustArgs(t, `{"path":"x/.env"}`)) != Deny {
		t.Fatal("snake_case deny_paths was ignored")
	}
	if snake.Check("bash", mustArgs(t, `{"command":"git push --force"}`)) != Deny {
		t.Fatal("snake_case deny_commands was ignored")
	}
	if snake.Check("custom_read", mustArgs(t, `{}`)) != Allow {
		t.Fatal("snake_case read_only_tools was ignored")
	}

	camel, err := PolicyFromJSON(dir, []byte(`{
		"denyPaths":["**/secret/**"],
		"allowTools":["read","bash","write"],
		"rules":[{"decision":"deny","tool":"bash"}]
	}`))
	if err != nil {
		t.Fatalf("camelCase parse: %v", err)
	}
	if camel.Check("read", mustArgs(t, `{"path":"secret/a"}`)) != Deny {
		t.Fatal("camelCase denyPaths was ignored")
	}
	if camel.Check("bash", mustArgs(t, `{"command":"ls"}`)) != Deny {
		t.Fatal("camelCase rule was ignored")
	}

	// 如果两种写法都出现，以文档记载的 camelCase 键为准。
	both, err := PolicyFromJSON(dir, []byte(`{"denyTools":["bash"],"deny_tools":["write"]}`))
	if err != nil {
		t.Fatalf("mixed parse: %v", err)
	}
	if both.Check("bash", mustArgs(t, `{}`)) != Deny || both.Check("write", mustArgs(t, `{}`)) == Deny {
		t.Fatal("camelCase key must take precedence over its snake_case alias")
	}
}

func TestVerifyFileDetectsTamperingAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hook.json")
	content := []byte(`{"hooks":{"PreToolUse":[]}}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := DigestFile(path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if err := VerifyFile(path, digest); err != nil {
		t.Fatalf("unchanged file failed verification: %v", err)
	}
	if err := VerifyFile(path, strings.ToUpper(digest)); err != nil {
		t.Fatalf("uppercase digest failed verification: %v", err)
	}
	if err := os.WriteFile(path, append(content, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered file: got %v, want ErrIntegrity", err)
	}
	if err := VerifyFile(filepath.Join(dir, "missing.json"), digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing file: got %v, want ErrIntegrity", err)
	}
	if err := VerifyFile(path, "deadbeef"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bad digest: got %v, want ErrIntegrity", err)
	}
}
