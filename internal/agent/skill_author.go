package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/llm"
)

// 技能创作工具是选择性启用的。下面的接口在本地声明，签名与 skills 包中对应类型完全一致，
// 因此本包从不导入 internal/skills，app 层可以通过一个很小的适配器接入任一实现。

const (
	// SaveSkillToolName 是持久化可复用流程的内置工具。
	SaveSkillToolName = "save_skill"
	// ListSkillsToolName 是列出已安装技能的只读工具。
	ListSkillsToolName = "list_skills"
)

// SkillWriter 是创作 SKILL.md 的接口接缝。它的方法签名与 skills.Author 完全一致，
// 无需导入那个包。
type SkillWriter interface {
	Author(root, name, description, body string) (string, error)
}

// SkillEntry 是列表器返回的单个已安装技能的本地视图。
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SkillLister 是只读 list_skills 工具的接口接缝。app 层中用 skills.Registry 包一层适配器
// 即可满足它。
type SkillLister interface {
	ListSkills() []SkillEntry
}

// SaveSkillToolSpec 向模型描述 save_skill 工具。
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

// ListSkillsToolSpec 向模型描述只读的 list_skills 工具。
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

// skillTools 用选择性启用的技能工具包装 ToolRunner。它只公布已配置接缝的工具，
// 其余所有调用都委托给内部运行器。
type skillTools struct {
	inner  ToolRunner
	writer SkillWriter
	lister SkillLister
	emit   func(Event)
	root   string
	depth  int
	specs  []llm.ToolSpec
}

// newSkillTools 构建包装器，过滤掉内部运行器已公布的同名工具，
// 使内置处理器始终优先。
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

// saveSkillArgs 是 save_skill 的参数结构。
type saveSkillArgs struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Steps       []string `json:"steps"`
}

// saveSkill 把步骤渲染为有序的 markdown 流程，并通过注入的 writer 持久化。
// 成功时它会发出一条通知，使操作人员即使在模型作答之前也能看到文件已创建。
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

// listSkills 把列表器的条目格式化为紧凑的工具结果。
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
