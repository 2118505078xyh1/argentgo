package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/agent"
	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/provider"
	"github.com/2118505078xyh1/argent-go/internal/security"
)

func envFrom(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestDefaultStoragePrecedence(t *testing.T) {
	home := t.TempDir()
	base := map[string]string{"USERPROFILE": home}

	if got, want := DefaultStorage(envFrom(base)), filepath.Join(home, ".argent-go"); got != want {
		t.Fatalf("DefaultStorage = %q, want %q", got, want)
	}

	withDir := map[string]string{"USERPROFILE": home, "ARGENT_STORAGE_DIR": filepath.Join(home, "s1")}
	if got, want := DefaultStorage(envFrom(withDir)), filepath.Join(home, "s1"); got != want {
		t.Fatalf("ARGENT_STORAGE_DIR: got %q, want %q", got, want)
	}

	withStorage := map[string]string{
		"USERPROFILE":        home,
		"ARGENT_STORAGE_DIR": filepath.Join(home, "s1"),
		"ARGENT_STORAGE":     filepath.Join(home, "s2"),
	}
	if got, want := DefaultStorage(envFrom(withStorage)), filepath.Join(home, "s2"); got != want {
		t.Fatalf("ARGENT_STORAGE wins: got %q, want %q", got, want)
	}
}

func TestResolvePrefersFlagsOverConfig(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "argent.json"), []byte(`{
		"model": "config-model",
		"provider": "anthropic",
		"maxTurns": 9
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := envFrom(map[string]string{"USERPROFILE": home, "HOME": home})

	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, Model: "flag-model"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "flag-model" {
		t.Errorf("model = %q, want the flag value", res.Model)
	}
	if res.MaxTurns != 9 {
		t.Errorf("maxTurns = %d, want 9 from config", res.MaxTurns)
	}
	if res.Protocol != string(provider.ProtocolAnthropic) {
		t.Errorf("protocol = %q, want the anthropic template protocol", res.Protocol)
	}
	if res.Sandbox != security.SandboxWorkspaceWrite {
		t.Errorf("sandbox = %v, want workspace-write", res.Sandbox)
	}
	if !res.Mutating {
		t.Error("workspace-write must allow mutating tools")
	}
	if res.Storage != filepath.Join(home, ".argent-go") {
		t.Errorf("storage = %q", res.Storage)
	}
	if res.Chatter == nil {
		t.Fatal("chatter is nil")
	}
}

func TestResolveHonoursReadOnlySandbox(t *testing.T) {
	root := t.TempDir()
	env := envFrom(map[string]string{"USERPROFILE": t.TempDir()})

	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, SandboxMode: "read-only"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Mutating {
		t.Fatal("read-only must disable mutating tools")
	}
}

func TestResolveYoloForcesAutoApproval(t *testing.T) {
	root := t.TempDir()
	env := envFrom(map[string]string{"USERPROFILE": t.TempDir()})

	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, Yolo: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Approval != "auto" {
		t.Fatalf("approval = %q, want auto", res.Approval)
	}
}

func TestToolNamesDeduplicatesInOrder(t *testing.T) {
	result := agent.Result{Messages: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{Function: llm.ToolCallFunction{Name: "read"}},
			{Function: llm.ToolCallFunction{Name: "grep"}},
		}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			{Function: llm.ToolCallFunction{Name: "read"}},
			{Function: llm.ToolCallFunction{Name: "  "}},
		}},
	}}
	got := toolNames(result)
	want := []string{"read", "grep"}
	if len(got) != len(want) {
		t.Fatalf("toolNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("toolNames = %v, want %v", got, want)
		}
	}
}
