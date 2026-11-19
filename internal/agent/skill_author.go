package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Skill-authoring tools are opt-in. The interfaces below are declared locally,
// with the exact signatures of their skills-package counterparts, so this
// package never imports internal/skills and the app layer can wire either
// implementation through a tiny adapter.

const (
	// SaveSkillToolName is the built-in tool that persists a reusable procedure.
	SaveSkillToolName = "save_skill"
	// ListSkillsToolName is the read-only tool that lists installed skills.
	ListSkillsToolName = "list_skills"
)

// SkillWriter is the seam for authoring a SKILL.md. Its method signature
// matches skills.Author exactly; no import of that package is needed.
type SkillWriter interface {
	Author(root, name, description, body string) (string, error)
}

// SkillEntry is the local view of one installed skill returned by a lister.
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SkillLister is the seam for the read-only list_skills tool. An adapter over
// skills.Registry can satisfy it in the app layer.
type SkillLister interface {
	ListSkills() []SkillEntry
}

// SaveSkillToolSpec describes the save_skill tool to the model.
func SaveSkillToolSpec() llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = SaveSkillToolName
	spec.Function.Description = "Save a reusable workflow as a SKILL.md so it can be discovered and loaded in a future session. " +
		"Use it after a complex task that is worth repeating; give a short kebab-case name, a one-line trigger description and the ordered steps."
	spec.Function.Parameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Lowercase kebab-case skill name, e.g. deploy-service"},
    "description": {"type": "string", "description": "One-line description saying when to use the skill"},
    "steps": {"type": "array", "items": {"type": "string"}, "description": "Ordered steps that make up the procedure"}
  },
  "required": ["name", "description", "steps"],
  "additionalProperties": false
}`)
	return spec
}

// ListSkillsToolSpec describes the read-only list_skills tool to the model.
func ListSkillsToolSpec() llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = ListSkillsToolName
	spec.Function.Description = "List the skills installed for this session with their one-line descriptions."
	spec.Function.Parameters = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)
	return spec
}

// skillTools wraps a ToolRunner with the opt-in skill tools. It advertises
// only the tools whose seam was configured and delegates every other call.
type skillTools struct {
	inner  ToolRunner
	writer SkillWriter
	lister SkillLister
	emit   func(Event)
	root   string
	depth  int
	specs  []llm.ToolSpec
}

// newSkillTools builds the wrapper, filtering any same-named tool already
// advertised by the inner runner so the built-in handler always wins.
func newSkillTools(inner ToolRunner, opts Options) *skillTools {
	innerSpecs := inner.Specs()
	specs := make([]llm.ToolSpec, 0, len(innerSpecs)+2)
	for _, spec := range innerSpecs {
		switch spec.Function.Name {
		case SaveSkillToolName:
			if opts.SkillAuthor != nil {
				continue
			}
		case ListSkillsToolName:
			if opts.SkillLister != nil {
				continue
			}
		}
		specs = append(specs, spec)
	}
	if opts.SkillAuthor != nil {
		specs = append(specs, SaveSkillToolSpec())
	}
	if opts.SkillLister != nil {
		specs = append(specs, ListSkillsToolSpec())
	}
	return &skillTools{
		inner:  inner,
		writer: opts.SkillAuthor,
		lister: opts.SkillLister,
		emit:   opts.Emit,
		root:   opts.Root,
		depth:  opts.Depth,
		specs:  specs,
	}
}

func (t *skillTools) Specs() []llm.ToolSpec { return t.specs }

func (t *skillTools) Run(ctx context.Context, name, args string) (string, error) {
	switch name {
	case SaveSkillToolName:
		if t.writer != nil {
			return t.saveSkill(args)
		}
	case ListSkillsToolName:
		if t.lister != nil {
			return t.listSkills(), nil
		}
	}
	return t.inner.Run(ctx, name, args)
}

// saveSkillArgs is the save_skill argument shape.
type saveSkillArgs struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Steps       []string `json:"steps"`
}

// saveSkill renders the steps as an ordered markdown procedure and persists
// it through the injected writer. On success it emits a notice so the operator
// can see that a file was created even before the model answers.
func (t *skillTools) saveSkill(raw string) (string, error) {
	var args saveSkillArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("save_skill: invalid arguments: %w", err)
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return "", fmt.Errorf("save_skill: name is required")
	}
	description := strings.TrimSpace(args.Description)
	if description == "" {
		return "", fmt.Errorf("save_skill: description is required")
	}

	var body strings.Builder
	n := 0
	for _, step := range args.Steps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		n++
		fmt.Fprintf(&body, "%d. %s\n", n, step)
	}
	if n == 0 {
		return "", fmt.Errorf("save_skill: steps must contain at least one item")
	}

	path, err := t.writer.Author(t.root, name, description, body.String())
	if err != nil {
		return "", fmt.Errorf("save_skill: %w", err)
	}
	t.notice(fmt.Sprintf("saved skill %q to %s", name, path))
	return fmt.Sprintf("Saved skill %q to %s.", name, path), nil
}

// listSkills formats the lister's entries as a compact tool result.
func (t *skillTools) listSkills() string {
	entries := t.lister.ListSkills()
	if len(entries) == 0 {
		return "No skills are installed."
	}
	var b strings.Builder
	b.WriteString("Installed skills:\n")
	for _, entry := range entries {
		if entry.Description == "" {
			fmt.Fprintf(&b, "- %s\n", entry.Name)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", entry.Name, entry.Description)
		}
	}
	return b.String()
}

func (t *skillTools) notice(text string) {
	if t.emit == nil {
		return
	}
	t.emit(Event{Kind: EventNotice, Text: text, Depth: t.depth})
}
