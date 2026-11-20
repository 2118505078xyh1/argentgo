package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/skills"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// skillShellFixture 构建一个工具集，其技能注册表包含一个具有给定正文的技能。
// run 是注入的 shell 执行器；nil 表示"绝不能运行"。
func skillShellFixture(body string, on bool, run func(context.Context, string) (string, error)) *toolset {
	registry := skills.New()
	registry.Add(skills.Skill{Name: "demo", Description: "demo skill", Path: "/skills/demo/SKILL.md", Body: body})
	return &toolset{skills: registry, skillShellOn: on, skillShellRun: run}
}

// skillShellHeader 是 HandleUseSkill 在已加载正文前添加的包装。
const skillShellHeader = "# skill: demo\n(source: /skills/demo/SKILL.md)\n\n"

func skillShellCall(t *testing.T, set *toolset) string {
	t.Helper()
	out, err := set.Run(context.Background(), "use_skill", `{"name":"demo"}`)
	if err != nil {
		t.Fatalf("use_skill: %v", err)
	}
	return out
}

// TestSkillShellDefaultOffKeepsBodyUntouched 锁定风险最低的不变量：未启用时，use_skill 返回完全相同的正文，且从不执行 shell。
// （接上行）
func TestSkillShellDefaultOffKeepsBodyUntouched(t *testing.T) {
	body := "Step one.\nRun `!echo hi` then continue.\n"
	set := skillShellFixture(body, false, func(context.Context, string) (string, error) {
		t.Fatal("shell must not run when the feature is off")
		return "", nil
	})
	want := skillShellHeader + body
	if got := skillShellCall(t, set); got != want {
		t.Fatalf("body changed with feature off:\n got: %q\nwant: %q", got, want)
	}
}

// TestSkillShellSubstitutesOutputsAtExecutableSpans 证明完整闭环：已加载技能的可执行片段被解析，每条命令被执行，
// 其输出被折回原始片段。普通内联代码和重复命令（解析器会丢弃）保持为惰性文本。
// （接上行）
// （接上行）
func TestSkillShellSubstitutesOutputsAtExecutableSpans(t *testing.T) {
	body := "A `!cmd one` B `echo inert` C `!cmd one` D `!cmd two`"
	var ran []string
	set := skillShellFixture(body, true, func(_ context.Context, command string) (string, error) {
		ran = append(ran, command)
		return "OUT(" + command + ")", nil
	})
	got := skillShellCall(t, set)

	want := skillShellHeader + "A OUT(cmd one) B `echo inert` C `!cmd one` D OUT(cmd two)"
	if got != want {
		t.Fatalf("substituted body:\n got: %q\nwant: %q", got, want)
	}
	if len(ran) != 2 || ran[0] != "cmd one" || ran[1] != "cmd two" {
		t.Fatalf("commands run = %q, want [cmd one cmd two]", ran)
	}
}

// TestSkillShellDenialBecomesReadableMarker 证明执行经过安全链而非裸 exec：一个拒绝的防护器会与 shell 工具和解析出的命令一起被咨询，
// 没有 shell 进程运行，且模型在技能正文中收到一个有界、可读的错误标记。
// （接上行）
// （接上行）
func TestSkillShellDenialBecomesReadableMarker(t *testing.T) {
	const command = "rm -rf /should-not-run"
	body := "Before `!" + command + "` after."

	root := t.TempDir()
	registry := tools.NewWithLimit(root, true, 0)
	guard := &recordingDenyGuard{err: errors.New("policy denied by test")}
	registry.SetGuard(guard)

	skillsReg := skills.New()
	skillsReg.Add(skills.Skill{Name: "demo", Body: body})
	set := &toolset{
		base:         &multiRootTools{roots: []string{root}, regs: []*tools.Registry{registry}},
		skills:       skillsReg,
		skillShellOn: true,
	}

	got := skillShellCall(t, set)
	if guard.name != skillShellToolName() {
		t.Fatalf("guard checked tool %q, want %q", guard.name, skillShellToolName())
	}
	if !strings.Contains(guard.args, command) {
		t.Fatalf("guard args %q missing command %q", guard.args, command)
	}
	if !strings.Contains(got, "[use_skill shell:") || !strings.Contains(got, "policy denied by test") {
		t.Fatalf("denial not surfaced in body: %q", got)
	}
	if strings.Contains(got, command) && strings.Contains(got, "`!") {
		t.Fatalf("executable span should have been replaced: %q", got)
	}
}

