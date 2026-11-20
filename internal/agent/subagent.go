package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
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
	// MaxVerifyRetries is the hard ceiling for a task call's verify_max_retries.
	// A model cannot request an unbounded verification loop: anything above this
	// is clamped, so every verify-enabled task does at most 1+MaxVerifyRetries
	// child runs (plus one verification child per attempt).
	MaxVerifyRetries = 3
)

const subagentTemplate = `You are a subagent working for another agent. Task: %s
Do only the work the task asks for, using the tools you have. Do not ask questions: make the most reasonable assumption and report the result. Finish with a concise final answer; the parent agent receives only that text.`

// SubagentConfig enables the built-in `task` tool.
type SubagentConfig struct {
	// Model, when set, is the child loop's model (e.g. --secondary-model).
	// The parent's model is never changed. Empty inherits the parent's model.
	Model string
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
	// ResolvePersona maps a persona/role name requested by a task call to the
	// child config that persona defines. It is optional and injected by the
	// wiring layer (internal/app) so this package never imports it. When nil,
	// or when the name is unknown, delegation falls back to the generic config
	// above. A resolver returning true starts from the persona's config; the
	// explicitly-set fields of this config are then layered on top.
	ResolvePersona func(string) (SubagentConfig, bool)
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
    "task": {"type": "string", "description": "Alias for prompt, accepted for compatibility"},
    "persona": {"type": "string", "description": "Optional persona/role name defined in .argent/agents; the named persona's system prompt and tool allow-list seed the subagent"},
    "role": {"type": "string", "description": "Alias for persona, accepted for compatibility"},
    "verify": {"type": "string", "description": "Optional verification instruction. When set, the parent validates the subagent's result against it before accepting: it issues a short verification subagent that replies PASS or FAIL. On FAIL the original task is retried up to verify_max_retries times."},
    "verify_max_retries": {"type": "integer", "description": "How many times to retry the original task when verification fails (default 0, capped at 3). Ignored when verify is absent."}
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
	// Skill tools are opt-in and wrap the caller's runner before the delegate
	// tool, so a subagent inherits the same advertised set through taskTools.
	if opts.SkillAuthor != nil || opts.SkillLister != nil {
		inner = newSkillTools(inner, opts)
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
	// Persona optionally selects a named subagent definition. Role is an alias.
	Persona string `json:"persona"`
	Role    string `json:"role"`
	// Verify, when set, makes the parent validate the child's result before
	// accepting it (see runTaskVerified). Empty preserves the legacy behavior.
	Verify string `json:"verify,omitempty"`
	// VerifyMaxRetries bounds how many times a FAIL verdict retries the
	// original task. Negative values clamp to 0, values above MaxVerifyRetries
	// clamp down. Ignored when Verify is empty.
	VerifyMaxRetries int `json:"verify_max_retries,omitempty"`
}

// runTask is the task tool's entry point. It parses the arguments and either
// runs the child once (the original, backward-compatible path) or, when a
// verify instruction is present, drives the bounded verify/retry loop.
//
// Verify design: PARENT-RECHECK. A single child's self-check cannot express a
// FAIL verdict that the parent can act on, so verification is a separate,
// short task call that replies PASS or FAIL. The follow-up is itself run
// through runTask, so it inherits the depth guard; it carries no verify field
// of its own, so verification cannot recurse. Retries are capped by
// MaxVerifyRetries. This keeps the change inside subagent.go.
func (t *taskTools) runTask(ctx context.Context, raw string) (string, error) {
	if t.depth >= t.cfg.MaxDepth {
		return "", fmt.Errorf("task depth limit reached: subagents may nest at most %d level(s)", t.cfg.MaxDepth)
	}
	var args taskArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("task: invalid arguments: %w", err)
	}
	if verify := strings.TrimSpace(args.Verify); verify != "" {
		return t.runTaskVerified(ctx, raw, args, verify)
	}
	return t.runTaskOnce(ctx, raw, args)
}

