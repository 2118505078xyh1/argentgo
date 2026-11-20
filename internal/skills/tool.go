package skills

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolSpec describes a tool the caller can register with its own registry.
//
// It mirrors the wire shape every provider expects (`type: function`,
// `function: {name, description, parameters}`) but is deliberately a local type
// so this package does not depend on the agent or llm packages.
type ToolSpec struct {
	Type     string     `json:"type"`
	Function ToolSchema `json:"function"`
}

// ToolSchema is the function half of a ToolSpec.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// UseSkillSpec returns the tool the caller should advertise so the model can
// load a skill body by name.
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

// UseSkillSpec is the same spec bound to a registry, for callers that build it
// per session.
func (r *Registry) UseSkillSpec() ToolSpec { return UseSkillSpec() }

// HandleUseSkill executes the use_skill tool with raw JSON arguments.
//
// An empty or missing `name` lists the installed skills; a known name returns
// the full body with its source path; an unknown name returns an error whose
// message suggests close matches.
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
