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
//
// TODO(persona-role): finish wiring this into the delegation path. The task
// argument struct (taskArgs), its advertised schema (TaskToolSpec) and
// (*taskTools).childOptions all live in internal/agent/subagent.go, which is
// outside this file's ownership. To select a persona per task, that file needs
// an optional `persona` (or `role`) field on taskArgs/TaskToolSpec and must
// start childOptions from Persona.SubagentConfig() when the name resolves.
// Until then Execute deliberately preserves existing behaviour: no role is
// requested through the current tool schema, so no task tool is advertised and
// no persona overrides the generic child prompt.
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

// Execute runs one prompt through a freshly wired tool registry, policy and
// agent loop. The registry is per-run so the security chain can carry the
// session id without mutable global state.
func Execute(ctx context.Context, stdio cli.IO, res Resolved, sess *session.Session, prompt string, onText func(string)) (agent.Result, error) {
	// Warm the persona cache once per workspace root. Loading is best-effort:
	// a malformed agent definition must not fail the run. Per-task persona
	// selection is resolved by PersonaSubagentConfig (see its TODO above).
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

	result, err := loop.Run(ctx, prompt)
	if err == nil && strings.TrimSpace(res.OutputSchema) != "" {
		if invalid := validateOutputText(res.OutputSchema, result.Text); invalid != nil {
			// One corrective turn gives the model the exact failure instead of
			// failing the whole run on a formatting slip.
			loop.Opts.System = res.System + correctiveInstruction(res.OutputSchema, invalid)
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
