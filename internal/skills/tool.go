package skills

import (
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/commands"
	"example.com/argentgo/internal/security"
)

// ToolSpec 描述调用方可用自己的 registry 注册的工具。
//
// 它镜像每个 provider 期望的线上形态（`type: function`、
// `function: {name, description, parameters}`），但刻意使用本地类型
// 使本包不依赖 agent 或 llm 包。
type ToolSpec struct {
	Type     string     `json:"type"`
	Function ToolSchema `json:"function"`
}

// ToolSchema 是 ToolSpec 的函数部分。
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// UseSkillSpec 返回调用方应公布的工具，使模型能按名称加载技能正文。
// 使模型能按名称加载技能正文。
func UseSkillSpec() ToolSpec {
	return ToolSpec{
		Type: "function",
		Function: ToolSchema{
			Name:        "use_skill",
			Description: "Load a skill's full procedure by name. Omit the name to list what is installed. Pass arguments to fill the skill's $ARGUMENTS / $N placeholders.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Skill name from the skills index in the system prompt"
    },
    "arguments": {
      "type": "string",
      "description": "Optional arguments used to expand the skill body's $ARGUMENTS / $1 / ${1} placeholders"
    }
  },
  "additionalProperties": false
}`),
		},
	}
}

// UseSkillSpec 是同一个规格绑定到 registry 的版本，供按会话构建它的调用方使用。
// 供按会话构建它的调用方使用。
func (r *Registry) UseSkillSpec() ToolSpec { return UseSkillSpec() }

// HandleUseSkill 用原始 JSON 参数执行 use_skill 工具。
//
// 空或缺失的 `name` 列出已安装技能；已知名称返回
// 带来源路径的完整正文；未知名称返回错误，其消息
// 建议相近的匹配项。提供 `arguments` 时按命令占位符规则
// 展开正文；未提供时正文逐字节保持不变。
func (r *Registry) HandleUseSkill(args json.RawMessage) (string, error) {
	var call struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &call); err != nil {
			return "", fmt.Errorf("use_skill: invalid arguments: %w", err)
		}
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		if r == nil || r.Len() == 0 {
			return "No skills are installed. Add SKILL.md directories under .argent/skills or ~/.argentgo/skills, ~/.agents/skills, ~/.claude/skills.", nil
		}
		var b strings.Builder
		b.WriteString("Installed skills:\n")
		// 收紧后只列举当前上下文可见的技能：无名列出的名字绝不能暴露被
		// 触发器排除的技能，否则模型仍会按索引中的名字尝试加载它们。
		for _, skill := range r.ActiveSkills() {
			// 与系统提示索引一致：列举输出同样先脱敏不可见字符再折叠为单行。
			name := oneLine(security.RedactArtifactContent(skill.Name))
			desc := oneLine(security.RedactArtifactContent(skill.Description))
			fmt.Fprintf(&b, "- %s — %s\n  %s\n", name, desc, skill.Path)
		}
		return b.String(), nil
	}
	if r == nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	skill, err := r.Load(name)
	if err != nil {
		return "", err
	}
	body := skill.Body
	// 未提供参数时完全跳过展开，确保正文逐字节不变。
	if call.Arguments != "" {
		body = commands.ExpandArguments(body, call.Arguments)
	}
	// 抬头与系统提示索引共用同一套脱敏/折叠/裁剪，消除“索引净化名 vs 加载原始名”
	// 的不一致；正文按需加载，保持逐字节不变。
	shownName := clipIndexField(skill.Name)
	source := clipIndexField(skill.Path)
	return fmt.Sprintf("# skill: %s\n(source: %s)\n\n%s", shownName, source, body), nil
}
