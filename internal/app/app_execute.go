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
//
// bg is the background-result queue owned by the caller for this one root run.
// It must NOT be a process singleton: two independent sessions/runners (e.g.
// TUI + --rpc, or two sequential Execute calls) that both opt into personas
// would otherwise share one queue, and a result from one session would be
// drained into the other session's transcript (all root runs sit at depth 0).
// Owning the queue per run also keeps the bounded-cap eviction local to a run.
func configureSubagents(opts *agent.Options, res Resolved, bg *agent.BackgroundResults) {
	if opts == nil {
		return
	}
	personas, err := loadPersonasForRoot(res.Root)
	if err != nil || len(personas) == 0 {
		return
	}
	// Append to the system prompt the caller already installed (the static,
	// cache-stable half on trust-opted runs) rather than res.System, which
	// still carries the per-run dynamic values. Direct callers set opts.System
	// to res.System, so their behaviour is byte-for-byte unchanged.
	opts.System = opts.System + "\n\n" + agent.OrchestratorPrompt()
	opts.Subagent = &agent.SubagentConfig{
		MaxDepth: agent.DefaultMaxDepth,
		ResolvePersona: func(name string) (agent.SubagentConfig, bool) {
			return PersonaSubagentConfig(res.Root, name)
		},
		Background: bg,
	}
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
	// Keep the caller's own words before review/context/dynamic-context prefixes
	// are prepended below. Budget declarations must come only from the user: a
	// "+500k" living in an injected prefix is not a declaration.
	rawPrompt := prompt

	// Resume classification: announce interrupted/degraded history and stop
	// before the agent loop when a human decision is required. Clean sessions
	// (the common case) produce no notice and fall through unchanged.
	if sess != nil {
		if err := announceRecovery(ctx, stdio, onText, recoveryDecisionFor(recoveryMessagesForClassification(sess.Messages()))); err != nil {
			return agent.Result{}, err
		}
	}

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
	//
	// On the trust-opted path the system message carries only the static half
	// of the prompt. cwd, reasoning effort and the output schema are per-run
	// values: they travel in the first user message below, so a directory or
	// thinking change no longer invalidates the provider's cached system
	// prefix. buildStaticSystem(true) keeps the explicit flag override of an
	// ARGENT_TRUST_PROJECT=0 env value.
	trustedSystem := res.System
	dynamicContext := ""
	if res.TrustProject {
		trustedSystem = buildStaticSystem(res.Root, stdio.Env, true, res.Cfg)
		dynamicContext = buildDynamicContext(res.Root, res.Thinking, res.OutputSchema)
	}
	if dynamicContext != "" {
		prompt = dynamicContext + "\n\nUser request: " + strings.TrimSpace(prompt)
	}
	if res.MaxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(res.MaxSeconds)*time.Second)
		defer cancel()
	}
	// Opt-in artifact spilling for oversized tool outputs. With no opt-in this
	// returns ctx unchanged, so the tools package keeps the ordinary clipTo path
	// and every existing run stays byte-identical. The derived ctx reaches every
	// tool via the agent loop's callTool(ctx, ...).
	ctx = configureOutputSpill(ctx, stdio.Env, res.Storage)

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
		runner = &hookedTools{inner: toolset, bridge: bridge, mutationPaths: toolset.base.mutationPaths}
	}

	tracker := newCheckpointTracker(res.Root, sess)
	var evidence agent.CompactionEvidence
	compactor := newSessionCompactor(res, sess)
	// Durability barriers: flush the JSONL session before each expensive step.
	// These are per-Runner fields, so concurrent Execute calls (TUI + RPC, or
	// multiple sessions) cannot clear each other's barrier or sync a stale
	// session bound to an earlier run.
	flush := func() error { return sess.Sync() }

	// Schema-constrained runs buffer the root answer (text deltas + stop) until
	// it passes validation. A corrective turn can then replace that answer on
	// the client instead of appending a second copy. Runs without --output-schema
	// keep the original live-streaming path byte-for-byte.
	schemaMode := strings.TrimSpace(res.OutputSchema) != ""
	var pendingTexts []agent.Event
	var pendingStop *agent.Event
	mutatingRan := false

	forwardEvent := func(event agent.Event) {
		// Publish to the per-run event hub so --serve / --rpc subscribers
		// (SSE /v1/event, event/subscribe) see real agent events. The sink is
		// injected into ctx by runner.Prompt; a no-op when absent.
		EmitEvent(ctx, event)
		// Feed the live usage tool so it reports real token totals.
		if event.Kind == agent.EventUsage {
			toolset.UpdateUsage(event.Turn, event.Usage, event.TotalUsage)
		}
		if event.Kind == agent.EventTextDelta && event.Depth == 0 && onText != nil {
			onText(event.Text)
		}
	}
	flushAnswer := func() {
		for _, event := range pendingTexts {
			forwardEvent(event)
		}
		pendingTexts = nil
		if pendingStop != nil {
			forwardEvent(*pendingStop)
			pendingStop = nil
		}
	}

	// A user-declared per-turn cap is an extra cumulative limit; parse it from
	// the raw prompt (never the injected prefixes) before the loop is built.
	budget := agent.Budget{
		MaxTurns:       res.MaxTurns,
		MaxTotalTokens: res.MaxTokens,
		MaxCostUSD:     res.MaxCostUSD,
	}
	applyDeclaredTurnBudget(rawPrompt, &budget)

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
			filtered := toolset.base.regs[0].FilterSpecs(policy, res.Sandbox, specs)
			// Privacy posture: local-only withholds web tools entirely. A remote
			// base URL under local-only is a hard error surfaced before the run.
			blocked, perr := ApplyPrivacyPosture(res.Cfg.PrivacyMode, res.BaseURL, []string{"web_search", "web_fetch"})
			if perr != nil {
				return nil
			}
			if len(blocked) > 0 {
				drop := make(map[string]bool, len(blocked))
				for _, name := range blocked {
					drop[name] = true
				}
				kept := filtered[:0]
				for _, spec := range filtered {
					if !drop[spec.Function.Name] {
						kept = append(kept, spec)
					}
				}
				return kept
			}
			return filtered
		},
		Approve:     approver,
		Record:      agent.RecorderFunc(sess.Append),
		Budget:      budget,
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
			if schemaMode {
				switch {
				case event.Kind == agent.EventTextDelta && event.Depth == 0:
					pendingTexts = append(pendingTexts, event)
					return
				case event.Kind == agent.EventStop:
					stop := event
					pendingStop = &stop
					return
				case event.Kind == agent.EventCheckpointReady:
					// The agent emits this immediately before a batch that
					// contains a mutating tool, so it is a reliable signal that
					// side effects already happened and must never be repeated.
					mutatingRan = true
				}
			}
			forwardEvent(event)
		},
	}}
	// One fresh queue per root run. Finished background children are delivered
	// only to the run (and, for a resumed Runner, the later prompts on that same
	// Runner) that owns this queue; never to an unrelated concurrent session.
	bg := agent.NewBackgroundResults(0, 0)
	configureSubagents(&loop.Opts, res, bg)
	// Per-model edit-format guidance is opt-in so byte-exact system-prompt
	// assertions and callers that pin their own prompt keep unchanged bytes by
	// default. Set ARGENT_EDIT_GUIDE=1 to append it. Appending here (after
	// configureSubagents) keeps the orchestrator append order intact.
	if env := stdio.Env; env != nil && env("ARGENT_EDIT_GUIDE") == "1" {
		loop.Opts.System = agent.AppendEditFormat(loop.Opts.System, res.Model)
	}
	system := loop.Opts.System

	result, err := loop.Run(ctx, prompt)
	if schemaMode && err == nil {
		if invalid := validateOutputText(res.OutputSchema, result.Text); invalid != nil {
			switch {
			case mutatingRan:
				// A mutating tool already ran. Re-running the prompt would repeat
				// its side effects, so surface the failure instead of retrying.
				err = fmt.Errorf("output schema validation failed (auto-retry skipped: a mutating tool already ran): %w", invalid)
			default:
				// No tool side effects to repeat: append one corrective user turn
				// to the SAME conversation and ask the model once more. This
				// bypasses the agent loop entirely - no tools are advertised and
				// no tool events are emitted - so neither text nor tool work is
				// duplicated on the client.
				corrected, cerr := correctSchemaAnswer(ctx, loop.Opts.Chatter, loop.Opts.Model, system, result.Messages, res.OutputSchema, invalid)
				if cerr != nil {
					err = fmt.Errorf("output schema retry failed: %w", cerr)
				} else if again := validateOutputText(res.OutputSchema, corrected); again != nil {
					err = fmt.Errorf("output schema validation failed after one retry: %w", again)
				} else {
					// Replace (rather than append to) the buffered first answer.
					result.Text = corrected
					result.Turns++
					pendingTexts = []agent.Event{{Kind: agent.EventTextDelta, Turn: result.Turns, Depth: 0, Text: corrected}}
					pendingStop = &agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete}
					_ = sess.Append("user", schemaCorrectionUserTurn)
					_ = sess.Append("assistant", corrected)
				}
			}
		}
	}
	flushAnswer()
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