// runTaskOnce runs one child loop and returns its final answer as the tool
// result. This is the original delegation path, unchanged when verify is
// absent.
func (t *taskTools) runTaskOnce(ctx context.Context, raw string, args taskArgs) (string, error) {
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
	persona := strings.TrimSpace(args.Persona)
	if persona == "" {
		persona = strings.TrimSpace(args.Role)
	}

	t.parentEmit(Event{Kind: EventSubagentStart, Text: label, ToolName: TaskToolName, Args: raw})
	child := &Runner{Opts: t.childOptions(label, persona)}
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

// runTaskVerified runs the original task, then asks a short verification
// child to judge the result against the verify instruction. A PASS returns the
// result; a FAIL retries the original task up to verify_max_retries (clamped
// to [0, MaxVerifyRetries]). Both the attempt count and the verification
// recursion (one non-verifying follow-up per attempt) are bounded, so no
// configuration can produce an unbounded loop.
func (t *taskTools) runTaskVerified(ctx context.Context, raw string, args taskArgs, verify string) (string, error) {
	label := strings.TrimSpace(args.Description)
	if label == "" {
		label = firstLine(strings.TrimSpace(args.Prompt))
	}
	retries := args.VerifyMaxRetries
	if retries < 0 {
		retries = 0
	}
	if retries > MaxVerifyRetries {
		retries = MaxVerifyRetries
	}

	for attempt := 0; ; attempt++ {
		text, err := t.runTaskOnce(ctx, raw, args)
		if err != nil {
			return "", err
		}

		// The follow-up reuses runTask so it inherits the depth guard. Its JSON
		// deliberately omits verify, so verification never recurses.
		verdict, err := t.runTask(ctx, verifyArgsJSON(label, verify, text))
		if err != nil {
			// Without a verdict we cannot accept the result as verified.
			t.verifyNotice(fmt.Sprintf("subagent verification error for %q: %v", label, err))
			return "", fmt.Errorf("subagent verification failed for %q: %w", label, err)
		}
		if !verificationFailed(verdict) {
			t.verifyNotice(fmt.Sprintf("subagent verification PASS: %s", label))
			return text, nil
		}
		t.verifyNotice(fmt.Sprintf("subagent verification FAIL (attempt %d/%d): %s - %s", attempt+1, retries+1, label, firstLine(verdict)))
		if attempt >= retries {
			// Retries exhausted: return the last result rather than losing the
			// work, with the FAIL outcome already recorded above.
			return text, nil
		}
	}
}

// verifyNotice emits an auditable verification outcome as a parent-level
// notice event (EventNotice), reusing the existing event vocabulary.
func (t *taskTools) verifyNotice(text string) {
	t.parentEmit(Event{Kind: EventNotice, Text: text, ToolName: TaskToolName})
}

// verifyArgsJSON builds the follow-up task call that judges a result. The
// verify instruction and the result are embedded in the prompt; no verify
// field is set, so the follow-up is a plain single child run.
func verifyArgsJSON(label, verify, result string) string {
	prompt := fmt.Sprintf("Verify the following result against these criteria:\n%s\n\nResult to verify:\n%s\n\nReply with PASS if the result satisfies the criteria, otherwise reply with FAIL followed by concise reasons.",
		verify, result)
	b, _ := json.Marshal(taskArgs{
		Description: "verify: " + label,
		Prompt:      prompt,
	})
	return string(b)
}

// verificationFailed reports whether a verification verdict is a FAIL. The
// follow-up is instructed to lead with PASS or FAIL, so the first token wins;
// a fallback handles verbose replies that only mention one of the two words.
func verificationFailed(verdict string) bool {
	v := strings.TrimSpace(verdict)
	if v == "" {
		return true
	}
	first := strings.ToUpper(strings.Fields(v)[0])
	if strings.HasPrefix(first, "FAIL") {
		return true
	}
	if strings.HasPrefix(first, "PASS") {
		return false
	}
	upper := strings.ToUpper(v)
	return strings.Contains(upper, "FAIL") && !strings.Contains(upper, "PASS")
}

// configForPersona seeds the child config from a named persona, then layers the
// parent's explicitly-set fields on top. A missing resolver, an empty name or
// an unknown name leaves the generic parent config untouched, which keeps
// delegation backward compatible.
func (t *taskTools) configForPersona(name string) SubagentConfig {
	cfg := *t.cfg
	name = strings.TrimSpace(name)
	if name == "" || t.cfg.ResolvePersona == nil {
		return cfg
	}
	persona, ok := t.cfg.ResolvePersona(name)
	if !ok {
		return cfg
	}

	merged := persona
	if v := strings.TrimSpace(t.cfg.Model); v != "" {
		merged.Model = v
	}
	if len(t.cfg.AllowedTools) > 0 {
		merged.AllowedTools = t.cfg.AllowedTools
	}
	if t.cfg.MaxDepth > 0 {
		merged.MaxDepth = t.cfg.MaxDepth
	}
	if t.cfg.MaxTurns > 0 {
		merged.MaxTurns = t.cfg.MaxTurns
	}
	if t.cfg.Budget != (Budget{}) {
		merged.Budget = t.cfg.Budget
	}
	if strings.TrimSpace(t.cfg.System) != "" {
		merged.System = t.cfg.System
	}
	if t.cfg.OnEvent != nil {
		merged.OnEvent = t.cfg.OnEvent
	}
	// Keep the resolver so nested tasks can choose their own personas too.
	merged.ResolvePersona = t.cfg.ResolvePersona
	return merged
}

// childOptions builds the nested run's configuration: restricted tools, the
// child system prompt, a budget slice, and the event bridge.
func (t *taskTools) childOptions(label, persona string) Options {
	cfg := t.configForPersona(persona)
	tools := t.inner
	if len(cfg.AllowedTools) > 0 {
		allowed := make(map[string]bool, len(cfg.AllowedTools))
		for _, name := range cfg.AllowedTools {
			allowed[name] = true
		}
		tools = restrictedTools{inner: t.inner, allowed: allowed}
	}

	system := strings.TrimSpace(cfg.System)
	if system == "" {
		system = fmt.Sprintf(subagentTemplate, label)
	}

	budget := cfg.Budget
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
	if cfg.MaxTurns > 0 {
		budget.MaxTurns = cfg.MaxTurns
	}

	emit := func(event Event) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(event)
			return
		}
		if t.parent.Emit != nil {
			t.parent.Emit(event)
		}
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = t.parent.Model
	}
	return Options{
		Model:            model,
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
		Subagent:         &cfg,
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
