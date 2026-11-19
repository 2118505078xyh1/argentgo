package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/config"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/security"
	"github.com/2118505078xyh1/argent-go/internal/session"
)

// toolCallChatter asks for one tool call on the first turn, then finishes.
type toolCallChatter struct {
	mu    sync.Mutex
	calls int
	first llm.Message
}

func (c *toolCallChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return c.first, nil
	}
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

func TestPolicyConfigDeniesConfiguredTool(t *testing.T) {
	root := t.TempDir()
	policy := policyFor(Resolved{
		Root:       root,
		Cfg:        config.Config{Raw: map[string]any{"permissions": map[string]any{"denyTools": []any{"bash"}}}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxWorkspaceWrite,
	})
	if got := policy.Check("bash", json.RawMessage(`{"command":"echo hi"}`)); got != security.Deny {
		t.Fatalf("bash decision = %v, want deny from permissions.denyTools", got)
	}
	if got := policy.Check("read", json.RawMessage(`{"path":"ok.txt"}`)); got != security.Allow {
		t.Fatalf("read decision = %v, want allow", got)
	}
}

func TestPolicyConfigDecodesSnakeCaseTopLevelKeys(t *testing.T) {
	root := t.TempDir()
	policy := policyFor(Resolved{
		Root: root,
		Cfg: config.Config{Raw: map[string]any{
			"deny_commands": []string{"git push --force"},
			"rules": []any{map[string]any{
				"decision": "deny", "tool": "grep", "command": "secret",
			}},
		}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxWorkspaceWrite,
	})
	if got := policy.Check("bash", json.RawMessage(`{"command":"git push --force origin"}`)); got != security.Deny {
		t.Fatalf("deny_commands decision = %v, want deny", got)
	}
	if got := policy.Check("grep", json.RawMessage(`{"command":"find secret"}`)); got != security.Deny {
		t.Fatalf("rules decision = %v, want deny", got)
	}
}

func TestPolicyConfigReadOnlyDeniesMutatingTools(t *testing.T) {
	root := t.TempDir()
	policy := policyFor(Resolved{
		Root:       root,
		Cfg:        config.Config{Raw: map[string]any{"mutatingTools": []any{"deploy"}}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxReadOnly,
	})
	for _, name := range append(append([]string(nil), builtinMutatingToolNames...), "deploy") {
		if got := policy.Check(name, json.RawMessage(`{}`)); got != security.Deny {
			t.Errorf("read-only %s decision = %v, want deny", name, got)
		}
	}
}

func TestPolicyConfigDangerFullAccessRemovesWriteClipping(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	args := json.RawMessage(fmt.Sprintf(`{"path":%q}`, filepath.Join(outside, "note.txt")))

	restricted := policyFor(Resolved{
		Root:       root,
		Cfg:        config.Config{Raw: map[string]any{}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxWorkspaceWrite,
	})
	if got := restricted.Check("write", args); got != security.Deny {
		t.Fatalf("workspace-write outside root = %v, want deny", got)
	}

	// An explicit user rule survives the wider write boundary.
	full := policyFor(Resolved{
		Root: root,
		Cfg: config.Config{Raw: map[string]any{
			"permissions": map[string]any{"denyTools": []any{"edit"}},
		}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxDangerFullAccess,
	})
	if got := full.Check("write", args); got == security.Deny {
		t.Fatalf("danger-full-access outside root = %v, want approval not deny", got)
	}
	if got := full.Check("edit", args); got != security.Deny {
		t.Fatalf("explicit denyTools lost under danger-full-access: %v", got)
	}
}

func TestUnconfiguredWorkspaceStillAllowsReads(t *testing.T) {
	root := t.TempDir()
	policy := policyFor(Resolved{
		Root:       root,
		Cfg:        config.Config{Raw: map[string]any{}},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxWorkspaceWrite,
	})
	if !policy.IsEmpty() {
		t.Fatal("empty config must not add rules")
	}
	args := json.RawMessage(fmt.Sprintf(`{"path":%q}`, filepath.Join(root, "file.txt")))
	if got := policy.Check("read", args); got != security.Allow {
		t.Fatalf("read decision = %v, want allow", got)
	}
}

func TestExecuteHonoursConfiguredDenyTool(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"), `{"permissions":{"denyTools":["write"]}}`)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "c1", Type: "function",
		Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"blocked.txt","content":"x"}`},
	}}}}
	store := testStore(t, res)
	sess, err := store.NewSession("policy-deny")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write created a file (stat err=%v)", err)
	}
	if !toolOutputContains(t, sess, "denied by policy") {
		t.Fatal("the model did not see a tool-visible policy deny")
	}
}

func toolOutputContains(t *testing.T, sess *session.Session, needle string) bool {
	t.Helper()
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Kind != session.KindTool {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(record.Data, &fields) != nil {
			continue
		}
		if strings.Contains(fmt.Sprint(fields["output"]), needle) {
			return true
		}
	}
	return false
}