// recoveryMessagesForClassification prepares Session.Messages for ClassifyRecovery.
// Session.Messages projects only conversation records, but a real transcript
// interleaves metadata records (usage/stats/turn) between them. A message's
// Parent therefore often names a metadata record that is absent from the
// projection, and a naive link check would flag every resumed session as
// degraded. Rewrite only those dangling parents to the preceding message;
// genuine links between two messages are left untouched. The input is copied,
// never mutated.
func recoveryMessagesForClassification(messages []session.Entry) []session.Entry {
	out := make([]session.Entry, len(messages))
	copy(out, messages)
	ids := make(map[string]struct{}, len(out))
	for _, m := range out {
		if m.ID != "" {
			ids[m.ID] = struct{}{}
		}
	}
	prevID := ""
	for i := range out {
		if i > 0 && out[i].Parent != "" && out[i].Parent != prevID {
			if _, ok := ids[out[i].Parent]; !ok {
				out[i].Parent = prevID
			}
		}
		if out[i].ID != "" {
			prevID = out[i].ID
		}
	}
	return out
}

// announceRecovery publishes a resume notice through the two channels Execute
// already uses for user-visible text (onText for the caller, EventNotice for
// --serve/--rpc subscribers). For degraded history it deliberately refuses to
// continue: Execute has no confirmation channel, so instead of inventing a UI
// prompt it writes an explicit warning to stderr and returns an error, leaving
// the transcript untouched for an operator to inspect.
//
// A clean or merely interrupted transcript returns nil immediately, keeping a
// resumed run byte-identical to before this wiring.
func announceRecovery(ctx context.Context, stdio cli.IO, onText func(string), decision recoveryDecision) error {
	if decision.Notice == "" {
		return nil
	}
	EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: decision.Notice})
	if onText != nil {
		onText(decision.Notice)
	}
	if !decision.NeedsConfirmation {
		return nil
	}

	warning := "Session recovery paused: automatic continuation is disabled for degraded history; please confirm before resuming. " + decision.Notice
	if stdio.Err != nil {
		fmt.Fprintln(stdio.Err, warning)
	}
	EmitEvent(ctx, agent.Event{Kind: agent.EventNotice, Text: warning})
	if onText != nil {
		onText(warning)
	}
	return fmt.Errorf("session recovery paused: degraded history requires operator confirmation before continuing")
}

