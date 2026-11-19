package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// pathTools adds the optional mutation reporter to the shared fake toolset.
type pathTools struct {
	*fakeTools
	fn func(name, args string) []string
}

func (p pathTools) MutationPaths(name, args string) []string {
	if p.fn == nil {
		return nil
	}
	return p.fn(name, args)
}

func TestCheckpointReadyPrecedesMutatingBatch(t *testing.T) {
	sink := &eventSink{}
	tools := pathTools{
		fakeTools: &fakeTools{specs: []llm.ToolSpec{toolSpec("write"), toolSpec("read")}},
		fn: func(name, args string) []string {
			if name == "write" {
				return []string{"src/a.txt", "../escape.txt", "src/a.txt", "/abs.txt"}
			}
			return []string{"src/b.txt"}
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c1", "write", `{"path":"src/a.txt"}`),
				toolCall("c2", "read", `{"path":"src/b.txt"}`),
			), nil
		}
		return textReply("done"), nil
	}}
	var hooks []Checkpoint
	runner := &Runner{Opts: Options{
		Model:         "m",
		Chatter:       chat,
		Tools:         tools,
		MutatingTools: []string{"write"},
		MaxTurns:      2,
		Emit:          sink.emit,
		Checkpoint:    func(cp Checkpoint) { hooks = append(hooks, cp) },
	}}

	if _, err := runner.Run(context.Background(), "change"); err != nil {
		t.Fatalf("run: %v", err)
	}
	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	checkpointAt, firstToolAt := -1, -1
	for i, event := range events {
		if event.Kind == EventCheckpointReady && checkpointAt < 0 {
			checkpointAt = i
		}
		if event.Kind == EventToolStart && firstToolAt < 0 {
			firstToolAt = i
		}
	}
	if checkpointAt < 0 || firstToolAt < 0 || checkpointAt >= firstToolAt {
		t.Fatalf("checkpoint must precede tool starts: checkpoint=%d firstTool=%d kinds=%v", checkpointAt, firstToolAt, sink.kinds())
	}
	cp := events[checkpointAt].Checkpoint
	if cp == nil || cp.Turn != 0 || len(cp.Calls) != 1 || cp.Calls[0].Tool != "write" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	if len(cp.Paths) != 1 || cp.Paths[0] != "src/a.txt" {
		t.Fatalf("paths = %v, want only the in-workspace write path", cp.Paths)
	}
	if len(hooks) != 1 || hooks[0].Turn != cp.Turn || len(hooks[0].Paths) != 1 {
		t.Fatalf("checkpoint hook calls = %+v", hooks)
	}
}

func TestReadOnlyBatchEmitsNoCheckpoint(t *testing.T) {
	sink := &eventSink{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("grep")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`), toolCall("c2", "grep", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2,
		MaxParallel: 2, Emit: sink.emit,
		Checkpoint: func(Checkpoint) { t.Fatal("read-only batch must not ask for a checkpoint") },
	}}
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sink.has(EventCheckpointReady) {
		t.Fatal("read-only batch emitted checkpoint_ready")
	}
}

func TestDedupeNormalizesJSONKeyOrderForRepeatedCalls(t *testing.T) {
	guard := newDedupe(1)
	first := `{"b":2,"a":{"y":1,"x":2}}`
	second := `{ "a" : { "x" : 2, "y" : 1 }, "b" : 2 }`
	if blocked, msg := guard.check("read", first); blocked {
		t.Fatalf("first call blocked: %s", msg)
	}
	if blocked, msg := guard.check("read", second); !blocked || !strings.Contains(msg, "duplicate call blocked") {
		t.Fatalf("re-spelled identical call was not blocked: blocked=%v msg=%q", blocked, msg)
	}
	if blocked, _ := guard.check("read", `{"path":"文档.txt"}`); blocked {
		t.Fatal("different unicode arguments must not be blocked")
	}
}

func TestRepeatGuardForcesSequentialBatch(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{"path":"a"}`), toolCall("c2", "read", `{"path":"a"}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2,
		MaxParallel: 4, MaxRepeats: 1,
	}}
	res, err := runner.Run(context.Background(), "read twice")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if tools.count() != 1 {
		t.Fatalf("real calls = %d, want 1", tools.count())
	}
	if got := lastToolContent(res.Messages); !strings.Contains(got, "duplicate call blocked") {
		t.Fatalf("blocked result = %q", got)
	}
}

func TestParallelBatchRespectsWorkerLimit(t *testing.T) {
	var active, peak atomic.Int32
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(_ context.Context, _ string, _ string) (string, error) {
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return "ok", nil
	}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c1", "read", `{}`), toolCall("c2", "read", `{"i":2}`),
				toolCall("c3", "read", `{"i":3}`), toolCall("c4", "read", `{"i":4}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2, MaxParallel: 2,
	}}
	if _, err := runner.Run(context.Background(), "batch"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrent tools = %d, want at most 2", got)
	}
	if tools.count() != 4 {
		t.Fatalf("tool calls = %d, want 4", tools.count())
	}
}

func TestCancellationInsideToolBatchSkipsRemainingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &eventSink{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(_ context.Context, name, _ string) (string, error) {
		if name == "first" {
			cancel()
		}
		return name + " done", nil
	}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "first", `{}`), toolCall("c2", "second", `{}`)), nil
		}
		return textReply("unreachable"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2, Emit: sink.emit}}
	res, err := runner.Run(ctx, "batch")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Stopped != StopCancelled {
		t.Fatalf("stopped = %q", res.Stopped)
	}
	if names := tools.names(); len(names) != 1 || names[0] != "first" {
		t.Fatalf("tool calls = %v, want only first", names)
	}
	if got := lastToolContent(res.Messages); !strings.Contains(got, "cancelled") {
		t.Fatalf("unrun call result = %q", got)
	}
	if !sink.has(EventToolEnd) {
		t.Fatal("cancelled batch has no tool_end events")
	}
}

