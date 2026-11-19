package app

import (
	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// skillWriter adapts the skills package to the local seam in internal/agent,
// keeping the loop provider-agnostic and the app layer the only wiring point.
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
