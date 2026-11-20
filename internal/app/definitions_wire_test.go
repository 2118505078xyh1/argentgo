package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// resetSharedDefs 为测试安装一个新的 DefinitionCache 单例。发现调用点使用包级单例（没有可挂字段的 App/Factory 接收者），
// 因此断言缓存计数的测试需要从一个干净的单例开始。该辅助函数不调用 t.Parallel；本包中唯一的并行测试
// 只演练运行器，从不触碰 defs。
// （接上行）
// （接上行）
func resetSharedDefs(t *testing.T) *DefinitionCache {
	t.Helper()
	defsOnce = sync.Once{}
	c := sharedDefs()
	t.Cleanup(func() {
		defsOnce = sync.Once{}
		defs = nil
	})
	return c
}

// writeWireSkill 在一个目录中放置一个可被发现的 SKILL.md，使用隔离的工作区根目录，使指纹只反映该根目录。
// （接上行）
func writeWireSkill(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(skills.DefaultProjectSubdir), name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: ship it\n---\nStep one.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWiredDefinitionsCacheDedupesSystemBuilds 证明接线：从相同根目录构建的两个系统提示只执行一次发现，Refresh 强制重新发现，
// 且技能仍出现在提示中（无行为变化）。
// （接上行）
func TestWiredDefinitionsCacheDedupesSystemBuilds(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWireSkill(t, root, "deploy")

	c := resetSharedDefs(t)
	if h, m, r := c.Stats(); h != 0 || m != 0 || r != 0 {
		t.Fatalf("fresh cache stats = (%d,%d,%d), want (0,0,0)", h, m, r)
	}

	prompt1 := buildSystemWithTrust(root, env, true, "", "")
	prompt2 := buildSystemWithTrust(root, env, true, "", "")
	if prompt1 != prompt2 {
		t.Fatal("two builds from the same roots produced different prompts")
	}
	if !strings.Contains(prompt1, "deploy") {
		t.Fatalf("system prompt lost the discovered skill:\n%s", prompt1)
	}
	assertDefStats(t, c, 1, 1, 0)

	c.Refresh()
	prompt3 := buildSystemWithTrust(root, env, true, "", "")
	if !strings.Contains(prompt3, "deploy") {
		t.Fatalf("skill missing after Refresh:\n%s", prompt3)
	}
	assertDefStats(t, c, 1, 2, 1)
}

// TestWiredDefinitionsCacheSharedAcrossCallSites 证明 buildSystemWithTrust 和 contextPrompt 共享一个快照：第二个调用点由缓存服务，
// 而非重新扫描相同的技能根目录。
// （接上行）
func TestWiredDefinitionsCacheSharedAcrossCallSites(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWireSkill(t, root, "deploy")

	c := resetSharedDefs(t)
	buildSystemWithTrust(root, env, true, "", "")
	assertDefStats(t, c, 0, 1, 0)

	res := Resolved{Root: root, Sandbox: security.SandboxReadOnly, Approval: "ask"}
	report, ok := contextPrompt(res, cli.IO{Env: env}, "/context")
	if !ok {
		t.Fatal("/context was not routed to the built-in hook")
	}
	if !strings.Contains(report, "skills: 1") {
		t.Fatalf("context report did not count the cached skill:\n%s", report)
	}
	assertDefStats(t, c, 1, 1, 0)
}
