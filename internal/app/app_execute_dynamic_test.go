package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

// dynamicCaptureChatter 记录每条消息的角色和内容，使测试能断言提示的每一部分最终落在哪里。
// （接上行）
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

// executeDynamic 在启用信任门控的情况下运行一个提示，并返回实际发送给模型的系统和第一条用户消息。
// （接上行）
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

// TestExecuteCachesStaticSystemAcrossProcessChanges 证明接线行为：两次启用信任的运行即使 cwd 和思考级别不同，也发送逐字节相同的
// 系统前缀，而逐运行的值只通过用户消息传递。
// （接上行）
func TestExecuteCachesStaticSystemAcrossProcessChanges(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir() // 没有文档或技能，因此只有进程上下文不同

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

// TestExecuteDynamicSplitKeepsAllInfo 证明拆分不丢失任何信息：静态那一半仍携带角色设定、工具指引和注入的 AGENTS.md/技能，
// 而动态那一半携带 cwd、思考级别和 schema。
// （接上行）
func TestExecuteDynamicSplitKeepsAllInfo(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWiringFile(t, filepath.Join(root, "AGENTS.md"), "TRUSTED_DYNAMIC_MARKER_4c1a")
	writeWireSkill(t, root, "deploy")
	const schema = `{"type":"object","properties":{"ok":{"type":"boolean"}}}`

	system, user := executeDynamic(t, env, root, "medium", schema)
	for _, want := range []string{
		"You are argentgo",
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
