package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// cancelledCallReason is the tool result written for a call that never ran.
const cancelledCallReason = "tool did not run: the run was cancelled"

// continueAfterOutputLimit is the synthetic user turn appended when the model
// runs out of output tokens before it asks for tools. Mirrors OpenCode's
// CONTINUE_AFTER_OUTPUT_LIMIT: the model usually knows what is left to say but
// needs a fresh output budget in which to say it in smaller pieces.
const continueAfterOutputLimit = "Continue the work, but break the remaining steps into smaller pieces."

// maxConsecutiveLengthFinishes bounds how often the loop asks the model to
// continue after output-limit truncations. Without a bound, a model that always
// hits the same limit would keep the run alive until the turn budget expired.
const maxConsecutiveLengthFinishes = 2

// defaultTrimTargetRatio is the hysteresis ratio applied to the trimming
// budget: Trim aims for this fraction of MaxTokens so a successful trim leaves
// headroom and the next turn does not immediately re-trigger trimming (which
// would invalidate the prompt cache). Mirrors gptme's 0.7 target.
const defaultTrimTargetRatio = 0.7

// stopRepeatedMistakes ends a run when consecutive tool failures reach the
// mistake tracker's limit and no recovery guidance is available. It is an
// internal stop reason: the public Stop* constants are unchanged.
const stopRepeatedMistakes = "repeated_mistakes"

// defaultMistakeGuidance keeps a run alive after a streak of tool failures by
// asking the model to change its approach. The tracker owns only the decision;
// the instruction is injected as a user turn at the next loop entry.
func defaultMistakeGuidance(MistakeKind, int) string {
	return "连续出错，请换一种方法或换模型"
}

// runState is the mutable state of one run. A subagent gets its own state, so
// nothing but the configured interfaces is shared with the parent.
type runState struct {
	opts     Options
	model    string
	tools    ToolRunner
	specs    []llm.ToolSpec
	messages []llm.Message

	emitFn func(Event)
	rec    Recorder
	steer  *steering
	guard  *dedupe

	// beforeRequest and beforeTool are this run's injected durability seams,
	// copied from Options. A nil hook is a no-op, so manually built run states
	// (e.g. in tests) keep their previous behaviour.
	beforeRequest func() error
	beforeTool    func() error
	// mistakes counts consecutive tool failures across turns. Nil-receiver
	// methods make a manually built runState (e.g. in tests) safe without one.
	mistakes *MistakeTracker
	// invalidBreaker counts argument-validation rejections within a turn. It is
	// lazily created by dispatch's breaker() and reset at each runTools entry, so
	// manually built run states need no explicit construction.
	invalidBreaker *InvalidArgBreaker
	// stuck watches event patterns for repetition loops. Nil-receiver Observe
	// keeps manually built run states safe without one.
	stuck *StuckDetector

	usage        llm.Usage
	costUSD      float64
	costKnown    bool
	costWatch    *CostWatcher
	promptTokens int
	turns        int
	depth        int
	// bgOwner is this run's delivery identity inside Subagent.Background. It
	// differs from depth: sibling subagents share a depth but must never drain
	// each other's background results.
	bgOwner  uint64
	maxTurns int
	lastText string
	trim     TrimOptions
	events   []Event
	// eventsMu serialises event recording: a streaming tool's progress callback
	// may run on its own goroutine while the loop emits a turn boundary.
	eventsMu sync.Mutex
	// recordMu serialises recorder writes for the same reason.
	recordMu sync.Mutex

	// Delivery to the outside world (emitFn and the legacy Out writer) happens
	// outside eventsMu so a slow consumer cannot block event recording or a turn
	// boundary. emitSeq is the next append-order ticket and is guarded by
	// eventsMu; emitDelivered is the next ticket allowed out and is guarded by
	// emitSendMu. The cond gate makes emitFn observe exactly the append order
	// even when a streaming tool's progress callback emits from a worker.
	emitSeq       uint64
	emitSendMu    sync.Mutex
	emitDelivered uint64
	emitOnce      sync.Once
	emitCond      *sync.Cond

	// toolMeta is the in-process metadata produced by the tool runner, keyed by
	// ToolCallID. A parallel batch writes distinct keys from worker goroutines,
	// so access is guarded. It is never persisted: after resume the regular
	// token-based trimming still works.
	metaMu   sync.Mutex
	toolMeta map[string]ToolMeta

	// compactor and compactThreshold drive the mid-turn compaction boundary.
	compactor        Compactor
	compactThreshold int

	// consecutiveLengthFinishes counts replies ended for output length without
	// an intervening non-length reply. It drives the continuation budget for
	// tool-free answers and is reset by every reply that was not truncated.
	consecutiveLengthFinishes int

	// verifyState bounds post-edit verification feedback for this run. It is
	// only consulted when Options.Verify is set, so unconfigured runs never
	// touch it.
	verifyState VerifyLoopState
	// batchVerificationEligible records whether the most recently processed
	// batch went through normal dispatch and contained a mutating call. A batch
	// rejected wholesale (truncated arguments, a failing BeforeTool hook or
	// invalid call IDs) is answered with fillUnrun instead; no edit happened,
	// so post-edit verification must not spend a reflection or stop the run.
	// Individual repeat-guard blocks inside a dispatched batch stay eligible:
	// they are the model's own failed edit attempts, not a loop-level rejection.
	batchVerificationEligible bool
}

