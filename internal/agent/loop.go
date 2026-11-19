package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

// cancelledCallReason is the tool result written for a call that never ran.
const cancelledCallReason = "tool did not run: the run was cancelled"

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

	usage        llm.Usage
	costUSD      float64
	costKnown    bool
	promptTokens int
	turns        int
	depth        int
	maxTurns     int
	lastText     string
	trim         TrimOptions
}

// execute is the internal entry point shared by Run, RunText and Prompt.
func (r *Runner) execute(ctx context.Context, prompt string, emit func(Event)) (Result, error) {
	opts, err := r.resolved()
	if err != nil {
		return Result{}, err
	}
	state := newRunState(opts, prompt, emit)
	return state.loop(ctx)
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
	s := &runState{
		opts:     opts,
		model:    opts.Model,
		tools:    newToolset(opts.Tools, opts),
		emitFn:   emit,
		rec:      normaliseRecorder(opts.Record),
		steer:    &steering{ch: opts.Steer},
		guard:    newDedupe(repeatsFor(opts)),
		depth:    opts.Depth,
		maxTurns: maxTurnsFor(opts),
		trim: TrimOptions{
			MaxTokens:      trimTarget(opts),
			KeepTurns:      opts.KeepTurns,
			KeepToolTokens: opts.KeepToolTokens,
		},
	}
	s.messages = []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}
	s.record("user", prompt)
	return s
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

// loop runs turns until the model answers without tools, a limit trips, or the
// context is cancelled.
func (s *runState) loop(ctx context.Context) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			s.emit(Event{Kind: EventStop, Reason: StopCancelled})
			return s.result(StopCancelled), err
		}
		s.injectSteering()
		if s.turns >= s.maxTurns {
			s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("turn limit reached after %d turn(s)", s.turns)})
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
		s.specs = s.tools.Specs()
		target := s.trim.MaxTokens
		if schemaTokens := EstimateTools(s.specs); schemaTokens > 0 && target > schemaTokens+1000 {
			target -= schemaTokens
		}
		trimOpts := s.trim
		trimOpts.MaxTokens = target
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
		reply, err := s.chat(ctx, turn)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				s.emit(Event{Kind: EventStop, Reason: StopCancelled})
				return s.result(StopCancelled), ctxErr
			}
			s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error()})
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

		if len(reply.ToolCalls) == 0 {
			// A correction that arrived while the answer was streaming still
			// deserves an answer: another turn instead of finishing.
			if s.injectSteering() {
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

		if stopped := s.runTools(ctx, turn, reply.ToolCalls); stopped != "" {
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
	}
}

// chat issues one model request and forwards answer deltas as they stream.
func (s *runState) chat(ctx context.Context, turn int) (llm.Message, error) {
	var streamed strings.Builder
	reply, err := s.opts.Chatter.Chat(ctx, s.model, s.messages, s.specs, func(delta string) {
		if delta == "" {
			return
		}
		streamed.WriteString(delta)
		s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: delta})
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
	return usage, usage.PromptTokens
}

// injectSteering moves queued corrections into the conversation.
func (s *runState) injectSteering() bool {
	pending := s.steer.drain()
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

// emit delivers one event, stamping the subagent depth and mirroring text
// deltas to the legacy Out writer.
func (s *runState) emit(event Event) {
	event.Depth = s.depth
	if event.Kind == EventTextDelta && s.opts.Out != nil {
		_, _ = io.WriteString(s.opts.Out, event.Text)
	}
	if s.emitFn != nil {
		s.emitFn(event)
	}
}

// record persists one transcript entry. A recorder failure never stops the run.
func (s *runState) record(kind string, payload any) {
	if s.rec == nil {
		return
	}
	_ = s.rec.Append(kind, payload)
}
