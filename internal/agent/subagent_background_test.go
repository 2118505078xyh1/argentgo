package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeChildRunner is an injectable ChildRunner for the background tests. It
// records how many children run and their peak concurrency, and can block in
// Run until released so a test can observe the "ack first, result later"
// contract deterministically.
type fakeChildRunner struct {
	started chan string
	release chan struct{}
	answer  string
	err     error

	active atomic.Int64
	max    atomic.Int64
	calls  atomic.Int64
}

func (f *fakeChildRunner) Run(ctx context.Context, prompt string) (Result, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	for {
		peak := f.max.Load()
		if n <= peak || f.max.CompareAndSwap(peak, n) {
			break
		}
	}
	defer f.active.Add(-1)
	if f.started != nil {
		f.started <- prompt
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if f.err != nil {
		return Result{}, f.err
	}
	return Result{Text: f.answer}, nil
}

// bgTaskTools builds a taskTools wired for background delegation with a fake
// child runner and no model.
func bgTaskTools(bg *BackgroundResults, child ChildRunner, emit func(Event), depth, maxDepth int) *taskTools {
	return &taskTools{
		inner:  nopTools{},
		parent: Options{Emit: emit},
		depth:  depth,
		cfg: &SubagentConfig{
			MaxDepth:   maxDepth,
			Background: bg,
			NewChild:   func(Options) ChildRunner { return child },
		},
	}
}

func backgroundTaskArgs(description, prompt string) string {
	b, _ := json.Marshal(taskArgs{Description: description, Prompt: prompt, Background: true})
	return string(b)
}

// waitForResult polls the bounded queue until a result is available.
func waitForResult(t *testing.T, bg *BackgroundResults) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := bg.Drain(); len(got) > 0 {
			return got[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for a background result")
	return ""
}

// waitForNotice waits for a parent EventNotice containing substr. It locks the
// sink so it is safe while a background goroutine is still emitting.
func waitForNotice(t *testing.T, sink *eventSink, substr string) Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		for _, event := range sink.events {
			if event.Kind == EventNotice && strings.Contains(event.Text, substr) {
				sink.mu.Unlock()
				return event
			}
		}
		sink.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no EventNotice containing %q; kinds = %v", substr, sink.kinds())
	return Event{}
}

func TestBackgroundTaskAcksImmediatelyAndQueuesResult(t *testing.T) {
	release := make(chan struct{})
	child := &fakeChildRunner{
		started: make(chan string, 1),
		release: release,
		answer:  "bg answer 42",
	}
	sink := &eventSink{}
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, child, sink.emit, 0, 2)

	ack, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("look", "find it"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(ack, "task 1 started in background") || !strings.Contains(ack, "result will be injected") {
		t.Fatalf("ack = %q, want an immediate background ack with an id", ack)
	}

	// The child has started but is still blocked: no result is queued yet.
	select {
	case <-child.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background child did not start")
	}
	if got := bg.Len(); got != 0 {
		t.Fatalf("queued results before finish = %d, want 0", got)
	}

	close(release)
	result := waitForResult(t, bg)
	if !strings.Contains(result, BackgroundResultPrefix) || !strings.Contains(result, "bg answer 42") {
		t.Fatalf("result = %q, want the prefixed child text", result)
	}
	notice := waitForNotice(t, sink, BackgroundResultPrefix)
	if notice.Depth != 0 {
		t.Fatalf("notice depth = %d, want 0", notice.Depth)
	}
	if child.calls.Load() != 1 {
		t.Fatalf("child calls = %d, want 1", child.calls.Load())
	}
}

func TestBackgroundTaskConcurrentCap(t *testing.T) {
	release := make(chan struct{})
	child := &fakeChildRunner{started: make(chan string, 8), release: release, answer: "ok"}
	bg := NewBackgroundResults(0, 2)
	tt := bgTaskTools(bg, child, (&eventSink{}).emit, 0, 2)

	for i := 0; i < 2; i++ {
		if _, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("t", "p")); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	// The ack is immediate, so wait until both children are actually running
	// (and blocked on release) before checking the peak concurrency.
	for i := 0; i < 2; i++ {
		select {
		case <-child.started:
		case <-time.After(2 * time.Second):
			t.Fatal("background child did not start")
		}
	}
	if _, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("t", "p")); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("third background Run err = %v, want a concurrency-limit error", err)
	}
	if got := child.max.Load(); got != 2 {
		t.Fatalf("max concurrent children = %d, want 2", got)
	}

	close(release)
	// Retry until a slot is freed by the two finishing children.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("t", "p"))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a background slot never freed: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBackgroundTaskRespectsMaxDepth(t *testing.T) {
	child := &fakeChildRunner{answer: "ok"}
	sink := &eventSink{}
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, child, sink.emit, 2, 2)

	_, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("deep", "p"))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("err = %v, want a depth-limit error", err)
	}
	if child.calls.Load() != 0 {
		t.Fatalf("child calls = %d, want 0 (no goroutine may start)", child.calls.Load())
	}
	if len(sink.events) != 0 {
		t.Fatalf("events = %v, want none when the depth guard rejects the call", sink.kinds())
	}
}

