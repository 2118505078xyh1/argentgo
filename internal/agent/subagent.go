package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

const (
	// TaskToolName is the built-in delegate tool.
	TaskToolName = "task"
	// DefaultMaxDepth is how deep subagents may nest when the caller sets no
	// limit. Root is depth 0, so the default allows two child levels.
	DefaultMaxDepth = 2
	// DefaultSubagentTurns caps a child run's turns when the parent has no
	// smaller budget.
	DefaultSubagentTurns = 16
)

const subagentTemplate = `You are a subagent working for another agent. Task: %s
Do only the work the task asks for, using the tools you have. Do not ask questions: make the most reasonable assumption and report the result. Finish with a concise final answer; the parent agent receives only that text.`

// SubagentConfig enables the built-in `task` tool.
type SubagentConfig struct {
	// AllowedTools restricts the child's tool set. Empty inherits the parent's
	// tools (the child still cannot recurse past MaxDepth).
	AllowedTools []string
	// MaxDepth is the deepest child level that may spawn another subagent
	// (default 2).
	MaxDepth int
	// MaxTurns overrides the child's turn cap.
	MaxTurns int
	// Budget, when set, is the child's whole budget; otherwise the parent's
	// budget is halved.
	Budget Budget
	// System replaces the child's system prompt.
	System string
	// OnEvent receives the child's events. When nil they go to the parent's
	// Emit, with Event.Depth set.
	OnEvent func(Event)
}

