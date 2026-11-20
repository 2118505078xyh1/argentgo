package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// dynamicCaptureChatter records the role and content of every message so the
// tests can assert where each piece of the prompt ended up.
type dynamicCaptureChatter struct {
	systems []string
	users   []string
}

func (c *dynamicCaptureChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	for _, message := range messages {
		switch message.Role {
		case "system":
			c.systems = append(c.systems, message.Content)
		case "user":
			c.users = append(c.users, message.Content)
		}
	}
	return llm.Message{Role: "assistant", Content: `{"ok":true}`}, nil
}

// executeDynamic runs one prompt with the trust gate opted in and returns the
// system and first user message actually sent to the model.
func executeDynamic(t *testing.T, env func(string) string, root, thinking, schema string) (string, string) {
	t.Helper()
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root, Thinking: thinking, OutputSchema: schema})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.TrustProject = true
	res.Approve = autoApprover{}
	chatter := &dynamicCaptureChatter{}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("dynamic-context")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "do the thing", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(chatter.systems) == 0 || len(chatter.users) == 0 {
		t.Fatalf("model never called: systems=%v users=%v", chatter.systems, chatter.users)
	}
	return chatter.systems[0], chatter.users[0]
}

// TestExecuteCachesStaticSystemAcrossProcessChanges proves the wired behaviour:
// two trust-opted runs with different cwd and thinking send byte-identical
// system prefixes, while the per-run values travel in the user message only.
func TestExecuteCachesStaticSystemAcrossProcessChanges(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir() // no docs or skills, so only process context differs

	systemA, userA := executeDynamic(t, env, rootA, "low", "")
	systemB, userB := executeDynamic(t, env, rootB, "high", "")

	if systemA != systemB {
		t.Fatalf("cached system prefix drifted across cwd/thinking:\nA: %q\nB: %q", systemA, systemB)
	}
	for _, leaked := range []string{"Working directory", "Reasoning effort"} {
		if strings.Contains(systemA, leaked) {
			t.Fatalf("system prefix leaked dynamic value %q: %q", leaked, systemA)
		}
	}
	for _, want := range []string{
		"Working directory: " + rootA,
		"Reasoning effort: low. Spend accordingly.",
		"do the thing",
	} {
		if !strings.Contains(userA, want) {
			t.Fatalf("first user message missing %q: %q", want, userA)
		}
	}
	for _, want := range []string{
		"Working directory: " + rootB,
		"Reasoning effort: high. Spend accordingly.",
	} {
		if !strings.Contains(userB, want) {
			t.Fatalf("second run user message missing %q: %q", want, userB)
		}
	}
}

// TestExecuteDynamicSplitKeepsAllInfo proves nothing is lost by the split: the
// static half still carries the persona, tool guidance and injected
// AGENTS.md/skills, while the dynamic half carries cwd, thinking and schema.
func TestExecuteDynamicSplitKeepsAllInfo(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWiringFile(t, filepath.Join(root, "AGENTS.md"), "TRUSTED_DYNAMIC_MARKER_4c1a")
	writeWireSkill(t, root, "deploy")
	const schema = `{"type":"object","properties":{"ok":{"type":"boolean"}}}`

	system, user := executeDynamic(t, env, root, "medium", schema)
	for _, want := range []string{
		"You are argent",
		"Use the available tools",
		"TRUSTED_DYNAMIC_MARKER_4c1a",
		"deploy",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("static system missing %q: %q", want, system)
		}
	}
	for _, want := range []string{
		"Working directory: " + root,
		"Reasoning effort: medium. Spend accordingly.",
		"Reply with a single JSON document that conforms to this JSON Schema:",
		schema,
		"do the thing",
	} {
		if !strings.Contains(user, want) {
			t.Fatalf("dynamic user message missing %q: %q", want, user)
		}
	}
}
