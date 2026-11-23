package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// writeAppPersona 把一个有效的子 agent 定义放入工作区根目录。
func writeAppPersona(t *testing.T, root, name, system, tools string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(personaDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\nsubagent: true\ntools: " + tools + "\n---\n" + system + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureSubagentsWithOptIn(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "Review carefully.", "read, grep")
	res := Resolved{Root: root, System: "base system"}
	opts := &agent.Options{System: res.System}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent == nil {
		t.Fatal("Subagent is nil for a workspace with personas")
	}
	if opts.System != "base system\n\n"+agent.OrchestratorPrompt() {
		t.Fatalf("System = %q, want the base prompt plus the orchestrator prompt", opts.System)
	}
	if opts.Subagent.MaxDepth != agent.DefaultMaxDepth {
		t.Errorf("MaxDepth = %d, want %d", opts.Subagent.MaxDepth, agent.DefaultMaxDepth)
	}
	if opts.Subagent.Model != "" {
		t.Errorf("Model = %q, want empty so the child inherits the parent model", opts.Subagent.Model)
	}
	if opts.Subagent.ResolvePersona == nil {
		t.Fatal("ResolvePersona was not injected")
	}
	cfg, ok := opts.Subagent.ResolvePersona("reviewer")
	if !ok {
		t.Fatal("ResolvePersona did not find the reviewer persona")
	}
	if cfg.System != "Review carefully." {
		t.Errorf("persona System = %q, want %q", cfg.System, "Review carefully.")
	}
	if len(cfg.AllowedTools) != 2 || cfg.AllowedTools[0] != "read" || cfg.AllowedTools[1] != "grep" {
		t.Errorf("persona AllowedTools = %v, want [read grep]", cfg.AllowedTools)
	}
	if _, ok := opts.Subagent.ResolvePersona("missing"); ok {
		t.Error("an unknown persona must not resolve")
	}
}

func TestConfigureSubagentsKeepsQueuePerRun(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "Review carefully.", "read")
	res := Resolved{Root: root, System: "base system"}

	bgA := agent.NewBackgroundResults(0, 0)
	bgB := agent.NewBackgroundResults(0, 0)
	optsA := &agent.Options{System: res.System}
	optsB := &agent.Options{System: res.System}
	configureSubagents(optsA, res, bgA)
	configureSubagents(optsB, res, bgB)

	if optsA.Subagent == nil || optsB.Subagent == nil {
		t.Fatal("both opted-in runs must have a Subagent config")
	}
	if optsA.Subagent.Background != bgA || optsB.Subagent.Background != bgB {
		t.Fatal("each run must keep the caller-owned queue")
	}
	if optsA.Subagent.Background == optsB.Subagent.Background {
		t.Fatal("two root runs must not share one background queue")
	}
	// 即使在相同的根深度下，运行 A 的结果对运行 B 也是不可见的。
	bgA.PushFor(bgA.RootOwner(), 0, "A-only")
	if got := bgB.DrainFor(bgB.RootOwner()); len(got) != 0 {
		t.Fatalf("run B drained %v from run A's queue", got)
	}
	if got := bgA.DrainFor(bgA.RootOwner()); len(got) != 1 || got[0] != "A-only" {
		t.Fatalf("run A's queue = %v, want [A-only]", got)
	}
}

func TestConfigureSubagentsWithoutOptInKeepsDefaults(t *testing.T) {
	root := t.TempDir() // 没有 .argent/agents 目录
	res := Resolved{Root: root, System: "base system"}
	opts := &agent.Options{System: res.System}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent != nil {
		t.Fatalf("Subagent = %#v, want nil without personas", opts.Subagent)
	}
	if opts.System != "base system" {
		t.Fatalf("System = %q, want the system prompt left byte-identical", opts.System)
	}
}