// execute is the internal entry point shared by Run, RunText and Prompt.
func (r *Runner) execute(ctx context.Context, prompt string, emit func(Event)) (Result, error) {
	opts, err := r.resolved()
	if err != nil {
		return Result{}, err
	}
	state := newRunState(opts, prompt, emit)
	// Memoise the injected compactor by source prefix hash so an identical
	// prefix is not summarised twice across prompts on this Runner. The wrapper
	// is transparent when no compactor is configured.
	state.compactor = newCachingCompactor(state.compactor, r.compactionCache())
	res, err := state.loop(ctx)
	if state.opts.OnCompactionEvidence != nil {
		state.opts.OnCompactionEvidence(CollectCompactionEvidence(state.eventsSnapshot(), res.Messages))
	}
	// Persist one run-totals stats record through the injected Recorder. The app
	// wires Record to Session.Append, so this becomes a kind:"stats" record that
	// LatestStats/AggregateStats read back after resume. It is a no-op without a
	// recorder. A cancelled run is skipped so its transcript still ends with the
	// synthetic terminal assistant (the recorder's last row stays "assistant");
	// per-request usage from account() is still persisted for such runs.
	if res.Stopped != StopCancelled {
		state.recordRunStats()
	}
	return res, err
}

func newRunState(opts Options, prompt string, emit func(Event)) *runState {
	system := strings.TrimSpace(opts.System)
	if system == "" {
		root := opts.Root
		if root == "" {
			root = "."
		}
		system = fmt.Sprintf(systemTemplate, root)
	}
	// The root run has no owner yet. Ask its queue for a stable one: every Run
	// on the same queue (including a schema retry or a resumed Runner's next
	// prompt) resolves to the same owner. A different queue resolves against
	// its own counter and never mixes results with this one. Children already
	// carry an owner assigned by childOptions.
	if opts.bgOwner == 0 && opts.Subagent != nil && opts.Subagent.Background != nil {
		opts.bgOwner = opts.Subagent.Background.RootOwner()
	}
	s := &runState{
		opts:          opts,
		model:         opts.Model,
		tools:         newToolset(opts.Tools, opts),
		emitFn:        emit,
		rec:           normaliseRecorder(opts.Record),
		steer:         &steering{ch: opts.Steer},
		beforeRequest: opts.BeforeRequest,
		beforeTool:    opts.BeforeTool,
		guard:         newDedupe(repeatsFor(opts)),
		mistakes:      NewMistakeTracker(0, defaultMistakeGuidance),
		stuck:         &StuckDetector{},
		depth:         opts.Depth,
		bgOwner:       opts.bgOwner,
		maxTurns:      maxTurnsFor(opts),
		trim: TrimOptions{
			MaxTokens:      trimTarget(opts),
			KeepTurns:      opts.KeepTurns,
			KeepToolTokens: opts.KeepToolTokens,
			TargetRatio:    defaultTrimTargetRatio,
		},
		toolMeta:         make(map[string]ToolMeta),
		compactor:        opts.Compactor,
		compactThreshold: opts.CompactThreshold,
		costWatch:        NewCostWatcher(opts.Budget.MaxCostUSD),
		verifyState:      VerifyLoopState{Max: opts.MaxVerifyReflections, Root: opts.Root},
	}
	s.messages = []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}
	s.record("user", prompt)
	return s
}

