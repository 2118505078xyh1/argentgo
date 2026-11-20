package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

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
	// DefaultBackgroundResultsCap is the default number of undrained results
	// the shared background queue retains.
	DefaultBackgroundResultsCap = 50
	// DefaultMaxBackgroundTasks caps how many background children may run at
	// once across one root run and all of its descendants.
	DefaultMaxBackgroundTasks = 8
	// BackgroundResultPrefix marks both the synthetic result text pushed by a
	// finished background task and the matching parent EventNotice.
	BackgroundResultPrefix = "task result: "
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
	// Background, when set, enables the `background` field on task calls. It is
	// the shared, bounded result queue for one root run and all of its
	// descendants; the parent loop drains it (see the wiring note on the
	// BackgroundResults type). A background call with no queue configured is
	// rejected rather than silently losing its result.
	Background *BackgroundResults
	// NewChild optionally overrides how a delegated child runner is built. Nil
	// means &Runner{Opts: opts}. It is primarily a test seam: injecting a fake
	// lets a test control child execution without a real model.
	NewChild func(opts Options) ChildRunner
	// SeedContext carries retention-flagged parent records into each child's
	// context, one formatted line per record. It is additive and default-off:
	// empty leaves the child system prompt exactly as before. The app computes
	// the lines with session.SelectForChildren(records) and formats them; the
	// agent package never imports session (see AGENTS.md layering rules).
	SeedContext []string
}