func TestBackgroundChildCannotSpawnUnboundedChildren(t *testing.T) {
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, &fakeChildRunner{answer: "ok"}, (&eventSink{}).emit, 1, 2)

	childOpts := tt.childOptions("label", "")
	if childOpts.Depth != 2 {
		t.Fatalf("child depth = %d, want 2", childOpts.Depth)
	}
	// At the depth limit the delegate tool is withheld, so a background child
	// cannot spawn another background child.
	for _, spec := range newToolset(nopTools{}, childOpts).Specs() {
		if spec.Function.Name == TaskToolName {
			t.Fatal("task tool must not be advertised at the depth limit")
		}
	}
}

func TestBackgroundResultsQueueDropsOldestAtCap(t *testing.T) {
	bg := NewBackgroundResults(3, 2)
	for i := 1; i <= 5; i++ {
		bg.Push(fmt.Sprintf("r%d", i))
	}
	got := bg.Drain()
	want := []string{"r3", "r4", "r5"}
	if len(got) != len(want) {
		t.Fatalf("Drain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Drain = %v, want %v", got, want)
		}
	}
	if bg.Dropped() != 2 {
		t.Fatalf("Dropped = %d, want 2", bg.Dropped())
	}
	if bg.Len() != 0 || bg.Drain() != nil {
		t.Fatal("queue must be empty after Drain")
	}
}

func TestBackgroundResultsDrainAtDepth(t *testing.T) {
	bg := NewBackgroundResults(0, 2)
	bg.PushAt(0, "root-1")
	bg.PushAt(1, "child-1")
	bg.PushAt(0, "root-2")

	root := bg.DrainAt(0)
	if len(root) != 2 || root[0] != "root-1" || root[1] != "root-2" {
		t.Fatalf("DrainAt(0) = %v, want [root-1 root-2]", root)
	}
	child := bg.DrainAt(1)
	if len(child) != 1 || child[0] != "child-1" {
		t.Fatalf("DrainAt(1) = %v, want [child-1]", child)
	}
}

func TestBackgroundTaskFailureStillNotifiesParent(t *testing.T) {
	child := &fakeChildRunner{err: errors.New("boom")}
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, child, (&eventSink{}).emit, 0, 2)

	if _, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("do", "p")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	result := waitForResult(t, bg)
	if !strings.Contains(result, "failed") || !strings.Contains(result, "boom") {
		t.Fatalf("result = %q, want a failure notice with the error", result)
	}
}

func TestBackgroundTaskRequiresConfiguredQueue(t *testing.T) {
	tt := &taskTools{
		inner:  nopTools{},
		parent: Options{Emit: (&eventSink{}).emit},
		cfg:    &SubagentConfig{MaxDepth: 2},
	}
	if _, err := tt.Run(context.Background(), TaskToolName, backgroundTaskArgs("do", "p")); err == nil || !strings.Contains(err.Error(), "Background") {
		t.Fatalf("err = %v, want an error about the missing queue", err)
	}
}

func TestBackgroundFlagFalseKeepsSynchronousPath(t *testing.T) {
	child := &fakeChildRunner{answer: "sync answer"}
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, child, (&eventSink{}).emit, 0, 2)

	args, _ := json.Marshal(taskArgs{Description: "do", Prompt: "p", Background: false})
	out, err := tt.Run(context.Background(), TaskToolName, string(args))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "sync answer" {
		t.Fatalf("out = %q, want the child's text returned synchronously", out)
	}
	if child.calls.Load() != 1 {
		t.Fatalf("child calls = %d, want 1", child.calls.Load())
	}
	if bg.Len() != 0 {
		t.Fatal("synchronous task must not queue a background result")
	}
}