// runBeforeRequest invokes this run's injected request barrier and returns its
// first (and only) error. A nil hook is a no-op, matching an unconfigured run.
func (s *runState) runBeforeRequest() error {
	if s.beforeRequest != nil {
		return s.beforeRequest()
	}
	return nil
}

// runBeforeTool invokes this run's injected tool barrier. A nil hook is a no-op.
func (s *runState) runBeforeTool() error {
	if s.beforeTool != nil {
		return s.beforeTool()
	}
	return nil
}

// trimTarget resolves the estimated prompt size trimming aims for.
func trimTarget(opts Options) int {
	target := opts.MaxContextTokens
	if target <= 0 {
		target = defaultContextTokens
	}
	if max := opts.Budget.MaxPromptTokens; max > 0 && max < target {
		target = max
	}
	return target
}

// setToolMeta records the metadata one tool call produced. The caller may be a
// worker goroutine in a parallel batch, so writes are mutex protected.
func (s *runState) setToolMeta(id string, meta ToolMeta) {
	if meta == (ToolMeta{}) {
		return
	}
	s.metaMu.Lock()
	s.toolMeta[id] = meta
	s.metaMu.Unlock()
}

// toolMetaSnapshot copies the metadata collected so far for Trim. Callers get
// their own map, so Trim never races with a still-running batch.
func (s *runState) toolMetaSnapshot() map[string]ToolMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	out := make(map[string]ToolMeta, len(s.toolMeta))
	for id, meta := range s.toolMeta {
		out[id] = meta
	}
	return out
}