func TestConfigureSubagentsIgnoresNonPersonaMarkdown(t *testing.T) {
	root := t.TempDir()
	// 未标记为子 agent 的 markdown 文件不得让运行启用委派。
	writeFile(t, filepath.Join(root, filepath.FromSlash(personaDir), "notes.md"), "just notes\n")
	res := Resolved{Root: root, System: "base system"}
	opts := &agent.Options{System: res.System}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent != nil || opts.System != "base system" {
		t.Fatalf("non-persona markdown changed the options: %#v, System=%q", opts.Subagent, opts.System)
	}
}

func TestPersonaSubagentConfigResolvesTempDir(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "writer", "Write the docs.", "read, write")

	cfg, ok := PersonaSubagentConfig(root, "writer")
	if !ok {
		t.Fatal("PersonaSubagentConfig did not resolve a loaded persona")
	}
	if cfg.System != "Write the docs." {
		t.Errorf("System = %q, want the persona body", cfg.System)
	}
	if len(cfg.AllowedTools) != 2 || cfg.AllowedTools[0] != "read" || cfg.AllowedTools[1] != "write" {
		t.Errorf("AllowedTools = %v, want [read write]", cfg.AllowedTools)
	}
	if _, ok := PersonaSubagentConfig(root, "unknown"); ok {
		t.Error("unknown persona must return false")
	}
	if _, ok := PersonaSubagentConfig(root, "  "); ok {
		t.Error("blank persona name must return false")
	}
}

// specCaptureChatter 记录系统提示和公布的工具，使
// 无需模型或网络即可断言生产环境的 Execute 接线。
type specCaptureChatter struct {
	mu      sync.Mutex
	systems []string
	specs   [][]string
}

func (c *specCaptureChatter) Chat(_ context.Context, _ string, messages []llm.Message, toolSpecs []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, message := range messages {
		if message.Role == "system" {
			c.systems = append(c.systems, message.Content)
		}
	}
	names := make([]string, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		names = append(names, spec.Function.Name)
	}
	c.specs = append(c.specs, names)
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

func executeAndCapture(t *testing.T, withPersona bool) (system string, specs []string, resolved Resolved) {
	t.Helper()
	env, _ := testEnv(t)
	root := t.TempDir()
	if withPersona {
		writeAppPersona(t, root, "reviewer", "Review carefully.", "read")
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	chatter := &specCaptureChatter{}
	res.Chatter = chatter
	res.Approve = autoApprover{}
	store := testStore(t, res)
	sess, err := store.NewSession("subagent-activation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(chatter.systems) == 0 || len(chatter.specs) == 0 {
		t.Fatal("the model was never called")
	}
	return chatter.systems[0], chatter.specs[0], res
}

func TestExecuteAdvertisesTaskToolOnlyWhenOptedIn(t *testing.T) {
	t.Run("with persona", func(t *testing.T) {
		system, specs, res := executeAndCapture(t, true)
		// 编辑格式指引与 repo map 只读声明均默认开启，按生产装配顺序追加在编排器提示之后。
		base := res.System + "\n\n" + agent.OrchestratorPrompt()
		want := strings.TrimSpace(agent.AppendEditFormat(base, res.Model) + "\n" + repoMapReadOnlyHint())
		if system != want {
			t.Fatalf("system prompt = %q, want %q", system, want)
		}
		found := false
		for _, name := range specs {
			if name == agent.TaskToolName {
				found = true
			}
		}
		if !found {
			t.Fatalf("advertised tools = %v, want the task tool", specs)
		}
	})

	t.Run("without persona", func(t *testing.T) {
		system, specs, res := executeAndCapture(t, false)
		// 编辑格式指引与 repo map 只读声明均默认开启，按生产装配顺序追加。
		if want := strings.TrimSpace(agent.AppendEditFormat(res.System, res.Model) + "\n" + repoMapReadOnlyHint()); system != want {
			t.Fatalf("system prompt = %q, want %q", system, want)
		}
		for _, name := range specs {
			if name == agent.TaskToolName {
				t.Fatalf("advertised tools = %v, task tool must be absent by default", specs)
			}
		}
	})
}
