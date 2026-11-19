// Package agent runs the model/tool loop.
//
// The package is deliberately free of the session, config, tools and security
// packages: everything it needs from the rest of the program is declared here
// as a small interface (ToolRunner, Approver, Recorder) and supplied by the
// caller. That keeps the loop testable with fakes and lets the integrator wire
// the real registry without a dependency cycle.
package agent

import (
	"context"
	"fmt"
	"io"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	systemTemplate = `You are argent, a concise coding assistant working on the user's machine.
Working directory: %s
Use the available tools to inspect and change files. Read before writing, batch independent calls, and stop once the request is satisfied.`

	// DefaultMaxTurns is the turn budget when the caller sets none.
	DefaultMaxTurns = 100

	// defaultContextTokens is the estimated prompt size trimming aims for when
	// the caller sets no MaxContextTokens.
	defaultContextTokens = 32000

	// defaultMaxRepeats is how many identical (tool, args) calls may really run
	// before the repeat guard answers with an error instead.
	defaultMaxRepeats = 2
)

// defaultMutatingTools is the conservative approval set used when the caller
// gives neither Mutating nor MutatingTools. Unknown tools are treated as
// read-only: a subagent or plugin tool that mutates should be named explicitly.
var defaultMutatingTools = []string{
	"write", "edit", "multi_edit", "apply_patch", "patch",
	"bash", "shell", "exec", "run",
	"delete", "remove", "move", "copy", "mkdir",
}

// ToolRunner is the slice of the tool registry the loop drives.
type ToolRunner interface {
	// Specs is the tool list advertised to the model for the next request.
	// It is re-read every turn so tools unlocked mid-run appear immediately.
	Specs() []llm.ToolSpec
	// Run executes one tool call and returns its output text.
	Run(ctx context.Context, name, args string) (string, error)
}

// Approver gates mutating tools. A nil Approver means every call is allowed.
type Approver interface {
	Ask(ctx context.Context, name, args string) (bool, error)
}

// Recorder persists one transcript entry the loop wants to keep.
type Recorder interface {
	Append(kind string, payload any) error
}

// RecorderFunc adapts a plain function to Recorder.
type RecorderFunc func(kind string, payload any) error

// Append implements Recorder.
func (f RecorderFunc) Append(kind string, payload any) error {
	if f == nil {
		return nil
	}
	return f(kind, payload)
}

// Options configures one Runner.
type Options struct {
	// Model is the primary model when FallbackModels is empty. For API
	// compatibility the legacy field is honoured as-is; setting
	// FallbackModels takes precedence, with its first entry as the primary.
	Model string
	// FallbackModels is an ordered model list: entry 0 is the primary and the
	// rest are tried in sequence when the current model fails in a retryable
	// way (HTTP 429/5xx, rate limiting or a network error). Empty means no
	// fallback, preserving the pre-wave-4 single-model behaviour.
	FallbackModels []string

	MaxTurns    int // 0 falls back to Budget.MaxTurns, then DefaultMaxTurns
	MaxParallel int // <=1 (default) runs tool batches sequentially
	System      string
	Root        string

	Chatter llm.Chatter
	Tools   ToolRunner
	Approve Approver

	// Record accepts a Recorder, a func(kind string, payload any) error or a
	// func(kind string, payload any). The loose type keeps callers written
	// before Recorder existed compiling; new code should pass a Recorder.
	Record any

	// Emit receives every observable step of the run. A nil Emit is fine.
	Emit func(Event)

	// Checkpoint is called once before a batch containing mutating tool calls,
	// synchronously and before any tool in that batch runs. The session layer
	// records pre-turn bytes and digests here. A nil hook leaves the loop's
	// behaviour unchanged; EventCheckpointReady is still emitted.
	Checkpoint func(Checkpoint)
	// MutationPaths supplies the workspace-relative paths a mutating call may
	// change. It overrides the optional MutationReporter on Tools.
	MutationPaths func(name, args string) []string
	// OnCompactionEvidence receives failures and next-step lines that a
	// compactor must preserve. It runs once when the loop finishes.
	OnCompactionEvidence func(CompactionEvidence)

	// Steer is drained between turns; each value becomes a user message.
	Steer <-chan string
	// Budget limits the run. Limits are checked before each turn, before a
	// tool batch and after it.
	Budget Budget
	// Prices maps a model id to its per-million-token price. The "" entry, if
	// present, is the fallback for models without an entry.
	Prices map[string]Price

	// MaxContextTokens is the estimated prompt size trimming aims for
	// (default 32000). Budget.MaxPromptTokens, when smaller, wins.
	MaxContextTokens int
	// KeepTurns is how many trailing assistant turns are never trimmed
	// (default 6).
	KeepTurns int
	// KeepToolTokens is how much recent tool output is never trimmed
	// (default 2000 estimated tokens).
	KeepToolTokens int
	// MaxRepeats is how many identical calls really run before the repeat
	// guard answers with an error (default 2; 0 uses the default).
	MaxRepeats int

	// MutatingTools names the tools that need approval. Mutating, when set,
	// takes precedence. With neither, defaultMutatingTools is used.
	MutatingTools []string
	Mutating      func(name string) bool

	// Subagent, when set, advertises the built-in `task` tool and runs nested
	// loops for it.
	Subagent *SubagentConfig
	// Depth is the current subagent nesting depth (0 for a root run).
	Depth int

	// SkillAuthor, when set, advertises the built-in `save_skill` tool and
	// routes its calls through the injected writer. Nil (the default) leaves
	// existing runs completely unchanged.
	SkillAuthor SkillWriter
	// SkillLister, when set, advertises the read-only `list_skills` tool.
	SkillLister SkillLister

	// Out receives streamed answer text. Deprecated: use Emit with
	// EventTextDelta; kept so pre-wave-2 callers keep working.
	Out io.Writer
}

