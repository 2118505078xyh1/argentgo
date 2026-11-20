package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestParallelBatchTracksBreakerInCallOrder checks that the invalid-argument
// breaker's final state is decided by the order of the tool calls, not by the
// order in which workers happened to finish. The success call is first in the
// batch but deliberately finishes last: replaying outcomes in completion order
// would let it clear the streak built by the three trailing invalid-argument
// failures, so the run would miss a stop that the same batch in sequential
// order always produces.
func TestParallelBatchTracksBreakerInCallOrder(t *testing.T) {
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(_ context.Context, _ string, args string) (string, error) {
			if strings.Contains(args, `"n":0`) {
				// First call, last to finish: a success clears the streak.
				time.Sleep(80 * time.Millisecond)
				return "ok", nil
			}
			return "", errors.New("bad arguments")
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c0", "read", `{"n":0}`),
				toolCall("c1", "read", `{"n":1}`),
				toolCall("c2", "read", `{"n":2}`),
				toolCall("c3", "read", `{"n":3}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:       "m",
		Chatter:     chat,
		Tools:       tools,
		MaxTurns:    3,
		MaxParallel: 4,
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != stopInvalidArguments {
		t.Fatalf("stopped = %q, want %q (call-order outcomes must trip the breaker)", res.Stopped, stopInvalidArguments)
	}
}

// TestParallelBatchTracksMistakesInCallOrder is the same contract for the
// consecutive-mistake tracker: a success that finishes after three failures
// must not erase the streak the call order builds.
func TestParallelBatchTracksMistakesInCallOrder(t *testing.T) {
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(_ context.Context, _ string, args string) (string, error) {
			if strings.Contains(args, `"n":0`) {
				time.Sleep(80 * time.Millisecond)
				return "ok", nil
			}
			return "", errors.New("boom")
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c0", "read", `{"n":0}`),
				toolCall("c1", "read", `{"n":1}`),
				toolCall("c2", "read", `{"n":2}`),
				toolCall("c3", "read", `{"n":3}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:       "m",
		Chatter:     chat,
		Tools:       tools,
		MaxTurns:    3,
		MaxParallel: 4,
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The mistake tracker has recovery guidance, so it never hard-stops; the
	// streak must still reach the limit and inject a guidance turn, which the
	// second request sees as a user message.
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	seen := chat.requestsSeen()
	if len(seen) < 2 {
		t.Fatalf("requests = %d, want a second turn with mistake guidance", len(seen))
	}
	found := false
	for _, msg := range seen[1] {
		if msg.Role == "user" && strings.Contains(msg.Content, "连续出错") {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request lacks mistake guidance; call-order outcomes were lost")
	}
}
