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

// skillShellFixture builds a toolset whose skill registry holds one skill with
// the given body. run is the injected shell executor; nil means "must not run".
func skillShellFixture(body string, on bool, run func(context.Context, string) (string, error)) *toolset {
	registry := skills.New()
	registry.Add(skills.Skill{Name: "demo", Description: "demo skill", Path: "/skills/demo/SKILL.md", Body: body})
	return &toolset{skills: registry, skillShellOn: on, skillShellRun: run}
}

// skillShellHeader is the wrapper HandleUseSkill adds in front of a loaded body.
const skillShellHeader = "# skill: demo\n(source: /skills/demo/SKILL.md)\n\n"

func skillShellCall(t *testing.T, set *toolset) string {
	t.Helper()
	out, err := set.Run(context.Background(), "use_skill", `{"name":"demo"}`)
	if err != nil {
		t.Fatalf("use_skill: %v", err)
	}
	return out
}

// TestSkillShellDefaultOffKeepsBodyUntouched pins the lowest-risk invariant:
// with no opt-in, use_skill returns the exact body and never executes a shell.
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

// TestSkillShellSubstitutesOutputsAtExecutableSpans proves the complete closed
// loop: a loaded skill's executable spans are parsed, each command is executed,
// and its output is folded back at the original span. Ordinary inline code and
// duplicate commands (the parser drops them) are left as inert text.
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

// TestSkillShellDenialBecomesReadableMarker proves the execution is routed
// through the security chain, not a bare exec: a denying guard is consulted
// with the shell tool and the parsed command, no shell process runs, and the
// model receives a bounded, readable error marker in the skill body.
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

// recordingDenyGuard is a tools.Guard that records the call and denies it.
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

// TestSkillShellEnabledGate pins the trust gate: default off, opt-in spellings,
// and the kill switch overriding a positive opt-in.
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

// TestSkillShellListingNeverExecutesMetadata proves the nameless use_skill
// (skills listing) is inert even when a skill's description carries an
// executable-looking span: only a named, loaded body may reach the shell.
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

// TestSkillShellFencedAndEscapedSpansAreInert verifies the wiring inherits the
// parser's conservative behaviour: fenced blocks and escaped backticks never
// reach the executor.
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
