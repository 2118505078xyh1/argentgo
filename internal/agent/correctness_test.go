package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// finishReasonChatter is a fake Chatter that also exposes a provider finish
// reason through the optional interface the loop consults before dispatching.
type finishReasonChatter struct {
	*fakeChatter
	reason string
}

func (c *finishReasonChatter) FinishReason() string { return c.reason }

func TestLengthTruncatedReplyNeverRunsItsTools(t *testing.T) {
	cases := []struct {
		name          string
		reason        string
		wantToolRuns  int
		wantTruncated bool
	}{
		{name: "openai length", reason: "length", wantTruncated: true},
		{name: "anthropic max_tokens", reason: "max_tokens", wantTruncated: true},
		{name: "responses max_output_tokens", reason: "MAX_OUTPUT_TOKENS", wantTruncated: true},
		{name: "ordinary stop still dispatches", reason: "stop", wantToolRuns: 2},
		{name: "no reason still dispatches", reason: "", wantToolRuns: 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
			var events []Event
			base := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					// The second call's arguments are cut off mid-token.
					return toolReply(
						toolCall("c1", "read", `{"path":"a.txt"}`),
						toolCall("c2", "read", `{"path":"b`),
					), nil
				}
				return textReply("done"), nil
			}}
			chat := &finishReasonChatter{fakeChatter: base, reason: test.reason}
			runner := &Runner{Opts: Options{
				Model: "m", Chatter: chat, Tools: tools,
				Emit: func(e Event) { events = append(events, e) },
			}}

			res, err := runner.Run(context.Background(), "read files")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if tools.count() != test.wantToolRuns {
				t.Fatalf("tool runs = %d, want %d", tools.count(), test.wantToolRuns)
			}

			type result struct{ id, output string }
			var results []result
			for _, msg := range res.Messages {
				if msg.Role == "tool" {
					results = append(results, result{msg.ToolCallID, msg.Content})
				}
			}
			if len(results) != 2 || results[0].id != "c1" || results[1].id != "c2" {
				t.Fatalf("tool results out of order: %+v", results)
			}
			if test.wantTruncated {
				for _, got := range results {
					if got.output != truncatedToolReason {
						t.Fatalf("result %q = %q, want %q", got.id, got.output, truncatedToolReason)
					}
				}
				assertOrderedToolEvents(t, events, []string{"c1", "c2"}, truncatedToolReason)
			} else if results[0].output != "read ok" || results[1].output != "read ok" {
				t.Fatalf("normal dispatch results = %+v", results)
			}
		})
	}
}

// assertOrderedToolEvents checks that each call was announced then answered in
// call order, and that every answer is the expected error text.
func assertOrderedToolEvents(t *testing.T, events []Event, ids []string, output string) {
	t.Helper()
	var sequence []string
	for _, event := range events {
		switch event.Kind {
		case EventToolStart:
			sequence = append(sequence, "start:"+event.ToolCallID)
		case EventToolEnd:
			sequence = append(sequence, "end:"+event.ToolCallID)
			if !event.IsError || event.Output != output {
				t.Fatalf("tool_end %q = %+v, want error %q", event.ToolCallID, event, output)
			}
		}
	}
	var want []string
	for _, id := range ids {
		want = append(want, "start:"+id, "end:"+id)
	}
	if got := strings.Join(sequence, ","); got != strings.Join(want, ",") {
		t.Fatalf("tool events = %q, want %q", got, strings.Join(want, ","))
	}
}