// recordingDenyGuard 是一个记录调用并拒绝它的 tools.Guard。
type recordingDenyGuard struct {
	err  error
	name string
	args string
}

func (g *recordingDenyGuard) Check(_ context.Context, name string, args json.RawMessage) error {
	g.name = name
	g.args = string(args)
	return g.err
}

// TestSkillShellEnabledGate 锁定信任门槛：默认关闭、各种启用写法，以及关闭开关覆盖正向启用。
// （接上行）
func TestSkillShellEnabledGate(t *testing.T) {
	cases := []struct {
		name    string
		raw     map[string]any
		killEnv string
		want    bool
	}{
		{"unset is off", map[string]any{}, "", false},
		{"camelCase bool", map[string]any{"allowSkillShell": true}, "", true},
		{"kebab string", map[string]any{"allow-skill-shell": "yes"}, "", true},
		{"snake string one", map[string]any{"skill_shell": "1"}, "", true},
		{"explicit false", map[string]any{"allowSkillShell": false}, "", false},
		{"kill switch overrides opt-in", map[string]any{"allowSkillShell": true}, "1", false},
		{"kill switch false keeps opt-in", map[string]any{"allowSkillShell": true}, "false", true},
		{"kill switch alone cannot enable", map[string]any{}, "false", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == skills.ShellKillSwitchEnv {
					return tc.killEnv
				}
				return ""
			}
			if got := skillShellEnabled(tc.raw, getenv); got != tc.want {
				t.Fatalf("skillShellEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSkillShellListingNeverExecutesMetadata 证明无名的 use_skill（技能列表）是惰性的，即使某技能的描述带有看似可执行的片段：
// 只有具名、已加载的正文才能到达 shell。
// （接上行）
func TestSkillShellListingNeverExecutesMetadata(t *testing.T) {
	registry := skills.New()
	registry.Add(skills.Skill{
		Name:        "demo",
		Description: "danger `!rm listing-metadata`",
		Body:        "body `!run me`",
	})
	set := &toolset{skills: registry, skillShellOn: true, skillShellRun: func(context.Context, string) (string, error) {
		t.Fatal("listing metadata must not be executed")
		return "", nil
	}}
	out, err := set.Run(context.Background(), "use_skill", `{}`)
	if err != nil {
		t.Fatalf("use_skill list: %v", err)
	}
	if !strings.Contains(out, "!rm listing-metadata") {
		t.Fatalf("listing should preserve metadata verbatim: %q", out)
	}
}

// TestSkillShellFencedAndEscapedSpansAreInert 验证接线继承了解析器的保守行为：围栏代码块和转义的反引号永远不会到达执行器。
// （接上行）
// （接上行）
func TestSkillShellFencedAndEscapedSpansAreInert(t *testing.T) {
	body := "```\n`!inside fence`\n```\nEscaped \\`!also inert\\` and live `!run me`."
	var ran []string
	set := skillShellFixture(body, true, func(_ context.Context, command string) (string, error) {
		ran = append(ran, command)
		return "OK", nil
	})
	got := skillShellCall(t, set)

	if len(ran) != 1 || ran[0] != "run me" {
		t.Fatalf("commands run = %q, want [run me]", ran)
	}
	if !strings.Contains(got, "`!inside fence`") || !strings.Contains(got, "\\`!also inert\\`") {
		t.Fatalf("inert spans were altered: %q", got)
	}
	if !strings.Contains(got, "OK") {
		t.Fatalf("live span output missing: %q", got)
	}
}