func TestToolFailureIsVisibleToModelAndRecorded(t *testing.T) {
	sink := &eventSink{}
	tools := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", errors.New("boom: not found")
	}}
	type record struct {
		kind    string
		payload map[string]string
	}
	var records []record
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2, Emit: sink.emit,
		Record: RecorderFunc(func(kind string, payload any) error {
			fields, _ := payload.(map[string]string)
			records = append(records, record{kind: kind, payload: fields})
			return nil
		}),
	}}
	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := lastToolContent(res.Messages); !strings.Contains(got, "error: boom: not found") {
		t.Fatalf("model-visible tool result = %q", got)
	}
	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	found := false
	for _, event := range events {
		if event.Kind == EventToolEnd && event.IsError && strings.Contains(event.Output, "boom") {
			found = true
		}
	}
	if !found {
		t.Fatal("no error tool_end event for a failed tool")
	}
	found = false
	for _, r := range records {
		if r.kind == "tool" && r.payload["is_error"] == "true" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failure was not recorded: %+v", records)
	}
}

func TestCollectCompactionEvidenceKeepsFailuresAndNextSteps(t *testing.T) {
	events := []Event{
		{Kind: EventToolEnd, Turn: 1, ToolName: "write", IsError: true, Output: "disk\nfull"},
		{Kind: EventError, Turn: 2, Err: "provider timeout"},
	}
	messages := []llm.Message{
		{Role: "assistant", Content: "Next: restore the checkpoint\n普通说明\n下一步: 重新运行测试"},
	}
	got := CollectCompactionEvidence(events, messages)
	if len(got.Failures) != 2 {
		t.Fatalf("failures = %+v", got.Failures)
	}
	if got.Failures[0].Tool != "write" || strings.Contains(got.Failures[0].Reason, "\n") {
		t.Fatalf("tool failure = %+v", got.Failures[0])
	}
	if !got.Failures[1].Error || got.Failures[1].Reason != "provider timeout" {
		t.Fatalf("provider failure = %+v", got.Failures[1])
	}
	if len(got.NextSteps) != 2 || got.NextSteps[0] != "Next: restore the checkpoint" || got.NextSteps[1] != "下一步: 重新运行测试" {
		t.Fatalf("next steps = %v", got.NextSteps)
	}
	long := strings.Repeat("很", 300)
	got = CollectCompactionEvidence(nil, []llm.Message{{Role: "assistant", Content: "next: " + long}})
	if len(got.NextSteps) != 1 || !strings.HasSuffix(got.NextSteps[0], "...") {
		t.Fatalf("long next step was not clipped: %q", got.NextSteps)
	}
}

func TestCompactionEvidenceHookReceivesRunFailures(t *testing.T) {
	tools := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", errors.New("write failed")
	}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "write", `{}`)), nil
		}
		return textReply("next: inspect the disk"), nil
	}}
	var got CompactionEvidence
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2,
		OnCompactionEvidence: func(e CompactionEvidence) { got = e },
	}}
	if _, err := runner.Run(context.Background(), "write"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got.Failures) != 1 || got.Failures[0].Tool != "write" {
		t.Fatalf("failures = %+v", got.Failures)
	}
	if len(got.NextSteps) != 1 {
		t.Fatalf("next steps = %v", got.NextSteps)
	}
}

func TestCancellationDuringChatterStopsWithinTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &fakeTools{}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		cancel()
		return llm.Message{}, ctx.Err()
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 5}}
	res, err := runner.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) || res.Stopped != StopCancelled {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if res.Turns != 0 || tools.count() != 0 || chat.count() != 1 {
		t.Fatalf("turns=%d tools=%d chatter=%d", res.Turns, tools.count(), chat.count())
	}
}

func TestSteeringQueuedDuringToolsWaitsForTurnBoundary(t *testing.T) {
	steer := make(chan string, 2)
	var once sync.Once
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}, run: func(context.Context, string, string) (string, error) {
		once.Do(func() { steer <- "change direction" })
		return "ok", nil
	}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`), toolCall("c2", "read", `{"i":2}`)), nil
		}
		toolOne, toolTwo, correction := -1, -1, -1
		for i, msg := range messages {
			switch {
			case msg.Role == "tool" && msg.ToolCallID == "c1":
				toolOne = i
			case msg.Role == "tool" && msg.ToolCallID == "c2":
				toolTwo = i
			case msg.Role == "user" && msg.Content == "change direction":
				correction = i
			}
		}
		if toolOne < 0 || toolTwo < 0 || correction < toolTwo {
			t.Fatalf("steering entered the tool batch: one=%d two=%d steer=%d", toolOne, toolTwo, correction)
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2, Steer: steer}}
	res, err := runner.Run(context.Background(), "inspect")
	if err != nil || res.Text != "done" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestSteeringValuesDrainInOrder(t *testing.T) {
	steer := make(chan string, 2)
	steer <- "first correction"
	steer <- "second correction"
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		var users []string
		for _, msg := range messages {
			if msg.Role == "user" && msg.Content != "original" {
				users = append(users, msg.Content)
			}
		}
		if len(users) != 2 || users[0] != "first correction" || users[1] != "second correction" {
			t.Fatalf("steering order = %v", users)
		}
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}, Steer: steer}}
	if _, err := runner.Run(context.Background(), "original"); err != nil {
		t.Fatalf("run: %v", err)
	}
}
