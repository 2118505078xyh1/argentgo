package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/memory"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/skills"
	"github.com/2118505078xyh1/argentgo/internal/tools"
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

// sharedDefs holds the process-wide DefinitionCache. The discovery call sites
// (buildSystemWithTrust, contextPrompt and newToolset) are package-level
// functions with no App/Factory receiver whose lifetime they could share, so
// the cache is a lazily-initialised package singleton rather than a struct
// field. DefinitionCache.Get/Refresh are mutex-protected, so concurrent runners
// (TUI + --serve/--rpc) share it safely.
//
// There is no config/file-change hook wired to Refresh yet; the fingerprint
// already re-discovers when a definition file changes on disk, and the per-run
// Get calls collapse to a stat-only hit while nothing has changed.
var (
	defsOnce sync.Once
	defs     *DefinitionCache
)

func sharedDefs() *DefinitionCache {
	defsOnce.Do(func() { defs = &DefinitionCache{} })
	return defs
}

// contextPrompt handles slash-style /context input the same way reviewPrompt
// handles /review: it injects a read-only report for the model to summarise.
// The built-in descriptor is registered through commands.NewSetWithBuiltins,
// and the report itself comes from commands.ContextHook so the app and the
// commands package share one renderer. Registration is checked before routing;
// if the built-in were missing, the prompt would be left untouched.
func contextPrompt(res Resolved, stdio cli.IO, prompt string) (string, bool) {
	text := strings.TrimSpace(prompt)
	if text != "/context" && !strings.HasPrefix(text, "/context ") {
		return "", false
	}
	home := homeDir(stdio.Env)
	sources := commands.DefaultSources(res.Root, home)
	// Plugin-provided commands join the set under their plugin namespace. The
	// helper applies the same user/project trust gate as MCP and skills, so an
	// untrusted checkout cannot inject commands into the session.
	sources = append(sources, pluginCommandSources(res)...)
	set := commands.NewSetWithBuiltins(sources)
	if _, ok := set.Get("context"); !ok {
		return "", false
	}

	docs := projectMemoryDocs(res.Root, stdio.Env, trustFromEnv(stdio.Env))
	var memoryDocs []commands.MemoryDoc
	for _, doc := range docs {
		memoryDocs = append(memoryDocs, commands.MemoryDoc{
			Path:      doc.Path,
			Bytes:     len(doc.Text),
			Truncated: doc.Truncated,
			Reason:    "per-file or total size cap",
		})
	}
	// Only the skill half of the cached discovery is needed here. The command
	// set above stays uncached: NewSetWithBuiltins mutates its set in place.
	_, skillRegistry := sharedDefs().Get(nil, skills.DefaultRoots(res.Root, home))
	input := commands.ContextInput{
		MemoryDocs:   memoryDocs,
		SkillCount:   skillRegistry.Len(),
		CommandCount: set.Len(),
		SandboxMode:  res.Sandbox.String(),
		ApprovalMode: res.Approval,
		Ignore:       ignoreStatus(res.Root),
	}
	report, err := commands.ContextHook{Input: input}.Run(strings.TrimSpace(strings.TrimPrefix(text, "/context")))
	if err != nil {
		return "The /context command failed: " + err.Error() + ". Answer the user directly.", true
	}
	return "Read-only /context report:\n" + report +
		"\nSummarise the findings; do not modify files.", true
}