// loop runs turns until the model answers without tools, a limit trips, or the
// context is cancelled.
func (s *runState) loop(ctx context.Context) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			s.appendAbortedAssistant()
			s.emit(Event{Kind: EventStop, Reason: StopCancelled})
			return s.result(StopCancelled), err
		}
		s.injectSteering()
		// Re-evaluate the consecutive-mistake streak at every turn entry. No
		// guidance available means the run cannot recover and ends here;
		// otherwise the recovery instruction becomes a fresh user turn. Both
		// decisions are nil-safe for run states built without a tracker.
		if stop, action := s.mistakes.Check(); stop {
			s.emit(Event{Kind: EventStop, Reason: stopRepeatedMistakes})
			return s.result(stopRepeatedMistakes), nil
		} else if action.Guidance != "" {
			s.messages = append(s.messages, llm.Message{Role: "user", Content: action.Guidance})
			s.record("mistake_guidance", action.Guidance)
		}
		if s.turns >= s.maxTurns {
			// Cancellation may have arrived after the loop's top-of-turn check
			// (e.g. while steering was drained). Treat it like any other abort,
			// not as a clean turn-limit stop.
			if err := ctx.Err(); err != nil {
				s.appendAbortedAssistant()
				s.emit(Event{Kind: EventStop, Reason: StopCancelled})
				return s.result(StopCancelled), err
			}
			s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("turn limit reached after %d turn(s)", s.turns)})
			// Ask once for a tool-free answer so an exhausted budget still
			// yields something useful. The stop reason stays StopMaxTurns,
			// unless that last request was cancelled — finalAnswer returns the
			// context error so it is not swallowed into a successful result.
			if !s.opts.NoFinalAnswer {
				if err := s.finalAnswer(ctx); err != nil {
					s.appendAbortedAssistant()
					s.emit(Event{Kind: EventStop, Reason: StopCancelled})
					return s.result(StopCancelled), err
				}
			}
			s.emit(Event{Kind: EventStop, Reason: StopMaxTurns})
			return s.result(StopMaxTurns), nil
		}
		if reason, detail, hit := s.budgetBreach(); hit {
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		// Re-read the schemas every turn: a tool may have been unlocked by the
		// previous call, and it must be visible to the very next request.
		// The optional filter hides tools the policy or sandbox forbids before
		// they are advertised, saving the model a guaranteed-failing call.
		s.specs = s.advertisedSpecs()
		target := s.trim.MaxTokens
		if schemaTokens := EstimateTools(s.specs); schemaTokens > 0 && target > schemaTokens+1000 {
			target -= schemaTokens
		}
		trimOpts := s.trim
		trimOpts.MaxTokens = target
		trimOpts.ToolMeta = s.toolMetaSnapshot()
		if trimmed := Trim(s.messages, trimOpts); trimmed.Pruned > 0 {
			s.messages = trimmed.Messages
			s.emit(Event{
				Kind: EventNotice,
				Text: fmt.Sprintf("trimmed %d old tool result(s), ~%d tokens freed", trimmed.Pruned, trimmed.Freed),
			})
		}
		if max := s.opts.Budget.MaxPromptTokens; max > 0 {
			estimate := EstimateMessages(s.messages) + EstimateTools(s.specs)
			if estimate >= max {
				detail := fmt.Sprintf("%s: prompt estimate %d tokens (limit %d)", StopMaxPromptTokens, estimate, max)
				s.emit(Event{Kind: EventNotice, Text: detail})
				s.emit(Event{Kind: EventStop, Reason: StopMaxPromptTokens})
				return s.result(StopMaxPromptTokens), nil
			}
		}

		turn := s.turns
		promptEstimate := EstimateMessages(s.messages) + EstimateTools(s.specs)
		s.emit(Event{Kind: EventTurnStart, Turn: turn})
		// Durability seam: flush the session before the expensive request. A
		// hook error is treated like any other chat failure — no request is
		// made, and the run ends as a provider error rather than risking work
		// the store is not ready to survive.
		if err := s.runBeforeRequest(); err != nil {
			failure := llm.ClassifyFailure(err)
			s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error(), Failure: &failure})
			s.emit(Event{Kind: EventStop, Reason: StopProviderError})
			return s.result(StopProviderError), err
		}
		reply, err := s.chatWithFallback(ctx, turn)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				s.appendAbortedAssistant()
				s.emit(Event{Kind: EventStop, Reason: StopCancelled})
				return s.result(StopCancelled), ctxErr
			}
			failure := llm.ClassifyFailure(err)
			s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error(), Failure: &failure})
			s.emit(Event{Kind: EventStop, Reason: StopProviderError})
			return s.result(StopProviderError), err
		}
		if reply.Reasoning != "" {
			// Reasoning arrives as one block through Chatter; a provider that
			// streams it separately still renders correctly here.
			s.emit(Event{Kind: EventReasoningDelta, Turn: turn, Text: reply.Reasoning})
		}
		turnUsage, promptTokens := s.account(reply, promptEstimate)
		s.promptTokens = promptTokens
		s.turns++
		s.messages = append(s.messages, llm.Message{
			Role:      "assistant",
			Content:   reply.Content,
			ToolCalls: reply.ToolCalls,
			Reasoning: reply.Reasoning,
		})
		if reply.Content != "" {
			s.lastText = reply.Content
		}
		s.record("assistant", reply)
		s.emit(Event{Kind: EventTurnEnd, Turn: turn})
		s.emit(Event{Kind: EventUsage, Turn: turn, Usage: turnUsage, TotalUsage: s.usage})

		// Classify the finish once: a length truncation matters both to the
		// tool batch below and to the tool-free continuation path. Every reply
		// that was not truncated restores the continuation budget.
		truncated := s.replyTruncated(reply)
		if truncated {
			s.consecutiveLengthFinishes++
		} else {
			s.consecutiveLengthFinishes = 0
		}

		if len(reply.ToolCalls) == 0 {
			// A correction that arrived while the answer was streaming still
			// deserves an answer: another turn instead of finishing.
			if s.injectSteering() {
				continue
			}
			if truncated {
				// The answer is incomplete only because the output budget ran
				// out. Ask for the remainder in a fresh turn, but do not let a
				// model stuck at the same limit spin until the turn budget ends.
				if s.consecutiveLengthFinishes >= maxConsecutiveLengthFinishes {
					s.emit(Event{Kind: EventStop, Reason: StopOutputLimit})
					return s.result(StopOutputLimit), nil
				}
				s.messages = append(s.messages, llm.Message{Role: "user", Content: continueAfterOutputLimit})
				s.record("user", continueAfterOutputLimit)
				s.emit(Event{Kind: EventNotice, Text: "output limit reached; asking the model to continue in smaller steps"})
				continue
			}
			s.emit(Event{Kind: EventStop, Reason: StopComplete})
			return s.result(StopComplete), nil
		}

		// Checked before the batch so a blown budget does not spend tool time.
		if reason, detail, hit := s.budgetBreach(); hit {
			s.fillUnrun(reply.ToolCalls, "run stopped on its budget before this call executed")
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		if truncated {
			// A length-truncated reply can carry half-written tool arguments.
			// Pair every call with an error result but execute none of them.
			s.fillUnrun(reply.ToolCalls, truncatedToolReason)
			s.emit(Event{Kind: EventNotice, Text: truncatedToolReason})
		} else if err := s.runBeforeTool(); err != nil {
			// Durability barrier failed: no tool may be dispatched. Answer
			// every call with an ordered error result so the transcript keeps
			// each tool call paired with its result.
			reason := fmt.Sprintf("tool dispatch aborted: %v", err)
			s.fillUnrun(reply.ToolCalls, reason)
			s.emit(Event{Kind: EventNotice, Text: reason})
		} else if stopped := s.runTools(ctx, turn, reply.ToolCalls); stopped != "" {
			if stopped == StopCancelled {
				// The batch already appended its (unrun) tool results; close the
				// transcript with a terminal assistant so the last message is
				// the abort, not a pending tool use.
				s.appendAbortedAssistant()
			}
			s.emit(Event{Kind: EventStop, Reason: stopped})
			if stopped == StopCancelled {
				return s.result(stopped), ctx.Err()
			}
			return s.result(stopped), nil
		}

		// ... and again after it, because tools may have taken a long time and
		// the next request is the expensive part.
		if reason, detail, hit := s.budgetBreach(); hit {
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		// A batch of tool results is the natural place to look for repetition
		// loops. The detector is one-shot per episode: a stuck pattern becomes
		// a corrective user turn, not a hard stop.
		if guidance, _ := s.stuck.Observe(s.eventsSnapshot()); guidance != "" {
			s.injectSteering(guidance)
		}

		// A batch of tool results is the natural place to summarise: the next
		// request is about to pay for the whole history again.
		s.maybeCompact()

		// Bounded post-edit verification (default off). Runs after compaction so
		// a freshly injected feedback turn stays at the tail of the next request.
		if reason, stop := s.verifyAfterBatch(ctx, turn, reply.ToolCalls); stop {
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}
	}
}

