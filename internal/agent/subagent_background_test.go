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

// fakeChildRunner 是供后台测试使用的可注入 ChildRunner。它记录运行的子级数量及其峰值并发，
// 并可以在 Run 中阻塞直到被释放，使测试能确定性地观察「先确认、后出结果」的契约。
// （接上行）
// （接上行）
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

// bgTaskTools 构建一个为后台委派接好线的 taskTools，使用伪子运行器且无模型。
// （接上行）
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

// waitForResult 轮询有界队列直到有结果可用。
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

// waitForNotice 等待包含 substr 的父级 EventNotice。它锁定接收器，使后台 goroutine 仍在发出时也是安全的。
// （接上行）
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

// TestBackgroundTaskSurvivesCallerCancel 守护后台生命周期修复：模型结束当前提示时
// 调用方 context 被取消，但后台子级必须继续跑完并把结果投递给自己所属的队列。
func TestBackgroundTaskSurvivesCallerCancel(t *testing.T) {
	release := make(chan struct{})
	child := &fakeChildRunner{
		started: make(chan string, 1),
		release: release,
		answer:  "survived caller cancel",
	}
	sink := &eventSink{}
	bg := NewBackgroundResults(0, 4)
	tt := bgTaskTools(bg, child, sink.emit, 0, 2)

	ctx, cancel := context.WithCancel(context.Background())
	ack, err := tt.Run(ctx, TaskToolName, backgroundTaskArgs("look", "find it"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(ack, "started in background") {
		t.Fatalf("ack: %q", ack)
	}
	<-child.started // 确认后台 goroutine 已进入子级 Run 并持有 context
	cancel()        // 模拟提示结束：调用方 context 被取消
	close(release)  // 放行子级；若仍使用旧 ctx，此处会立刻返回 ctx.Err

	got := waitForResult(t, bg)
	if !strings.Contains(got, "survived caller cancel") {
		t.Fatalf("后台结果未投递（可能仍继承被取消的 ctx）: %q", got)
	}
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

	// 子级已启动但仍被阻塞：还没有结果入队。
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
	// 确认是立即返回的，因此要等到两个子级都真正运行（并阻塞在释放上）之后再检查峰值并发。
	// （接上行）
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
	// 重试直到两个完成的子级释放了槽位。
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
	// 在深度上限处，委派工具会被隐藏，因此后台子级不能再派生另一个后台子级。
	// （接上行）
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

func TestBackgroundResultsDrainForOwner(t *testing.T) {
	bg := NewBackgroundResults(0, 2)
	root := bg.RootOwner()
	child := bg.nextID()
	sibling := bg.nextID()
	bg.PushFor(root, 0, "root-1")
	bg.PushFor(child, 1, "child-1")
	bg.PushFor(root, 0, "root-2")
	// 同级子级共享深度 1，但必须彼此独立。
	bg.PushFor(sibling, 1, "sibling-1")

	got := bg.DrainFor(root)
	if len(got) != 2 || got[0] != "root-1" || got[1] != "root-2" {
		t.Fatalf("DrainFor(root) = %v, want [root-1 root-2]", got)
	}
	if got := bg.DrainFor(child); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("DrainFor(child) = %v, want [child-1]", got)
	}
	if got := bg.DrainFor(sibling); len(got) != 1 || got[0] != "sibling-1" {
		t.Fatalf("DrainFor(sibling) = %v, want [sibling-1]", got)
	}
	if bg.Len() != 0 {
		t.Fatalf("queue Len = %d, want 0 after all owners drained", bg.Len())
	}
}

func TestRootOwnerIsStablePerQueue(t *testing.T) {
	a := NewBackgroundResults(0, 2)
	b := NewBackgroundResults(0, 2)
	want := a.RootOwner()
	if want == 0 {
		t.Fatal("RootOwner must not be zero (zero means unassigned)")
	}
	if got := a.RootOwner(); got != want {
		t.Fatalf("second RootOwner = %d, want stable %d", got, want)
	}
	// 不同的队列有自己的计数器，可以合法地重用相同的数字 id：队列按身份隔离，而非按 owner 值。
	// （接上行）
	if got := b.RootOwner(); got == 0 || b.RootOwner() != got {
		t.Fatalf("second queue's RootOwner = %d, want a stable nonzero id", got)
	}
}

func TestNewRunStateOwnsQueueOwnerChildrenGetDistinctOwners(t *testing.T) {
	bg := NewBackgroundResults(0, 4)
	opts := Options{Subagent: &SubagentConfig{Background: bg}}

	root := newRunState(opts, "root", nil)
	if want := bg.RootOwner(); root.bgOwner != want {
		t.Fatalf("root bgOwner = %d, want queue owner %d", root.bgOwner, want)
	}
	// 重试提示或已恢复的 Runner 复用同一个队列，因此也复用同一个 owner，使迟到的结果仍被投递。
	// （接上行）
	if again := newRunState(opts, "retry", nil); again.bgOwner != root.bgOwner {
		t.Fatalf("retry bgOwner = %d, want %d", again.bgOwner, root.bgOwner)
	}
	// 不同的队列意味着不同的根运行；它从该队列解析自己的 owner（数值可能相同，但队列绝不混用）。
	// （接上行）
	other := NewBackgroundResults(0, 4)
	otherState := newRunState(Options{Subagent: &SubagentConfig{Background: other}}, "other", nil)
	if otherState.bgOwner != other.RootOwner() {
		t.Fatalf("other run bgOwner = %d, want its queue's owner %d", otherState.bgOwner, other.RootOwner())
	}

	// 相同深度的同级子级各自获得自己的身份。
	tt := &taskTools{parent: opts, depth: 0, cfg: &SubagentConfig{Background: bg}}
	childA := tt.childOptions("a", "")
	childB := tt.childOptions("b", "")
	if childA.Depth != 1 || childB.Depth != 1 {
		t.Fatalf("child depths = (%d, %d), want 1", childA.Depth, childB.Depth)
	}
	if childA.bgOwner == 0 || childB.bgOwner == 0 || childA.bgOwner == childB.bgOwner {
		t.Fatalf("sibling owners = (%d, %d), want two distinct nonzero ids", childA.bgOwner, childB.bgOwner)
	}
}

func TestSiblingBackgroundTasksNeverStealEachOthersResults(t *testing.T) {
	bg := NewBackgroundResults(0, 4)
	ownerA := bg.nextID()
	ownerB := bg.nextID()
	siblingTT := func(owner uint64, child ChildRunner) *taskTools {
		return &taskTools{
			inner:  nopTools{},
			parent: Options{Emit: func(Event) {}},
			depth:  1, // 两个同级子级在相同深度运行
			owner:  owner,
			cfg: &SubagentConfig{
				MaxDepth:   2,
				Background: bg,
				NewChild:   func(Options) ChildRunner { return child },
			},
		}
	}
	for _, tc := range []struct {
		owner uint64
		child *fakeChildRunner
	}{
		{ownerA, &fakeChildRunner{answer: "answer from A"}},
		{ownerB, &fakeChildRunner{answer: "answer from B"}},
	} {
		if _, err := siblingTT(tc.owner, tc.child).Run(context.Background(), TaskToolName, backgroundTaskArgs("s", "p")); err != nil {
			t.Fatalf("background task for owner %d: %v", tc.owner, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for bg.Len() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bg.Len() != 2 {
		t.Fatalf("queued results = %d, want 2", bg.Len())
	}

	gotA := bg.DrainFor(ownerA)
	if len(gotA) != 1 || !strings.Contains(gotA[0], "answer from A") {
		t.Fatalf("owner A drain = %v, want only A's answer", gotA)
	}
	gotB := bg.DrainFor(ownerB)
	if len(gotB) != 1 || !strings.Contains(gotB[0], "answer from B") {
		t.Fatalf("owner B drain = %v, want only B's answer", gotB)
	}
}

func TestInjectSteeringDrainsOnlyThisRunOwner(t *testing.T) {
	bg := NewBackgroundResults(0, 4)
	ownerA := bg.RootOwner()
	ownerB := bg.nextID()
	bg.PushFor(ownerA, 0, "for A")
	bg.PushFor(ownerB, 0, "for B")

	state := &runState{
		opts:    Options{Subagent: &SubagentConfig{Background: bg}},
		bgOwner: ownerA,
		steer:   &steering{},
	}
	if !state.injectSteering() {
		t.Fatal("injectSteering did not report the queued result")
	}
	if len(state.messages) != 1 || state.messages[0].Content != "for A" {
		t.Fatalf("injected messages = %+v, want only [for A]", state.messages)
	}
	if got := bg.DrainFor(ownerB); len(got) != 1 || got[0] != "for B" {
		t.Fatalf("run A stole run B's result: drain = %v", got)
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
	// 消息数量上限。
	byCount := &TaskBoard{}
	for i := 0; i < BoardMaxMessages; i++ {
		if _, err := byCount.Post("a", "b", BoardKindInfo, "x"); err != nil {
			t.Fatalf("Post %d: %v", i, err)
		}
	}
	if _, err := byCount.Post("a", "b", BoardKindInfo, "x"); !errors.Is(err, ErrBoardFull) {
		t.Fatalf("Post beyond message count err = %v, want ErrBoardFull", err)
	}

	// 总字节上限：恰好 2 MiB 的消息体能放下，再多一个就不行。
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