func TestInvalidToolCallIDsRejectTheWholeBatch(t *testing.T) {
	cases := []struct {
		name         string
		calls        []llm.ToolCall
		wantToolRuns int
		wantRejected bool
	}{
		{
			name: "unique ids run normally",
			calls: []llm.ToolCall{
				toolCall("c1", "read", `{}`), toolCall("c2", "read", `{}`),
			},
			wantToolRuns: 2,
		},
		{
			name: "duplicate ids reject every call",
			calls: []llm.ToolCall{
				toolCall("c1", "read", `{}`), toolCall("c1", "write", `{}`),
			},
			wantRejected: true,
		},
		{
			name: "empty id rejects every call",
			calls: []llm.ToolCall{
				toolCall("c1", "read", `{}`), toolCall("", "read", `{}`),
			},
			wantRejected: true,
		},
		{
			name: "blank id rejects every call",
			calls: []llm.ToolCall{
				toolCall("c1", "read", `{}`), toolCall("   ", "read", `{}`),
			},
			wantRejected: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
			var events []Event
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					return toolReply(test.calls...), nil
				}
				return textReply("done"), nil
			}}
			runner := &Runner{Opts: Options{
				Model: "m", Chatter: chat, Tools: tools,
				Emit: func(e Event) { events = append(events, e) },
			}}

			res, err := runner.Run(context.Background(), "batch")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if tools.count() != test.wantToolRuns {
				t.Fatalf("tool runs = %d, want %d", tools.count(), test.wantToolRuns)
			}
			type result struct{ id, output string }
			var results []result
			for _, msg := range res.Messages {
				if msg.Role == "tool" {
					results = append(results, result{msg.ToolCallID, msg.Content})
				}
			}
			if len(results) != len(test.calls) {
				t.Fatalf("tool results = %d, want %d", len(results), len(test.calls))
			}
			for i, got := range results {
				if got.id != test.calls[i].ID {
					t.Fatalf("result %d id = %q, want %q", i, got.id, test.calls[i].ID)
				}
				if test.wantRejected && got.output != duplicateIDReason {
					t.Fatalf("result %d = %q, want %q", i, got.output, duplicateIDReason)
				}
			}
			if test.wantRejected {
				assertOrderedToolEvents(t, events, []string{test.calls[0].ID, test.calls[1].ID}, duplicateIDReason)
			}
		})
	}
}

// progressTool captures the gated progress emitter from ctx, emits one row
// while it is running, and keeps the emitter for a deliberately late call.
type progressTool struct {
	mu      sync.Mutex
	emitter func(string)
}

func (p *progressTool) Specs() []llm.ToolSpec { return []llm.ToolSpec{toolSpec("stream")} }

func (p *progressTool) Run(ctx context.Context, _, _ string) (string, error) {
	emitter, _ := ctx.Value(toolProgressKey).(func(string))
	p.mu.Lock()
	p.emitter = emitter
	p.mu.Unlock()
	if emitter != nil {
		emitter("during execution")
	}
	return "final output", nil
}

func (p *progressTool) late(text string) {
	p.mu.Lock()
	emitter := p.emitter
	p.mu.Unlock()
	if emitter != nil {
		emitter(text)
	}
}

type rowRecorder struct {
	mu   sync.Mutex
	rows []string
}

func (r *rowRecorder) append(kind string, _ any) error {
	r.mu.Lock()
	r.rows = append(r.rows, kind)
	r.mu.Unlock()
	return nil
}

func (r *rowRecorder) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, row := range r.rows {
		if row == kind {
			count++
		}
	}
	return count
}

func TestLateToolProgressCannotAppendTranscriptRows(t *testing.T) {
	tool := &progressTool{}
	rows := &rowRecorder{}
	var notices int
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "stream", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tool, Record: rows.append,
		Emit: func(e Event) {
			if e.Kind == EventNotice {
				notices++
			}
		},
	}}

	res, err := runner.Run(context.Background(), "stream")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want complete", res.Stopped)
	}
	if got := rows.count("tool_progress"); got != 1 {
		t.Fatalf("progress rows during execution = %d, want 1", got)
	}
	if notices != 1 {
		t.Fatalf("progress notices during execution = %d, want 1", notices)
	}

	// The tool has returned; a leftover goroutine firing the captured callback
	// must now be a no-op.
	tool.late("stale output")
	if got := rows.count("tool_progress"); got != 1 {
		t.Fatalf("progress rows after late callback = %d, want 1", got)
	}
	if notices != 1 {
		t.Fatalf("notices after late callback = %d, want 1", notices)
	}
	for _, msg := range res.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "stale output") {
			t.Fatalf("late update reached the tool result: %q", msg.Content)
		}
	}
}
