package app

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// skillShellOptInKeys are the config keys that opt a run into executing the
// `!`cmd“ spans embedded in SKILL.md bodies. They are deliberately unknown to
// internal/config: app reads them from Config.Raw, the same way cacheTier and
// hooks do, so no lower layer has to change for this wiring to take effect.
var skillShellOptInKeys = []string{"allowSkillShell", "allow-skill-shell", "skillShell", "skill_shell"}

// skillShellEnabled is the trust gate for skill shell injection. It is false
// unless the operator explicitly opts in, and KILO_DISABLE_SKILL_SHELL wins
// unconditionally. Keeping the default off means an unconfigured run's
// use_skill output stays byte-identical and never spawns a shell from skill
// content. Once enabled, every command still goes through the full security
// approval chain (see toolset.runSkillShell).
func skillShellEnabled(raw map[string]any, getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	if skillShellKillSwitchOn(getenv(skills.ShellKillSwitchEnv)) {
		return false
	}
	return cfgBool(raw, skillShellOptInKeys...)
}

// skillShellKillSwitchOn treats any value except the usual falsy spellings as
// an active kill switch, so an accidental "yes"/"1"/"true" always disables
// execution and only an explicit false re-enables it.
func skillShellKillSwitchOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// cfgBool reads the first truthy boolean-ish key from a merged config tree.
// It accepts native booleans, the common string spellings and JSON numbers.
func cfgBool(tree map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := tree[key].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "1", "true", "yes", "on":
				return true
			}
		case float64:
			if value != 0 {
				return true
			}
		}
	}
	return false
}

// skillShellToolName is the built-in shell tool used to execute a parsed
// command. Both names are always registered and both route through the
// security chain before a process is spawned.
func skillShellToolName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

// runSkillShell hands one command to the built-in shell tool. That path applies
// the full policy -> approval -> audit chain plus the registry's output and
// timeout caps, so a command from a skill is never more privileged than a
// model-initiated shell call.
func (t *toolset) runSkillShell(ctx context.Context, command string) (string, error) {
	args, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		return "", err
	}
	return t.base.Run(ctx, skillShellToolName(), string(args))
}

// expandSkillShell replaces each executable `!`cmd“ span in a loaded skill
// body with the output of running that command through the security chain.
// Parsing is owned by internal/skills; this function only adds execution and
// substitution. Spans are applied in order so the byte offsets from the parser
// stay valid. A failed or denied command becomes a short, readable marker the
// model can see -- never a silent drop and never raw stderr.
func (t *toolset) expandSkillShell(ctx context.Context, body string) string {
	commands, spans := skills.ParseShellCommands(body)
	if len(commands) == 0 {
		return body
	}

	var out strings.Builder
	prev := 0
	for i, command := range commands {
		span := spans[i]
		if span.Start < prev || span.End > len(body) || span.End <= span.Start {
			continue // defensive: never slice outside the parsed body
		}
		out.WriteString(body[prev:span.Start])

		var text string
		var err error
		if t.skillShellRun != nil {
			text, err = t.skillShellRun(ctx, command)
		} else {
			text, err = t.runSkillShell(ctx, command)
		}
		if err != nil {
			out.WriteString(boundedSkillShellError(err.Error()))
		} else {
			out.WriteString(strings.TrimSpace(text))
		}
		prev = span.End
	}
	out.WriteString(body[prev:])
	return out.String()
}

// boundedSkillShellError clips a tool error and strips control characters so a
// hostile command cannot flood the model's context through the injected marker.
func boundedSkillShellError(message string) string {
	const max = 512
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(message))
	if runes := []rune(message); len(runes) > max {
		message = string(runes[:max]) + "..."
	}
	return "[use_skill shell: " + message + "]"
}