// maybeCompact implements the mid-turn compaction boundary. It only runs after
// a tool batch, never between a tool call and its result. The target window is
// the 0.8 hysteresis band minus a summary reserve, so one compaction relieves
// pressure instead of re-triggering on the next check.
func (s *runState) maybeCompact() {
	if s.compactor == nil || s.compactThreshold <= 0 {
		return
	}
	before := EstimateMessages(s.messages)
	if before <= s.compactThreshold {
		return
	}
	window := int(float64(s.compactThreshold)*compactionRecoveryBand) - summaryReserveTokens
	if window < minCompactWindow {
		window = minCompactWindow
	}
	compacted, changed := s.compactor.MaybeCompact(s.messages, window)
	if !changed || len(compacted) == 0 {
		return
	}
	s.messages = compacted
	// Re-estimate now so the budget check at the top of the next iteration sees
	// the compacted history rather than the old, oversized one.
	s.promptTokens = EstimateMessages(s.messages)
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"compacted context: ~%d -> ~%d tokens (target %d)", before, s.promptTokens, window,
	)})
}

// advertisedSpecs resolves the tool list for the next request. Without a
// filter the runner's full catalog is used; with one, only the allowed subset
// is advertised. Authorization still happens in Run, never here. The final
// list is projected for the current model's schema quirks before it is sent.
func (s *runState) advertisedSpecs() []llm.ToolSpec {
	specs := s.tools.Specs()
	if s.opts.FilterSpecs != nil {
		specs = s.opts.FilterSpecs(specs)
	}
	return projectSpecsForModel(specs, s.model)
}

