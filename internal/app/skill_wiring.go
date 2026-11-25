package app

import (
	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/skills"
)

// skillWriter 把 skills 包适配为 internal/agent 中的本地接缝，
// 使循环保持与提供方无关，且 app 层是唯一的接线点。
type skillWriter struct{}

func (skillWriter) Author(root, name, description, body string) (string, error) {
	return skills.Author(root, name, description, body)
}

type skillLister struct{ registry *skills.Registry }

func (l skillLister) ListSkills() []agent.SkillEntry {
	if l.registry == nil {
		return nil
	}
	all := l.registry.All()
	out := make([]agent.SkillEntry, 0, len(all))
	for _, skill := range all {
		out = append(out, agent.SkillEntry{Name: skill.Name, Description: skill.Description})
	}
	return out
}