// TaskToolSpec describes the delegate tool to the model.
func TaskToolSpec() llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = TaskToolName
	spec.Function.Description = "Delegate a self-contained task to a subagent with its own context and a restricted tool set. " +
		"The subagent returns only its final answer, so give it everything it needs in the prompt. " +
		"Set background to true to start without waiting: the call returns an ack immediately and the result is injected into the conversation when it is ready."
	spec.Function.Parameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {"type": "string", "description": "Short label for the task, shown in the UI"},
    "prompt": {"type": "string", "description": "Complete, self-contained instructions for the subagent"},
    "task": {"type": "string", "description": "Alias for prompt, accepted for compatibility"},
    "persona": {"type": "string", "description": "Optional persona/role name defined in .argent/agents; the named persona's system prompt and tool allow-list seed the subagent"},
    "role": {"type": "string", "description": "Alias for persona, accepted for compatibility"},
    "verify": {"type": "string", "description": "Optional verification instruction. When set, the parent validates the subagent's result against it before accepting: it issues a short verification subagent that replies PASS or FAIL. On FAIL the original task is retried up to verify_max_retries times."},
    "verify_max_retries": {"type": "integer", "description": "How many times to retry the original task when verification fails (default 0, capped at 3). Ignored when verify is absent."},
    "background": {"type": "boolean", "description": "When true, start the task in the background and return immediately with an ack; the finished result is injected into the conversation when ready. Takes precedence over verify."}
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
	// emitMu serialises parent-level subagent events. Background children emit
	// from their own goroutines, so a lock keeps start/end/notice ordering sane.
	emitMu sync.Mutex
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
	// Background, when true, launches the child in a goroutine and returns an
	// immediate ack. The finished result is queued for injection into the
	// parent. Absent or false preserves the legacy synchronous behaviour.
	Background bool `json:"background,omitempty"`
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
	if args.Background {
		return t.runTaskBackground(ctx, raw, args)
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
	child := t.newChild(label, persona)
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
	// Carry the shared background state and the child-runner seam across the
	// persona layering so a persona-selected child keeps the parent's queue,
	// semaphore and injectable runner.
	if t.cfg.Background != nil {
		merged.Background = t.cfg.Background
	}
	if t.cfg.NewChild != nil {
		merged.NewChild = t.cfg.NewChild
	}
	if len(t.cfg.SeedContext) > 0 {
		merged.SeedContext = t.cfg.SeedContext
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
	if len(cfg.SeedContext) > 0 {
		system += "\n\nRetained context from the parent thread:\n- " + strings.Join(cfg.SeedContext, "\n- ")
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
		BeforeRequest:    t.parent.BeforeRequest,
		BeforeTool:       t.parent.BeforeTool,
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

// parentEmit sends a parent-level subagent event. Background children emit
// from their own goroutines, so the lock keeps those events ordered.
func (t *taskTools) parentEmit(event Event) {
	if t.parent.Emit == nil {
		return
	}
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
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

// ---------------------------------------------------------------------------
// Background (fire-and-forget) tasks
// ---------------------------------------------------------------------------

// ChildRunner is the slice of Runner that delegation needs. *Runner satisfies
// it; tests inject a fake through SubagentConfig.NewChild to control child
// execution without a real model.
type ChildRunner interface {
	Run(ctx context.Context, prompt string) (Result, error)
}

// newChild builds the child runner for one delegated task.
func (t *taskTools) newChild(label, persona string) ChildRunner {
	opts := t.childOptions(label, persona)
	if t.cfg.NewChild != nil {
		return t.cfg.NewChild(opts)
	}
	return &Runner{Opts: opts}
}

// BackgroundResults is the shared, bounded queue of finished background task
// results for one root run and all of its descendants. The wiring layer creates
// one instance and stores it on SubagentConfig.Background; because that field
// is a pointer copied by value through every SubagentConfig, the parent loop
// (via Options.Subagent) and every taskTools in the tree see the same instance.
// The type also owns the background semaphore and task-id counter so those
// limits are shared across the whole tree. All methods are concurrency-safe.
//
// LOOP WIRING (done outside this file, in loop.go): in runState.injectSteering,
// after draining s.steer, append the results addressed to this run's depth:
//
//	if s.opts.Subagent != nil && s.opts.Subagent.Background != nil {
//	    pending = append(pending, s.opts.Subagent.Background.DrainAt(s.depth)...)
//	}
//
// injectSteering already runs at every turn boundary, so a finished result
// becomes a synthetic user message at the next boundary without any other
// change. The wiring layer must also create one queue on the root options:
//
//	opts.Subagent.Background = agent.NewBackgroundResults(0, 0)
//
// Use Drain() (every depth) for diagnostics/tests; DrainAt(s.depth) prevents a
// child run from consuming results addressed to its parent. Results that are
// not drained before a run ends stay in the queue and are picked up by the next
// prompt on the same Runner.
type BackgroundResults struct {
	mu      sync.Mutex
	items   []bgResult
	cap     int
	dropped int
	seq     atomic.Uint64

	sem    chan struct{}
	maxRun int
}

// bgResult is one queued result together with the depth of the run that should
// receive it (its parent).
type bgResult struct {
	depth int
	text  string
}

// NewBackgroundResults builds a shared background queue. cap bounds undrained
// results (<=0 uses DefaultBackgroundResultsCap); maxConcurrent bounds
// simultaneous background child runners (<=0 uses DefaultMaxBackgroundTasks).
func NewBackgroundResults(cap, maxConcurrent int) *BackgroundResults {
	if cap <= 0 {
		cap = DefaultBackgroundResultsCap
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxBackgroundTasks
	}
	return &BackgroundResults{
		items:  make([]bgResult, 0, cap),
		cap:    cap,
		sem:    make(chan struct{}, maxConcurrent),
		maxRun: maxConcurrent,
	}
}

// Push enqueues text targeting the root run (depth 0). See PushAt.
func (b *BackgroundResults) Push(text string) bool { return b.PushAt(0, text) }

// PushAt enqueues a finished result for the run at depth. When the queue is
// full the oldest entry is dropped (the newest results are the most relevant to
// a still-running parent); the bool reports whether a drop occurred.
func (b *BackgroundResults) PushAt(depth int, text string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	dropped := false
	if len(b.items) >= b.cap {
		copy(b.items, b.items[1:])
		b.items = b.items[:len(b.items)-1]
		dropped = true
		b.dropped++
	}
	b.items = append(b.items, bgResult{depth: depth, text: text})
	return dropped
}

// Drain removes and returns every queued result in FIFO order.
func (b *BackgroundResults) Drain() []string { return b.drainAt(-1) }

// DrainAt removes and returns only the results targeting depth.
func (b *BackgroundResults) DrainAt(depth int) []string { return b.drainAt(depth) }

func (b *BackgroundResults) drainAt(depth int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil
	}
	out := make([]string, 0, len(b.items))
	kept := b.items[:0]
	for _, item := range b.items {
		if depth < 0 || item.depth == depth {
			out = append(out, item.text)
			continue
		}
		kept = append(kept, item)
	}
	b.items = kept
	return out
}

// Len reports how many results are currently queued.
func (b *BackgroundResults) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Dropped reports how many results were evicted because the queue was full.
func (b *BackgroundResults) Dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// tryAcquire reserves a background slot, reporting false when the cap is full.
func (b *BackgroundResults) tryAcquire() bool {
	select {
	case b.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b *BackgroundResults) release() { <-b.sem }

func (b *BackgroundResults) nextID() uint64 { return b.seq.Add(1) }

func (b *BackgroundResults) maxConcurrent() int { return b.maxRun }

// runTaskBackground starts a child runner in a goroutine and returns an ack
// immediately. When the child finishes its text is pushed to the shared queue
// (for the parent loop to inject) and emitted as a parent EventNotice (for UI
// visibility). Verification is not applied in background mode: the background
// field takes precedence over verify. MaxDepth is enforced by the caller
// (runTask) before this method is reached, and the child inherits
// Depth+1 through childOptions, so a background child cannot spawn unbounded
// background grandchildren.
func (t *taskTools) runTaskBackground(ctx context.Context, raw string, args taskArgs) (string, error) {
	bg := t.cfg.Background
	if bg == nil {
		return "", fmt.Errorf("task: background execution requires SubagentConfig.Background to be configured")
	}
	if !bg.tryAcquire() {
		return "", fmt.Errorf("task: background task limit reached (%d concurrent); retry later or use a synchronous task", bg.maxConcurrent())
	}

	task := strings.TrimSpace(args.Prompt)
	if task == "" {
		task = strings.TrimSpace(args.Task)
	}
	if task == "" {
		task = strings.TrimSpace(args.Description)
	}
	if task == "" {
		bg.release()
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
	if strings.TrimSpace(args.Verify) != "" {
		t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName, Text: "background task ignores verify: " + label})
	}

	id := bg.nextID()
	t.parentEmit(Event{Kind: EventSubagentStart, Text: label, ToolName: TaskToolName, Args: raw})

	depth := t.depth
	go func() {
		defer bg.release()
		child := t.newChild(label, persona)
		res, err := child.Run(ctx, task)
		if err != nil {
			t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Err: err.Error(), IsError: true})
			bg.PushAt(depth, fmt.Sprintf("%s[task %d] %s failed: %v", BackgroundResultPrefix, id, label, err))
			return
		}
		text := strings.TrimSpace(res.Text)
		if text == "" {
			text = "(subagent finished without an answer)"
		}
		t.parentEmit(Event{Kind: EventSubagentEnd, Text: label, ToolName: TaskToolName, Output: text})
		result := fmt.Sprintf("%s[task %d] %s: %s", BackgroundResultPrefix, id, label, text)
		dropped := bg.PushAt(depth, result)
		t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName, Text: result})
		if dropped {
			t.parentEmit(Event{Kind: EventNotice, ToolName: TaskToolName,
				Text: "task result queue full: dropped an older result while storing task " + fmt.Sprint(id)})
		}
	}()

	return fmt.Sprintf("task %d started in background; result will be injected when ready", id), nil
}