func TestTaskToolSpecAdvertisesBackgroundField(t *testing.T) {
	spec := TaskToolSpec()
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	field, ok := schema.Properties["background"]
	if !ok || field.Type != "boolean" {
		t.Fatalf("background property = %+v (present=%v), want a boolean", field, ok)
	}
	for _, key := range schema.Required {
		if key == "background" {
			t.Fatal("background must stay optional (not in required)")
		}
	}
}

func TestTaskBoardPostReadAndSinceFilter(t *testing.T) {
	board := &TaskBoard{}
	seq1, err := board.Post("alice", "bob", BoardKindInfo, "m1")
	if err != nil || seq1 != 1 {
		t.Fatalf("Post 1 = (%d, %v), want (1, nil)", seq1, err)
	}
	seq2, err := board.Post("bob", "alice", BoardKindAsk, "m2")
	if err != nil || seq2 != 2 {
		t.Fatalf("Post 2 = (%d, %v), want (2, nil)", seq2, err)
	}
	seq3, err := board.Post("carol", "bob", BoardKindResult, "m3")
	if err != nil || seq3 != 3 {
		t.Fatalf("Post 3 = (%d, %v), want (3, nil)", seq3, err)
	}

	all := board.Read(0, "")
	if len(all) != 3 || all[0].Seq != seq1 || all[2].Seq != seq3 {
		t.Fatalf("Read(all) = %+v, want 3 ordered messages", all)
	}
	if all[1].Kind != BoardKindAsk || all[1].From != "bob" {
		t.Fatalf("message 2 = %+v, want bob's ask", all[1])
	}
	toBob := board.Read(0, "bob")
	if len(toBob) != 2 || toBob[0].Seq != seq1 || toBob[1].Seq != seq3 {
		t.Fatalf("Read(to bob) = %+v, want messages 1 and 3", toBob)
	}
	since := board.Read(seq1, "bob")
	if len(since) != 1 || since[0].Seq != seq3 {
		t.Fatalf("Read(since 1, bob) = %+v, want only message 3", since)
	}
	if later := board.Read(seq3, ""); len(later) != 0 {
		t.Fatalf("Read(since last) = %+v, want none", later)
	}
}

func TestTaskBoardRejectsInvalidPosts(t *testing.T) {
	board := &TaskBoard{}
	cases := []struct {
		name string
		from string
		to   string
		kind string
		body string
		want error
	}{
		{"self", "a", "a", BoardKindInfo, "x", ErrBoardSelfPost},
		{"blank self", "  ", "  ", BoardKindInfo, "x", ErrBoardSelfPost},
		{"bad kind", "a", "b", "gossip", "x", ErrBoardInvalidKind},
		{"oversize", "a", "b", BoardKindInfo, strings.Repeat("x", BoardMaxBodyBytes+1), ErrBoardMessageTooBig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := board.Post(tc.from, tc.to, tc.kind, tc.body); !errors.Is(err, tc.want) {
				t.Fatalf("Post err = %v, want %v", err, tc.want)
			}
		})
	}
	if got := board.Read(0, ""); len(got) != 0 {
		t.Fatalf("board has %d messages after rejected posts, want 0", len(got))
	}
}

func TestTaskBoardCapacityLimits(t *testing.T) {
	// The message-count limit.
	byCount := &TaskBoard{}
	for i := 0; i < BoardMaxMessages; i++ {
		if _, err := byCount.Post("a", "b", BoardKindInfo, "x"); err != nil {
			t.Fatalf("Post %d: %v", i, err)
		}
	}
	if _, err := byCount.Post("a", "b", BoardKindInfo, "x"); !errors.Is(err, ErrBoardFull) {
		t.Fatalf("Post beyond message count err = %v, want ErrBoardFull", err)
	}

	// The total-bytes limit: exactly 2 MiB of bodies fits, one more does not.
	byBytes := &TaskBoard{}
	body := strings.Repeat("x", BoardMaxBodyBytes)
	for i := 0; i < BoardMaxTotalBytes/BoardMaxBodyBytes; i++ {
		if _, err := byBytes.Post("a", "b", BoardKindResult, body); err != nil {
			t.Fatalf("Post %d: %v", i, err)
		}
	}
	if _, err := byBytes.Post("a", "b", BoardKindResult, body); !errors.Is(err, ErrBoardFull) {
		t.Fatalf("Post beyond total bytes err = %v, want ErrBoardFull", err)
	}
}

var _ TaskBoardIO = (*TaskBoard)(nil)
var _ ChildRunner = (*Runner)(nil)
