package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// personaDir is where a workspace keeps named subagent definitions.
const personaDir = ".argent/agents"

// personaCache loads persona definitions once per workspace root. Execute runs
// many times per process (TUI prompts, schema retries, tests), so rescanning
// the directory on every prompt would be wasted I/O.
var personaCache sync.Map // map[string]personaSnapshot

type personaSnapshot struct {
	personas map[string]agent.Persona
	err      error
}

// loadPersonasForRoot returns the personas configured for a workspace root.
// Results, including the first parse error, are cached: a broken agent file is
// reported once and never blocks an otherwise valid run.
func loadPersonasForRoot(root string) (map[string]agent.Persona, error) {
	if cached, ok := personaCache.Load(root); ok {
		snap := cached.(personaSnapshot)
		return snap.personas, snap.err
	}
	personas, err := agent.LoadPersonas(filepath.Join(root, filepath.FromSlash(personaDir)))
	personaCache.Store(root, personaSnapshot{personas: personas, err: err})
	return personas, err
}

// PersonaSubagentConfig resolves a persona name requested by a delegated task
// into the child configuration the `task` tool should use. It returns false for
// an empty or unknown name, leaving the caller's generic defaults untouched.
// It is injected into agent.SubagentConfig.ResolvePersona by configureSubagents,
// keeping internal/agent free of any dependency on internal/app.
func PersonaSubagentConfig(root, name string) (agent.SubagentConfig, bool) {
	if strings.TrimSpace(name) == "" {
		return agent.SubagentConfig{}, false
	}
	personas, _ := loadPersonasForRoot(root)
	persona, ok := personas[name]
	if !ok {
		return agent.SubagentConfig{}, false
	}
	return persona.SubagentConfig(), true
}

// configureSubagents applies the opt-in delegation setup to a root run's
// options. The opt-in signal is deliberately minimal: a workspace that defines
// at least one persona under .argent/agents is asking for delegation. No config
// key, flag or CLI change is required, and a workspace without personas keeps
// byte-identical behaviour (no `task` tool and an unchanged system prompt).
//
// When opted in, the orchestrator strategy is appended to the caller's system
// prompt (never replacing it), and Subagent is set with an empty model - which
// the agent loop interprets as "inherit the parent model" - and the default
// depth limit of 2. Persona names requested by individual task calls are
// resolved back here through the injected ResolvePersona callback, so the agent
// package never needs to import this one.
func configureSubagents(opts *agent.Options, res Resolved) {
	if opts == nil {
		return
	}
	personas, err := loadPersonasForRoot(res.Root)
	if err != nil || len(personas) == 0 {
		return
	}
	opts.System = res.System + "\n\n" + agent.OrchestratorPrompt()
	opts.Subagent = &agent.SubagentConfig{
		MaxDepth: agent.DefaultMaxDepth,
		ResolvePersona: func(name string) (agent.SubagentConfig, bool) {
			return PersonaSubagentConfig(res.Root, name)
		},
		// One process-wide queue so finished background children survive across
		// runs and are injected into whichever run drains them next.
		Background: sharedBackgroundResults(),
	}
}

var (
	backgroundOnce    sync.Once
	backgroundResults *agent.BackgroundResults
)

func sharedBackgroundResults() *agent.BackgroundResults {
	backgroundOnce.Do(func() {
		backgroundResults = agent.NewBackgroundResults(0, 0)
	})
	return backgroundResults
}