// projectSpecsForModel applies the model-family schema projection to every
// non-empty Parameters document. The returned slice is always a fresh copy, so
// projecting a request never mutates the toolset's own spec catalog.
func projectSpecsForModel(specs []llm.ToolSpec, model string) []llm.ToolSpec {
	compat := llm.CompatNone
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "moonshot"), strings.Contains(name, "kimi"):
		compat = llm.CompatMoonshot
	case strings.Contains(name, "gemini"):
		compat = llm.CompatGemini
	}
	if compat == llm.CompatNone {
		return specs
	}

	out := make([]llm.ToolSpec, len(specs))
	copy(out, specs)
	for i := range out {
		raw := out[i].Function.Parameters
		if len(raw) == 0 {
			continue
		}
		projected := llm.ProjectToolSchema(string(raw), compat)
		if projected != string(raw) {
			out[i].Function.Parameters = json.RawMessage(projected)
		}
	}
	return out
}

// chat issues one model request with the current model and forwards answer
// deltas to onText. Emission is left to the caller so a failed attempt can be
// retried without leaking partial text into the operator's stream.
func (s *runState) chat(ctx context.Context, onText func(string)) (llm.Message, error) {
	var streamed strings.Builder
	// Repair the transcript *view* handed to the provider: after a resume or an
	// interrupted batch an assistant tool_use may have lost its result, which
	// strict providers reject. NormalizeToolPairing is a pure view helper, so
	// s.messages keeps its original bytes and only the outgoing request is fixed.
	messages := NormalizeToolPairing(s.messages)
	reply, err := s.opts.Chatter.Chat(ctx, s.model, messages, s.specs, func(delta string) {
		if delta == "" {
			return
		}
		streamed.WriteString(delta)
		if onText != nil {
			onText(delta)
		}
	})
	if err != nil {
		return llm.Message{}, err
	}
	if reply.Role == "" {
		reply.Role = "assistant"
	}
	if reply.Content == "" && streamed.Len() > 0 {
		// A client that streams but forgets to assemble still gives the
		// transcript the text the operator already saw.
		reply.Content = streamed.String()
	}
	return reply, nil
}

