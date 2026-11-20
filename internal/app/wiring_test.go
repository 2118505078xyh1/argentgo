package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// wiringEnv isolates both the workspace and the user-home boundary so the
// boundary loader cannot accidentally pick up a real AGENTS.md from the host.
func wiringEnv(t *testing.T) (func(string) string, string, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	env := envFrom(map[string]string{"USERPROFILE": home, "HOME": home})
	return env, root, home
}

func writeWiringFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBuildSystemTrustGate(t *testing.T) {
	env, root, _ := wiringEnv(t)
	const marker = "TRUSTED_AGENT_MARKER_7f3a"
	writeWiringFile(t, filepath.Join(root, "AGENTS.md"), marker)

	// Explicitly trusted projects load AGENTS.md through LoadWithBoundary.
	if got := buildSystemWithTrust(root, env, true, "", ""); !strings.Contains(got, marker) {
		t.Fatalf("trusted system prompt dropped AGENTS.md:\n%s", got)
	}
	// Untrusted projects drop every project-level document.
	if got := buildSystemWithTrust(root, env, false, "", ""); strings.Contains(got, marker) {
		t.Fatalf("untrusted system prompt still contains AGENTS.md:\n%s", got)
	}
}

func TestBuildSystemLegacyDefaultKeepsAgents(t *testing.T) {
	env, root, _ := wiringEnv(t)
	const marker = "LEGACY_AGENT_MARKER_2b91"
	writeWiringFile(t, filepath.Join(root, "AGENTS.md"), marker)

	// Existing callers use buildSystem, whose default trust is true so the
	// historical "always inject AGENTS.md" behaviour is preserved.
	if got := buildSystem(root, env, "", ""); !strings.Contains(got, marker) {
		t.Fatalf("legacy system prompt dropped AGENTS.md:\n%s", got)
	}

	// ARGENT_TRUST_PROJECT=0 is the explicit opt-in to the boundary gate.
	gateEnv := envFrom(map[string]string{
		"USERPROFILE":          filepath.Dir(root),
		"HOME":                 filepath.Dir(root),
		"ARGENT_TRUST_PROJECT": "0",
	})
	if got := buildSystem(root, gateEnv, "", ""); strings.Contains(got, marker) {
		t.Fatalf("gated system prompt still contains AGENTS.md:\n%s", got)
	}
}

func TestContextPromptRegisteredAndProducesReport(t *testing.T) {
	env, root, home := wiringEnv(t)
	set := commands.NewSetWithBuiltins(commands.DefaultSources(root, home))
	if cmd, ok := set.Get("context"); !ok || cmd.Path != "<builtin:context>" {
		t.Fatalf("/context not registered: %+v, ok=%v", cmd, ok)
	}

	res := Resolved{Root: root, Sandbox: security.SandboxReadOnly, Approval: "ask"}
	injection, ok := contextPrompt(res, cli.IO{Env: env}, "/context")
	if !ok {
		t.Fatal("/context was not routed to the built-in hook")
	}
	for _, want := range []string{
		"context: read-only view of injected inputs",
		"sandbox: read-only",
		"approval: ask",
		"commands: 2",
	} {
		if !strings.Contains(injection, want) {
			t.Fatalf("report missing %q:\n%s", want, injection)
		}
	}

	if _, ok := contextPrompt(res, cli.IO{Env: env}, "just a normal prompt"); ok {
		t.Fatal("a plain prompt must not be hijacked by /context")
	}
}

// promptCaptureChatter records user messages so the Execute-level routing of
// /context can be asserted without a model or network.
type promptCaptureChatter struct {
	prompts []string
}

func (c *promptCaptureChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	for _, message := range messages {
		if message.Role == "user" {
			c.prompts = append(c.prompts, message.Content)
		}
	}
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

func TestExecuteRoutesContextReportToModel(t *testing.T) {
	env, root, _ := wiringEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	chatter := &promptCaptureChatter{}
	res.Chatter = chatter
	res.Approve = autoApprover{}
	store := testStore(t, res)
	sess, err := store.NewSession("context-wiring")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "/context", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(chatter.prompts) == 0 || !strings.Contains(chatter.prompts[0], "context: read-only view of injected inputs") {
		t.Fatalf("/context report did not reach the model: %v", chatter.prompts)
	}
}

func TestArgentIgnoreExcludesInjectedDoc(t *testing.T) {
	env, root, _ := wiringEnv(t)
	const marker = "IGNORED_AGENT_MARKER_9c04"
	writeWiringFile(t, filepath.Join(root, "AGENTS.md"), marker)

	// Without an ignore file the document is injected.
	if got := buildSystemWithTrust(root, env, true, "", ""); !strings.Contains(got, marker) {
		t.Fatalf("baseline system prompt missing AGENTS.md:\n%s", got)
	}

	writeWiringFile(t, filepath.Join(root, ".argentignore"), "AGENTS.md\n")
	if got := buildSystemWithTrust(root, env, true, "", ""); strings.Contains(got, marker) {
		t.Fatalf(".argentignore did not exclude the document:\n%s", got)
	}

	docs := projectMemoryDocs(root, env, true)
	for _, doc := range docs {
		if filepath.Base(doc.Path) == "AGENTS.md" {
			t.Fatalf("ignored document still present: %s", doc.Path)
		}
	}
}
