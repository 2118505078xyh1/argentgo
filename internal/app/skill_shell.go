package app

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// skillShellOptInKeys 是让某次运行选择执行 SKILL.md 正文中内嵌的 `!`cmd“ 片段的配置键。
// 它们刻意对 internal/config 不可见：app 从 Config.Raw 读取它们，与 cacheTier 和 hooks 一样，
// 因此无需改动任何更低层，此接线即可生效。
var skillShellOptInKeys = []string{"allowSkillShell", "allow-skill-shell", "skillShell", "skill_shell"}

// skillShellEnabled 是技能 shell 注入的信任门槛。除非操作人员显式选择启用，否则为 false，
// 且 KILO_DISABLE_SKILL_SHELL 无条件优先。保持默认关闭意味着未配置运行的 use_skill 输出
// 逐字节不变，且绝不会根据技能内容派生 shell。一旦启用，每条命令仍会经过完整的安全审批链
// （见 toolset.runSkillShell）。
func skillShellEnabled(raw map[string]any, getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	if skillShellKillSwitchOn(getenv(skills.ShellKillSwitchEnv)) {
		return false
	}
	return cfgBool(raw, skillShellOptInKeys...)
}

// skillShellKillSwitchOn 把除常见假值写法之外的任何值都视为生效的关闭开关，
// 因此误设的 "yes"/"1"/"true" 总会禁用执行，只有显式的 false 才能重新启用。
func skillShellKillSwitchOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// cfgBool 从合并配置树中读取第一个为真的类布尔键。它接受原生布尔值、常见的字符串写法和 JSON 数字。
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

// skillShellToolName 是用于执行解析出的命令的内置 shell 工具。两个名字总是被注册，
// 且在派生进程之前都经过安全链。
func skillShellToolName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

// runSkillShell 把一条命令交给内置 shell 工具。该路径应用完整的 策略 -> 审批 -> 审计 链，
// 加上注册表的输出和超时上限，因此来自技能的命令绝不会比模型发起的 shell 调用拥有更高权限。
func (t *toolset) runSkillShell(ctx context.Context, command string) (string, error) {
	args, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		return "", err
	}
	return t.base.Run(ctx, skillShellToolName(), string(args))
}

// expandSkillShell 把已加载技能正文中每个可执行的 `!`cmd“ 片段，替换为该命令经过安全链执行后的输出。
// 解析由 internal/skills 负责；本函数只增加执行和替换。片段按顺序应用，使解析器给出的字节偏移保持有效。
// 失败或被拒绝的命令会变成模型可见的简短可读标记——绝不静默丢弃，也绝不输出原始 stderr。
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
			continue // 防御性：绝不在解析出的正文之外切片
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

// boundedSkillShellError 裁剪工具错误并剥离控制字符，使恶意命令无法通过注入的标记淹没模型的上下文。
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