// schemaCorrectionUserTurn is the one user turn appended to an existing
// conversation when --output-schema validation fails. The schema and the exact
// failure go into the system message (correctiveInstruction); this turn only
// tells the model that a single corrected answer is expected.
const schemaCorrectionUserTurn = "Your previous reply did not satisfy the required output schema. Reply once more with a single JSON document that conforms exactly to the schema and nothing else."

// correctSchemaAnswer asks the model for one corrected answer on the existing
// conversation. It deliberately bypasses the agent loop: the chatter is called
// directly with no tool specs, so the retry cannot call a tool, emit a tool
// event, or re-run the original prompt. The caller owns publishing the returned
// text as the single replacement answer.
func correctSchemaAnswer(ctx context.Context, chatter llm.Chatter, model, system string, messages []llm.Message, schema string, invalid error) (string, error) {
	if chatter == nil {
		return "", fmt.Errorf("no chatter configured: %w", invalid)
	}
	instruction := correctiveInstruction(schema, invalid)
	request := make([]llm.Message, 0, len(messages)+2)
	systemFound := false
	for _, message := range messages {
		if message.Role == "system" && !systemFound {
			message.Content = system + instruction
			systemFound = true
		}
		request = append(request, message)
	}
	if !systemFound {
		request = append([]llm.Message{{Role: "system", Content: system + instruction}}, request...)
	}
	request = append(request, llm.Message{Role: "user", Content: schemaCorrectionUserTurn})

	var streamed strings.Builder
	reply, err := chatter.Chat(ctx, model, request, nil, func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Content) != "" {
		return reply.Content, nil
	}
	return streamed.String(), nil
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

// autoApprover grants every request except mutating shell commands; it backs
// --yolo and approval=auto. A destructive shell command is the one call auto
// mode must never grant silently (aider's explicit_yes_required): with no human
// in the loop the model could otherwise delete or overwrite files unnoticed.
// Such calls are delegated to fallback for a real decision; with no fallback
// they are refused (fail closed). Read-only tools and read-only shell commands
// keep the single-check grant, so --yolo stays fast and convenient off the
// command path. TUI/RPC inject their own approver through Resolved.Approve and
// never use this type, so interactive approval is unchanged.
type autoApprover struct {
	fallback security.Approver
}

func (a autoApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// shellCommandOf already restricts this to bash/powershell and returns ""
	// for everything else (including malformed payloads, a safe false negative),
	// so non-shell tools never pay for command classification or a prompt.
	command := shellCommandOf(name, json.RawMessage(args))
	if command == "" || security.FindFileEditingCommand(command) == "" {
		return true, nil
	}
	if a.fallback == nil {
		// Auto mode has no standing to approve a destructive command on its
		// own. (false, nil) becomes a refusal the model can read in Chain.Check.
		return false, nil
	}
	return a.fallback.Ask(ctx, name, args)
}

func approverFor(stdio cli.IO, res Resolved) security.Approver {
	switch res.Approval {
	case "never":
		return security.NeverApprover{}
	case "auto", "yolo":
		// Auto grants non-shell work without a prompt, but a mutating shell
		// command still needs a human: PromptApprover accepts only y/yes and
		// deliberately has no "approve all" shortcut. In headless --rpc/--serve
		// runs callers inject Resolved.Approve instead, so this reader is only
		// reached on the direct CLI path where stdio.In is the operator's tty.
		return autoApprover{fallback: security.NewPromptApprover(stdio.In, stdio.Err)}
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