// ---------------------------------------------------------------------------
// Shared task board
// ---------------------------------------------------------------------------

// BoardMessage is one typed message on the in-process task board.
type BoardMessage struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Body string `json:"body"`
}

// Board message kinds and bounded-capacity limits.
const (
	BoardKindInfo   = "info"
	BoardKindAsk    = "ask"
	BoardKindResult = "result"

	BoardMaxBodyBytes  = 4096
	BoardMaxMessages   = 1000
	BoardMaxTotalBytes = 2 << 20 // 2 MiB of body bytes
)

var (
	ErrBoardSelfPost      = errors.New("task board: a task cannot post to itself")
	ErrBoardInvalidKind   = errors.New("task board: kind must be info, ask or result")
	ErrBoardMessageTooBig = errors.New("task board: message body exceeds 4096 bytes")
	ErrBoardFull          = errors.New("task board: capacity reached")
)

// TaskBoard is a small in-process, bounded, concurrency-safe message board for
// inter-agent coordination. It is deliberately dependency-free.
type TaskBoard struct {
	mu      sync.Mutex
	entries []BoardMessage
	seq     int
	total   int
}

// TaskBoardIO is the narrow interface future board_post / board_read tools
// should take as a parameter. It lives here so internal/tools can depend on an
// interface without importing agent, and no new file is needed yet.
type TaskBoardIO interface {
	Post(from, to, kind, body string) (int, error)
	Read(since int, to string) []BoardMessage
}

// Post adds a message and returns its monotonically increasing sequence number.
// It rejects self-posts, unknown kinds, oversized bodies and a full board.
func (b *TaskBoard) Post(from, to, kind, body string) (int, error) {
	if strings.TrimSpace(from) == strings.TrimSpace(to) {
		return 0, ErrBoardSelfPost
	}
	switch kind {
	case BoardKindInfo, BoardKindAsk, BoardKindResult:
	default:
		return 0, ErrBoardInvalidKind
	}
	if len(body) > BoardMaxBodyBytes {
		return 0, ErrBoardMessageTooBig
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) >= BoardMaxMessages || b.total+len(body) > BoardMaxTotalBytes {
		return 0, ErrBoardFull
	}
	b.seq++
	b.total += len(body)
	b.entries = append(b.entries, BoardMessage{Seq: b.seq, From: from, To: to, Kind: kind, Body: body})
	return b.seq, nil
}

// Read returns messages with Seq > since, ordered by sequence. An empty to
// returns every recipient; otherwise only messages addressed to to are returned.
func (b *TaskBoard) Read(since int, to string) []BoardMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []BoardMessage
	for _, m := range b.entries {
		if m.Seq <= since {
			continue
		}
		if to != "" && m.To != to {
			continue
		}
		out = append(out, m)
	}
	return out
}
