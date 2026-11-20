package skills

import (
	"encoding/json"
	"fmt"
	"strings"
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
			Description: "Load a skill's full procedure by name. Omit the name to list what is installed.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Skill name from the skills index in the system prompt"
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
// 建议相近的匹配项。
func (r *Registry) HandleUseSkill(args json.RawMessage) (string, error) {
	var call struct {
		Name string `json:"name"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &call); err != nil {
			return "", fmt.Errorf("use_skill: invalid arguments: %w", err)
		}
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		if r == nil || r.Len() == 0 {
			return "No skills are installed. Add SKILL.md directories under .argent/skills or ~/.argentgo/skills.", nil
		}
		var b strings.Builder
		b.WriteString("Installed skills:\n")
		for _, skill := range r.All() {
			fmt.Fprintf(&b, "- %s — %s\n  %s\n", skill.Name, oneLine(skill.Description), skill.Path)
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
	return fmt.Sprintf("# skill: %s\n(source: %s)\n\n%s", skill.Name, skill.Path, skill.Body), nil
}