// Execute runs one prompt through a freshly wired tool registry, policy and
// agent loop. The registry is per-run so the security chain can carry the
// session id without mutable global state.
func Execute(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, prompt string, onText func(string)) (agent.Result, error) {
	// Loading personas is best-effort: a malformed agent definition must not
	// fail the run. configureSubagents reuses the cached result to decide
	// whether this run opts into delegation and to resolve per-task roles.
	_, _ = loadPersonasForRoot(res.Root)
	if injection, ok := reviewPrompt(res.Root, prompt); ok {
		prompt = injection + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	if res.MaxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(res.MaxSeconds)*time.Second)
		defer cancel()
	}

	bridge := newHookBridge(res, sess.ID(), stdio.Err)
	bridge.start(ctx, prompt)
	defer bridge.end()

	approver := res.Approve
	if approver == nil {
		approver = approverFor(stdio, res)
	}
	audit := security.NewAuditLog(filepath.Join(res.Storage, "audit"))
	policy := policyFor(res)
	chain := security.NewChain(policy, approver, audit, sess.ID())

	toolset := newToolset(ctx, stdio, res, sess, chain)
	defer toolset.Close()
	var runner agent.ToolRunner = toolset
	if bridge != nil {
		runner = &hookedTools{inner: toolset, bridge: bridge}
	}

	tracker := newCheckpointTracker(res.Root, sess)
	var evidence agent.CompactionEvidence
	compactor := newSessionCompactor(res, sess)
	// Durability barriers: flush the JSONL session before each expensive step.
	// Execute may run more than once per process (schema retries, tests), so
	// replace rather than accumulate hooks bound to an earlier session.
	agent.ResetBeforeHooks()
	agent.RegisterBeforeRequest(func() error { return sess.Sync() })
	agent.RegisterBeforeTool(func() error { return sess.Sync() })
	// Hooks are process-global; clear them on exit so a later run on another
	// path (e.g. the eval suite's own loop) cannot sync a stale session.
	defer agent.ResetBeforeHooks()
	loop := &agent.Runner{Opts: agent.Options{
		Model:       res.Model,
		MaxTurns:    res.MaxTurns,
		MaxParallel: res.MaxParallel,
		Root:        res.Root,
		System:      res.System,
		Chatter:     res.Chatter,
		Tools:       runner,
		// Never advertise tools the sandbox/deny rules forbid. Run() still
		// guards every call; this only keeps the model from wasting turns.
		FilterSpecs: func(specs []llm.ToolSpec) []llm.ToolSpec {
			if len(toolset.base.regs) == 0 {
				return specs
			}
			return toolset.base.regs[0].FilterSpecs(policy, res.Sandbox, specs)
		},
		Approve: approver,
		Record:  agent.RecorderFunc(sess.Append),
		Budget: agent.Budget{
			MaxTurns:       res.MaxTurns,
			MaxTotalTokens: res.MaxTokens,
			MaxCostUSD:     res.MaxCostUSD,
		},
		Prices:      res.Prices,
		SkillAuthor: skillWriter{},
		SkillLister: skillLister{toolset.skills},
		Checkpoint: func(cp agent.Checkpoint) {
			tracker.capture(cp, prompt)
		},
		MutationPaths: toolset.base.mutationPaths,
		// Mid-turn compaction is injected from the app layer so agent never
		// imports session. A disabled/negative threshold yields 0 and no-ops.
		Compactor:        compactor,
		CompactThreshold: compactor.CompactThreshold(),
		OnCompactionEvidence: func(e agent.CompactionEvidence) {
			evidence = e
		},
		Emit: func(event agent.Event) {
			if event.Kind == agent.EventTextDelta && event.Depth == 0 && onText != nil {
				onText(event.Text)
			}
		},
	}}
	configureSubagents(&loop.Opts, res)
	// Per-model edit-format guidance is opt-in so byte-exact system-prompt
	// assertions and callers that pin their own prompt keep unchanged bytes by
	// default. Set ARGENT_EDIT_GUIDE=1 to append it. Appending here (after
	// configureSubagents) keeps the orchestrator append order intact.
	if env := stdio.Env; env != nil && env("ARGENT_EDIT_GUIDE") == "1" {
		loop.Opts.System = agent.AppendEditFormat(loop.Opts.System, res.Model)
	}
	system := loop.Opts.System

	result, err := loop.Run(ctx, prompt)
	if err == nil && strings.TrimSpace(res.OutputSchema) != "" {
		if invalid := validateOutputText(res.OutputSchema, result.Text); invalid != nil {
			// One corrective turn gives the model the exact failure instead of
			// failing the whole run on a formatting slip.
			loop.Opts.System = system + correctiveInstruction(res.OutputSchema, invalid)
			if retried, retryErr := loop.Run(ctx, prompt); retryErr == nil {
				result = retried
				if invalid = validateOutputText(res.OutputSchema, result.Text); invalid != nil {
					err = fmt.Errorf("output schema validation failed after one retry: %w", invalid)
				}
			} else {
				err = retryErr
			}
		}
	}
	bridge.stop(ctx, prompt, result.Text)
	tracker.finish(err != nil || ctx.Err() != nil)
	recordAttempt(sess, ctx, err)
	maybeAutoCompact(res, sess, evidence)
	_ = sess.Append("turn", map[string]any{
		"turns":   result.Turns,
		"stopped": result.Stopped,
		"usage":   result.Usage,
	})
	return result, err
}

func firstLine(text string, limit int) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

// autoApprover grants every request; it backs --yolo and approval=auto.
type autoApprover struct{}

func (autoApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func approverFor(stdio cli.IO, res Resolved) security.Approver {
	switch res.Approval {
	case "never":
		return security.NeverApprover{}
	case "auto", "yolo":
		return autoApprover{}
	default:
		return security.NewCachedApprover(security.NewPromptApprover(stdio.In, stdio.Err))
	}
}