// TaskToolSpec describes the delegate tool to the model.
func TaskToolSpec() llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = TaskToolName
	spec.Function.Description = "Delegate a self-contained task to a subagent with its own context and a restricted tool set. " +
		"The subagent returns only its final answer, so give it everything it needs in the prompt."
	spec.Function.Parameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {"type": "string", "description": "Short label for the task, shown in the UI"},
    "prompt": {"type": "string", "description": "Complete, self-contained instructions for the subagent"},
    "task": {"type": "string", "description": "Alias for prompt, accepted for compatibility"}
  },
  "required": ["prompt"]
}`)
	return spec
}

// newToolset wraps the caller's tools with the delegate tool when subagents are
// enabled. At the depth limit the spec is withheld, so a child is not tempted
// to recurse.
func newToolset(inner ToolRunner, opts Options) ToolRunner {
	if inner == nil {
		inner = nopTools{}
	}
	if opts.Subagent == nil {
		return inner
	}
	cfg := *opts.Subagent
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = DefaultMaxDepth
	}
	specs := make([]llm.ToolSpec, 0, len(inner.Specs())+1)
	for _, spec := range inner.Specs() {
		if spec.Function.Name == TaskToolName {
			continue
		}
		specs = append(specs, spec)
	}
	if opts.Depth < cfg.MaxDepth {
		specs = append(specs, TaskToolSpec())
	}
	return &taskTools{inner: inner, specs: specs, parent: opts, depth: opts.Depth, cfg: &cfg}
}

// taskTools advertises and runs the delegate tool over a base tool runner.
type taskTools struct {
	inner  ToolRunner
	specs  []llm.ToolSpec
	parent Options
	depth  int
	cfg    *SubagentConfig
}

func (t *taskTools) Specs() []llm.ToolSpec { return t.specs }

func (t *taskTools) Run(ctx context.Context, name, args string) (string, error) {
	if name != TaskToolName {
		return t.inner.Run(ctx, name, args)
	}
	return t.runTask(ctx, args)
}

// taskArgs is the delegate tool's argument shape.
type taskArgs struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Task        string `json:"task"`
}

// runTask runs one child loop and returns its final answer as the tool result.
func (t *taskTools) runTask(ctx context.Context, raw string) (string, error) {
	if t.depth >= t.cfg.MaxDepth {
		return "", fmt.Errorf("task depth limit reached: subagents may nest at most %d level(s)", t.cfg.MaxDepth)
	}
	var args taskArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("task: invalid arguments: %w", err)
	}
	task := strings.TrimSpace(args.Prompt)
	if task == "" {
		task = strings.TrimSpace(args.Task)
	}
	if task == "" {
		task = strings.TrimSpace(args.Description)
	}
	if task == "" {
		return "", fmt.Errorf("task: prompt is required")
	}
	label := strings.TrimSpace(args.Description)
	if label == "" {
		label = firstLine(task)
	}

	t.parentEmit(Event{Kind: EventSubagentStart, Text: label, ToolName: TaskToolName, Args: raw})
	child := &Runner{Opts: t.childOptions(label)}
	res, err := child.Run(ctx, task)
	if err != nil {
		t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Err: err.Error(), IsError: true})
		return "", fmt.Errorf("subagent failed: %w", err)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "(subagent finished without an answer)"
	}
	t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Output: text})
	return text, nil
}

// childOptions builds the nested run's configuration: restricted tools, the
// child system prompt, a budget slice, and the event bridge.
func (t *taskTools) childOptions(label string) Options {
	tools := t.inner
	if len(t.cfg.AllowedTools) > 0 {
		allowed := make(map[string]bool, len(t.cfg.AllowedTools))
		for _, name := range t.cfg.AllowedTools {
			allowed[name] = true
		}
		tools = restrictedTools{inner: t.inner, allowed: allowed}
	}

	system := strings.TrimSpace(t.cfg.System)
	if system == "" {
		system = fmt.Sprintf(subagentTemplate, label)
	}

	budget := t.cfg.Budget
	if budget == (Budget{}) {
		budget = t.parent.Budget.sliceForChild()
	}
	if budget.MaxTurns <= 0 {
		turns := DefaultSubagentTurns
		if parentTurns := maxTurnsFor(t.parent); parentTurns > 1 && parentTurns/2 < turns {
			turns = max(1, parentTurns/2)
		}
		budget.MaxTurns = turns
	}
	if t.cfg.MaxTurns > 0 {
		budget.MaxTurns = t.cfg.MaxTurns
	}

	emit := func(event Event) {
		if t.cfg.OnEvent != nil {
			t.cfg.OnEvent(event)
			return
		}
		if t.parent.Emit != nil {
			t.parent.Emit(event)
		}
	}

	return Options{
		Model:            t.parent.Model,
		MaxTurns:         budget.MaxTurns,
		MaxParallel:      t.parent.MaxParallel,
		System:           system,
		Root:             t.parent.Root,
		Chatter:          t.parent.Chatter,
		Tools:            tools,
		Approve:          t.parent.Approve,
		Record:           t.parent.Record,
		Emit:             emit,
		Budget:           budget,
		Prices:           t.parent.Prices,
		MaxContextTokens: t.parent.MaxContextTokens,
		KeepTurns:        t.parent.KeepTurns,
		KeepToolTokens:   t.parent.KeepToolTokens,
		MaxRepeats:       t.parent.MaxRepeats,
		MutatingTools:    t.parent.MutatingTools,
		Mutating:         t.parent.Mutating,
		Subagent:         t.cfg,
		Depth:            t.depth + 1,
	}
}

// parentEmit sends a parent-level subagent event.
func (t *taskTools) parentEmit(event Event) {
	if t.parent.Emit == nil {
		return
	}
	event.Depth = t.depth
	t.parent.Emit(event)
}

// restrictedTools is the child's filtered view of the parent's tools.
type restrictedTools struct {
	inner   ToolRunner
	allowed map[string]bool
}

func (r restrictedTools) Specs() []llm.ToolSpec {
	specs := r.inner.Specs()
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if r.allowed[spec.Function.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func (r restrictedTools) Run(ctx context.Context, name, args string) (string, error) {
	if !r.allowed[name] {
		return "", fmt.Errorf("tool %q is not available to subagents", name)
	}
	return r.inner.Run(ctx, name, args)
}

// firstLine shortens a task prompt for a label.
func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:77]) + "..."
	}
	return text
}