// Result is the outcome of one Run.
type Result struct {
	// Text is the last assistant text produced.
	Text string
	// Messages is the full conversation, including tool results.
	Messages []llm.Message
	// Usage is the token usage accumulated over the run.
	Usage llm.Usage
	// Turns is how many model requests completed.
	Turns int
	// Stopped is why the run ended: StopComplete, StopMaxTurns,
	// StopMaxPromptTokens, StopMaxTotalTokens, StopMaxCost, StopCancelled or
	// StopProviderError.
	Stopped string
}

// Runner drives the loop.
type Runner struct {
	// Client and Tools are the pre-wave-2 construction seam. Options.Chatter
	// and Options.Tools take precedence when set.
	Client llm.Chatter
	Tools  ToolRunner
	Opts   Options
}

// Run executes the prompt and returns the full result.
func (r *Runner) Run(ctx context.Context, prompt string) (Result, error) {
	return r.execute(ctx, prompt, r.Opts.Emit)
}

// RunText is the pre-wave-2 shape: just the final assistant text.
func (r *Runner) RunText(ctx context.Context, prompt string) (string, error) {
	res, err := r.Run(ctx, prompt)
	return res.Text, err
}

// Prompt satisfies the transport runner interface (server.Runner): it runs one
// prompt and forwards answer deltas to onText.
func (r *Runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	base := r.Opts.Emit
	var emit func(Event)
	if base != nil || onText != nil {
		emit = func(e Event) {
			if e.Kind == EventTextDelta && onText != nil {
				onText(e.Text)
			}
			if base != nil {
				base(e)
			}
		}
	}
	res, err := r.execute(ctx, text, emit)
	return res.Text, err
}

// resolved merges the legacy struct fields with Options.
func (r *Runner) resolved() (Options, error) {
	opts := r.Opts
	if opts.Chatter == nil {
		opts.Chatter = r.Client
	}
	if opts.Tools == nil {
		opts.Tools = r.Tools
	}
	if opts.Chatter == nil {
		return opts, fmt.Errorf("agent: no chatter configured")
	}
	if opts.Tools == nil {
		opts.Tools = nopTools{}
	}
	return opts, nil
}

// maxTurnsFor resolves the turn budget.
func maxTurnsFor(opts Options) int {
	if opts.Budget.MaxTurns > 0 {
		return opts.Budget.MaxTurns
	}
	if opts.MaxTurns > 0 {
		return opts.MaxTurns
	}
	return DefaultMaxTurns
}

// repeatsFor resolves the repeat-guard allowance.
func repeatsFor(opts Options) int {
	if opts.MaxRepeats > 0 {
		return opts.MaxRepeats
	}
	return defaultMaxRepeats
}

// normaliseRecorder accepts every shape Options.Record historically took.
func normaliseRecorder(value any) Recorder {
	switch r := value.(type) {
	case nil:
		return nil
	case Recorder:
		return r
	case func(kind string, payload any) error:
		return RecorderFunc(r)
	case func(kind string, payload any):
		return RecorderFunc(func(kind string, payload any) error {
			r(kind, payload)
			return nil
		})
	default:
		return nil
	}
}

// nopTools is the tool runner used when the caller supplies none.
type nopTools struct{}

func (nopTools) Specs() []llm.ToolSpec { return nil }

func (nopTools) Run(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("agent: no tools configured")
}