// The two payload types below mirror the wire shape of session.UsageRecord and
// session.SessionStats without importing the session package: the agent layer
// stays dependency-free. They are written through the injected Recorder, which
// the app wires to Session.Append. Keeping the JSON field names in sync is what
// lets session.UsageRecords / LatestStats / AggregateStats read them back.
type sessionUsagePayload struct {
	Model  string `json:"model,omitempty"`
	Input  int    `json:"input"`
	Output int    `json:"output"`
	Cached int    `json:"cached,omitempty"`
	TS     string `json:"ts,omitempty"`
}

type sessionStatsPayload struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostSource       string  `json:"cost_source,omitempty"`
	Updated          string  `json:"updated,omitempty"`
}

// recordRunStats persists the run's cumulative token/cost roll-up as one stats
// record. One record per run keeps session.AggregateStats' sum semantics correct
// across resumes; per-request facts are written by account().
func (s *runState) recordRunStats() {
	source := ""
	if s.costKnown {
		// Cost is derived from the injected price table, never reported by the
		// provider wire, so it is always an estimate.
		source = "estimated"
	}
	s.record("stats", sessionStatsPayload{
		PromptTokens:     s.usage.PromptTokens,
		CompletionTokens: s.usage.CompletionTokens,
		CachedTokens:     s.usage.CachedTokens,
		Calls:            s.turns,
		CostUSD:          s.costUSD,
		CostSource:       source,
		Updated:          time.Now().UTC().Format(time.RFC3339),
	})
}

// account folds one reply's usage into the run totals and returns it.
//
// Provider-reported usage wins; otherwise the estimate for the prompt that was
// just sent and the answer that came back is used, so budgets work even with a
// client that reports no usage at all.
func (s *runState) account(reply llm.Message, promptEstimate int) (llm.Usage, int) {
	var usage llm.Usage
	if reply.Usage != nil {
		usage = *reply.Usage
	}
	if usage.PromptTokens <= 0 {
		usage.PromptTokens = promptEstimate
	}
	if usage.CompletionTokens <= 0 {
		usage.CompletionTokens = EstimateTokens(reply.Content)
	}
	s.usage.PromptTokens += usage.PromptTokens
	s.usage.CompletionTokens += usage.CompletionTokens
	if price, ok := PriceFor(s.opts.Prices, s.model); ok {
		s.costUSD += price.Cost(usage)
		s.costKnown = true
	}
	// One watcher per run: each spend threshold warns once as cost accumulates.
	for _, warning := range s.costWatch.Observe(s.costUSD) {
		s.emit(Event{Kind: EventNotice, Text: warning})
	}
	// Per-request usage fact. Written mid-turn (before any terminal assistant
	// record) so it never changes which row the recorder sees last.
	s.record("usage", sessionUsagePayload{
		Model:  s.model,
		Input:  usage.PromptTokens,
		Output: usage.CompletionTokens,
		Cached: usage.CachedTokens,
		TS:     time.Now().UTC().Format(time.RFC3339),
	})
	return usage, usage.PromptTokens
}

// injectSteering moves queued corrections into the conversation. Extra text
// (e.g. a one-shot stuck nudge) joins the same user-turn path.
func (s *runState) injectSteering(extra ...string) bool {
	pending := s.steer.drain()
	// Drain finished background subagent results into this run as synthetic
	// user turns. Delivery is keyed by this run's unique owner, not by depth:
	// sibling subagents share a depth and would otherwise steal each other's
	// results. Depth stays on each queued item only for diagnostics.
	// See SubagentConfig.Background / BackgroundResults.
	if s.opts.Subagent != nil && s.opts.Subagent.Background != nil {
		pending = append(pending, s.opts.Subagent.Background.DrainFor(s.bgOwner)...)
	}
	pending = append(pending, extra...)
	for _, text := range pending {
		s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
		s.record("steer", text)
		s.emit(Event{Kind: EventNotice, Text: "steering: " + text})
	}
	return len(pending) > 0
}

// maxParallel is the worker count for a read-only batch.
func (s *runState) maxParallel() int {
	if s.opts.MaxParallel <= 1 {
		return 1
	}
	return s.opts.MaxParallel
}