// ignoreStatus reports which .argentignore files affect this workspace. The
// rule count is deliberately left at zero: IgnoreRules keeps its patterns
// private, and /context only needs a best-effort status line.
func ignoreStatus(root string) commands.IgnoreStatus {
	var files []string
	dir := filepath.Clean(root)
	for depth := 0; depth < 32; depth++ {
		path := filepath.Join(dir, memory.IgnoreFileName)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			files = append(files, path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return commands.IgnoreStatus{Files: files}
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
	if injection, ok := contextPrompt(res, stdio, prompt); ok {
		prompt = injection + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	// Resolve builds the legacy system prompt for every caller. When the
	// resolved trust decision opts in, rebuild the project-memory block through
	// the boundary-aware loader so res.TrustProject actually reaches it (the
	// flag can override an ARGENT_TRUST_PROJECT=0 env value). When not opted
	// in, keep res.System byte-identical so existing callers are unaffected.
	trustedSystem := res.System
	if res.TrustProject {
		trustedSystem = buildSystemWithTrust(res.Root, stdio.Env, true, res.Thinking, res.OutputSchema)
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

	// The inspection layer is an opt-in extra gate in front of the chain; with
	// the gate off guard is the chain itself, so every existing run is byte-for-
	// byte unchanged (policy -> approval -> audit).
	var guard tools.Guard = chain
	if inspectionEnabled(stdio.Env) {
		guard = newInspectionGuard(chain, approver, stdio)
	}

	toolset := newToolset(ctx, stdio, res, sess, guard)
	defer toolset.Close()
	var runner agent.ToolRunner = toolset
	if bridge != nil {
		runner = &hookedTools{inner: toolset, bridge: bridge}
	}

	tracker := newCheckpointTracker(res.Root, sess)
	var evidence agent.CompactionEvidence
	compactor := newSessionCompactor(res, sess)
	// Durability barriers: flush the JSONL session before each expensive step.
	// These are per-Runner fields, so concurrent Execute calls (TUI + RPC, or
	// multiple sessions) cannot clear each other's barrier or sync a stale
	// session bound to an earlier run.
	flush := func() error { return sess.Sync() }
	loop := &agent.Runner{Opts: agent.Options{
		BeforeRequest: flush,
		BeforeTool:    flush,
		Model:         res.Model,
		MaxTurns:      res.MaxTurns,
		MaxParallel:   res.MaxParallel,
		Root:          res.Root,
		System:        trustedSystem,
		Chatter:       res.Chatter,
		Tools:         runner,
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
		// Opt-in bounded verification. Both helpers return zero values when no
		// verify command is configured, keeping default runs byte-identical.
		Verify:               verifyFuncFor(res),
		MaxVerifyReflections: verifyReflectionsFor(res),
		// Mid-turn compaction is injected from the app layer so agent never
		// imports session. A disabled/negative threshold yields 0 and no-ops.
		Compactor:        compactor,
		CompactThreshold: compactor.CompactThreshold(),
		OnCompactionEvidence: func(e agent.CompactionEvidence) {
			evidence = e
		},
		Emit: func(event agent.Event) {
			// Publish to the per-run event hub so --serve / --rpc subscribers
			// (SSE /v1/event, event/subscribe) see real agent events. The sink is
			// injected into ctx by runner.Prompt; a no-op when absent.
			EmitEvent(ctx, event)
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
		inner := security.NewPromptApprover(stdio.In, stdio.Err)
		if ttl, on := ttlApprovalGate(stdio.Env); on {
			// TTL grants replace CachedApprover rather than stacking on top of
			// it: stacking would let a TTL grant be re-granted forever by the
			// session-lifetime cache once it expired.
			return &ttlApprover{
				cache: security.NewTTLCache(filepath.Join(res.Storage, "ttl-approvals"), ttl),
				inner: inner,
			}
		}
		return security.NewCachedApprover(inner)
	}
}

// inspectionEnabled reports whether the optional fail-soft inspection layer is
// on. Default-off: only ARGENT_INSPECTION=1 opts in; every other value keeps the
// historical policy -> approval -> audit behaviour unchanged.
func inspectionEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_INSPECTION")) == "1"
}

// destructiveCommandInspector is the built-in inspector for the opt-in layer.
// It reuses security's token-aware command classifier and returns Allow for
// everything that is not a mutating shell command, so ordinary read-only calls
// are not delayed by an extra verdict. A detection maps to RequireApproval
// (never Deny): the operator still makes the final call.
type destructiveCommandInspector struct{}

func (destructiveCommandInspector) Name() string { return "destructive-command" }

func (destructiveCommandInspector) Inspect(ctx context.Context, tool string, args json.RawMessage) (security.Verdict, error) {
	if err := ctx.Err(); err != nil {
		return security.Verdict{}, err
	}
	token := security.FindFileEditingCommand(shellCommandOf(tool, args))
	if token == "" {
		return security.Verdict{
			Action:     security.InspectAllow,
			Reason:     "no destructive shell command detected",
			Confidence: 1,
			Inspector:  "destructive-command",
		}, nil
	}
	return security.Verdict{
		Action:     security.InspectRequireApproval,
		Reason:     "destructive shell command: " + token,
		Confidence: 1,
		Inspector:  "destructive-command",
	}, nil
}

// shellCommandOf extracts command text from a shell tool's JSON arguments. It
// mirrors security's unexported guardShellCommand so the inspector can reuse
// FindFileEditingCommand without changing the security package.
func shellCommandOf(tool string, args json.RawMessage) string {
	name := strings.ToLower(strings.TrimSpace(tool))
	if name != "bash" && name != "powershell" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
		Script  string `json:"script"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return ""
	}
	if strings.TrimSpace(payload.Command) == "" {
		return payload.Script
	}
	return payload.Command
}

// inspectionGuard folds one fail-soft inspection verdict into the single
// tools.Guard the registry already calls. Deny is a hard StopError; a
// RequireApproval is asked once here and replayed to Chain.Check through a
// fixed approver so the operator is never prompted twice; Allow (or an unknown
// action) adds no gate and simply falls through to the chain.
type inspectionGuard struct {
	manager  *security.InspectionManager
	chain    *security.Chain
	approver security.Approver
}

func (g *inspectionGuard) Check(ctx context.Context, tool string, args json.RawMessage) error {
	verdict := g.manager.Resolve(ctx, tool, args)
	switch verdict.Action {
	case security.InspectDeny:
		return &security.StopError{Tool: tool, Reason: verdict.Reason}
	case security.InspectRequireApproval:
		ok, err := g.approver.Ask(ctx, tool, string(args))
		if err != nil {
			return err
		}
		if !ok {
			return &security.StopError{Tool: tool, Reason: verdict.Reason, Refused: true}
		}
		// Replay the recorded answer through a per-call chain copy; the shared
		// chain keeps its real approver for calls that need no inspection gate.
		callChain := *g.chain
		callChain.Approver = fixedApprover{granted: true}
		return callChain.Check(ctx, tool, args)
	default:
		return g.chain.Check(ctx, tool, args)
	}
}

// fixedApprover replays one already-recorded answer. It is used only for the
// single Chain.Check following a RequireApproval verdict, so no state is shared
// across concurrent tool calls.
type fixedApprover struct{ granted bool }

func (a fixedApprover) Ask(ctx context.Context, _, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return a.granted, nil
}

// newInspectionGuard builds the opt-in layer with the built-in inspector. The
// InspectionManager's zero value is usable (the building block exposes no
// constructor), and its error handler is fail-soft: a broken inspector is
// skipped and only its name is logged, never the raw error text.
func newInspectionGuard(chain *security.Chain, approver security.Approver, stdio cli.IO) *inspectionGuard {
	manager := &security.InspectionManager{}
	manager.Add(destructiveCommandInspector{})
	if stdio.Err != nil {
		manager.SetErrorHandler(func(name string, _ error) {
			fmt.Fprintf(stdio.Err, "inspection: inspector %q failed and was skipped\n", name)
		})
	}
	return &inspectionGuard{manager: manager, chain: chain, approver: approver}
}

// ttlApprovalGate parses the ARGENT_APPROVAL_TTL opt-in. "0" and an empty value
// leave the gate off. A Go duration ("15m") sets an exact lifetime; a bare
// positive integer is read as minutes; any other truthy value enables the gate
// with the cache's 30-minute default.
func ttlApprovalGate(env func(string) string) (time.Duration, bool) {
	if env == nil {
		return 0, false
	}
	raw := strings.TrimSpace(env("ARGENT_APPROVAL_TTL"))
	if raw == "" || raw == "0" {
		return 0, false
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d, true
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Minute, true
	}
	return 0, true
}

// ttlApprover grants approvals backed by security.TTLCache. It is used in place
// of CachedApprover when the TTL gate is on: grants expire (and can survive a
// restart via the on-disk cache), while refusals are never cached — a refusal
// only clears any prior grant for the exact (tool, args) key.
type ttlApprover struct {
	cache *security.TTLCache
	inner security.Approver
}

func (a *ttlApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	key := a.cache.Key(name, json.RawMessage(args))
	if a.cache.IsAllowed(key) {
		return true, nil
	}
	ok, err := a.inner.Ask(ctx, name, args)
	if err != nil || !ok {
		if err == nil {
			a.cache.Deny(key)
		}
		return ok, err
	}
	_ = a.cache.Allow(key, name+" "+args)
	return true, nil
}
