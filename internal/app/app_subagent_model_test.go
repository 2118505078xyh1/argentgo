package app

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestConfigureSubagentsUsesSecondaryModel 验证配置次要模型后，委派配置携带该模型，
// 且父级模型与逐角色解析保持原样。
func TestConfigureSubagentsUsesSecondaryModel(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "Review carefully.", "read")
	res := Resolved{Root: root, System: "base system"}
	res.Options.SecondaryModel = "weak-model"
	opts := &agent.Options{System: res.System, Model: "primary-model"}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent == nil {
		t.Fatal("Subagent is nil for a workspace with personas")
	}
	if got := opts.Subagent.Model; got != "weak-model" {
		t.Errorf("Subagent.Model = %q, want %q", got, "weak-model")
	}
	if opts.Model != "primary-model" {
		t.Errorf("parent Model = %q, want it unchanged", opts.Model)
	}
	if cfg, ok := opts.Subagent.ResolvePersona("reviewer"); !ok || cfg.System != "Review carefully." {
		t.Errorf("persona resolution changed: cfg=%+v ok=%v", cfg, ok)
	}
}

// TestConfigureSubagentsSecondaryModelEmptyInherits 验证未配置次要模型时保持零值，
// 即子代理继承父级模型——默认行为逐字节不变。
func TestConfigureSubagentsSecondaryModelEmptyInherits(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "Review carefully.", "read")
	res := Resolved{Root: root, System: "base system"}
	// res.Options.SecondaryModel 未设置，保持空字符串。
	opts := &agent.Options{System: res.System}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent == nil {
		t.Fatal("Subagent is nil for a workspace with personas")
	}
	if got := opts.Subagent.Model; got != "" {
		t.Errorf("Subagent.Model = %q, want empty so the child inherits the parent model", got)
	}
}

// TestConfigureSubagentsCJKSecondaryModel 验证 CJK 模型名原样透传，不做裁剪或改写。
func TestConfigureSubagentsCJKSecondaryModel(t *testing.T) {
	cases := []string{
		"通义千问/qwen-plus",
		"智谱/GLM-4-闪",
		"月之暗面-kimi",
	}
	for _, model := range cases {
		t.Run(model, func(t *testing.T) {
			root := t.TempDir()
			writeAppPersona(t, root, "reviewer", "You are a subagent. Review.", "read")
			res := Resolved{Root: root, System: "base system"}
			res.Options.SecondaryModel = model
			opts := &agent.Options{System: res.System}

			configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

			if opts.Subagent == nil || opts.Subagent.Model != model {
				t.Fatalf("Subagent.Model = %+v, want verbatim %q", opts.Subagent, model)
			}
		})
	}
}

// TestConfigureSubagentsMultiplePersonasShareSecondaryModel 验证多个子代理共用同一个
// 次要模型：根配置只携带一次，所有具名角色仍能各自解析。
func TestConfigureSubagentsMultiplePersonasShareSecondaryModel(t *testing.T) {
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "You are a subagent reviewer.", "read")
	writeAppPersona(t, root, "writer", "You are a subagent writer.", "write")
	writeAppPersona(t, root, "tester", "You are a subagent tester.", "read, grep")
	res := Resolved{Root: root, System: "base system"}
	res.Options.SecondaryModel = "shared-weak-model"
	opts := &agent.Options{System: res.System}

	configureSubagents(opts, res, agent.NewBackgroundResults(0, 0))

	if opts.Subagent == nil {
		t.Fatal("Subagent is nil for a workspace with personas")
	}
	if got := opts.Subagent.Model; got != "shared-weak-model" {
		t.Fatalf("Subagent.Model = %q, want %q", got, "shared-weak-model")
	}
	for name, wantSystem := range map[string]string{
		"reviewer": "You are a subagent reviewer.",
		"writer":   "You are a subagent writer.",
		"tester":   "You are a subagent tester.",
	} {
		cfg, ok := opts.Subagent.ResolvePersona(name)
		if !ok {
			t.Errorf("ResolvePersona(%q) failed", name)
			continue
		}
		if cfg.System != wantSystem {
			t.Errorf("ResolvePersona(%q).System = %q, want %q", name, cfg.System, wantSystem)
		}
	}
}

// modelCall 记录一次模型请求所用的模型，以及它是否来自子代理。
type modelCall struct {
	model   string
	isChild bool
}

// subModelChatter 在不使用真实模型的前提下，记录主/子运行各自使用的模型。
// 根运行的第一次调用发出两个 task 委派（覆盖多个子代理），其余轮次给出最终答案；
// 子代理立即返回文本答案。系统提示中的 "You are a subagent" 用于区分两者。
type subModelChatter struct {
	mu         sync.Mutex
	calls      []modelCall
	rootCalls  int
	childCalls int
}

func (c *subModelChatter) Chat(_ context.Context, model string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	isChild := false
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "You are a subagent") {
			isChild = true
			break
		}
	}
	c.mu.Lock()
	c.calls = append(c.calls, modelCall{model: model, isChild: isChild})
	rootTurn := c.rootCalls
	if isChild {
		c.childCalls++
	} else {
		c.rootCalls++
	}
	c.mu.Unlock()

	if isChild {
		return llm.Message{Role: "assistant", Content: "child answer"}, nil
	}
	if rootTurn == 0 {
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.ToolCallFunction{
				Name:      agent.TaskToolName,
				Arguments: `{"description":"review","prompt":"review code","persona":"reviewer"}`,
			}},
			{ID: "c2", Type: "function", Function: llm.ToolCallFunction{
				Name:      agent.TaskToolName,
				Arguments: `{"description":"write","prompt":"write docs","persona":"writer"}`,
			}},
		}}, nil
	}
	return llm.Message{Role: "assistant", Content: "root answer"}, nil
}

// TestExecuteRoutesSubagentsToSecondaryModel 端到端验证接线：配置了次要模型后，
// 多个子代理的请求都使用该模型，而根运行始终使用主模型。
func TestExecuteRoutesSubagentsToSecondaryModel(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	writeAppPersona(t, root, "reviewer", "You are a subagent reviewer. Review.", "read")
	writeAppPersona(t, root, "writer", "You are a subagent writer. Write.", "write")

	res, err := Resolve(cli.IO{Env: env}, cli.Options{
		Cwd:            root,
		Model:          "primary-model",
		SecondaryModel: "weak-child-model",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	chatter := &subModelChatter{}
	res.Chatter = chatter
	res.Approve = autoApprover{}
	store := testStore(t, res)
	sess, err := store.NewSession("subagent-secondary-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "prompt", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	chatter.mu.Lock()
	defer chatter.mu.Unlock()
	if chatter.childCalls != 2 {
		t.Fatalf("child calls = %d, want 2 (one per delegated persona)", chatter.childCalls)
	}
	if chatter.rootCalls == 0 {
		t.Fatal("the root model was never called")
	}
	for i, call := range chatter.calls {
		want := "primary-model"
		if call.isChild {
			want = "weak-child-model"
		}
		if call.model != want {
			t.Errorf("call %d (child=%v) model = %q, want %q", i, call.isChild, call.model, want)
		}
	}
}