// mutating reports whether a tool changes state and therefore needs approval.
func (s *runState) mutating(name string) bool {
	if s.opts.Mutating != nil {
		return s.opts.Mutating(name)
	}
	if len(s.opts.MutatingTools) > 0 {
		for _, candidate := range s.opts.MutatingTools {
			if candidate == name {
				return true
			}
		}
		return false
	}
	for _, candidate := range defaultMutatingTools {
		if candidate == name {
			return true
		}
	}
	return false
}

func (s *runState) needsApproval(name string) bool {
	return s.opts.Approve != nil && s.mutating(name)
}

func (s *runState) budgetBreach() (string, string, bool) {
	return s.opts.Budget.Breach(s.usage, s.promptTokens, s.costUSD, s.costKnown)
}

// result snapshots the run into the public Result type.
func (s *runState) result(stopped string) Result {
	return Result{
		Text:     s.lastText,
		Messages: append([]llm.Message(nil), s.messages...),
		Usage:    s.usage,
		Turns:    s.turns,
		Stopped:  stopped,
	}
}

// appendAbortedAssistant leaves a terminal assistant turn on a cancelled run.
// Without it, a consumer that derives the outcome from the last assistant
// message would see a tool-use turn (or no assistant at all) rather than the
// abort. Only the transcript changes: Result.Stopped stays StopCancelled.
func (s *runState) appendAbortedAssistant() {
	aborted := llm.Message{
		Role:         "assistant",
		Content:      "Operation aborted",
		FinishReason: "aborted",
	}
	s.messages = append(s.messages, aborted)
	s.record("assistant", aborted)
}

// emit delivers one event, stamping the subagent depth and mirroring text
// deltas to the legacy Out writer.
//
// Recording and delivery are deliberately separated: eventsMu only guards the
// append, so a slow emitFn (pipe write, SSE flush, UI render) never blocks a
// progress callback or a turn boundary from being recorded. Delivery is gated
// by an append-order ticket so emitFn still observes events in exactly the
// order they were appended, even with concurrent emitters.
func (s *runState) emit(event Event) {
	event.Depth = s.depth
	// Boundaries where a frontend may want to show "N queued" carry a snapshot
	// of the steering backlog. This is a non-draining read, so reporting the
	// count never consumes a queued correction. All other event kinds keep the
	// zero value. Pending is nil-safe, so a runState without steering is fine.
	switch event.Kind {
	case EventTurnStart, EventTurnEnd, EventStop:
		if s.steer != nil {
			event.PendingMessages = s.steer.Pending()
		}
	}
	// sync.Once keeps emit usable on a runState built without newRunState.
	s.emitOnce.Do(func() { s.emitCond = sync.NewCond(&s.emitSendMu) })

	s.eventsMu.Lock()
	seq := s.emitSeq
	s.emitSeq++
	s.events = append(s.events, event)
	s.eventsMu.Unlock()

	s.emitSendMu.Lock()
	for s.emitDelivered != seq {
		s.emitCond.Wait()
	}
	if event.Kind == EventTextDelta && s.opts.Out != nil {
		_, _ = io.WriteString(s.opts.Out, event.Text)
	}
	if s.emitFn != nil {
		s.emitFn(event)
	}
	s.emitDelivered++
	s.emitCond.Broadcast()
	s.emitSendMu.Unlock()
}

// eventsSnapshot copies the recorded events under eventsMu. Callers get their
// own slice, so a later append by a worker cannot mutate what they observe.
func (s *runState) eventsSnapshot() []Event {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return append([]Event(nil), s.events...)
}

// record persists one transcript entry. A recorder failure never stops the run.
func (s *runState) record(kind string, payload any) {
	if s.rec == nil {
		return
	}
	s.recordMu.Lock()
	_ = s.rec.Append(kind, payload)
	s.recordMu.Unlock()
}
