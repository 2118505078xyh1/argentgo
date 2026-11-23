package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// recordingChild 记录构建其子级时的 Options，并返回固定答案。
type recordingChild struct {
	res  Result
	err  error
	got  []Options
	seen int
}

func (c *recordingChild) Run(_ context.Context, prompt string) (Result, error) {
	c.seen++
	return c.res, c.err
}

func newRecordingChildFactory(c *recordingChild) func(Options) ChildRunner {
	return func(opts Options) ChildRunner {
		c.got = append(c.got, opts)
		return c
	}
}

// TestTaskResumeById 锁定 #55：按 id 找回记录后续跑，并把追问原样交给回调；
// id 不存在或宿主未配置回调时返回可读错误，绝不从头重跑。
func TestTaskResumeById(t *testing.T) {
	ctx := context.Background()
	var notices []Event
	emit := func(e Event) { notices = append(notices, e) }

	var gotRecord SubagentRecord
	var gotFollow string
	child := &recordingChild{}
	tools := &taskTools{
		inner:  nopTools{},
		parent: Options{Emit: emit},
		depth:  0,
		cfg: &SubagentConfig{
			MaxDepth: 2,
			NewChild: newRecordingChildFactory(child),
			LookupTask: func(id string) (SubagentRecord, bool) {
				if id != "task-42" {
					return SubagentRecord{}, false
				}
				return SubagentRecord{ID: id, Label: "old task", Prompt: "original", Tail: "done"}, true
			},
			ResumeTask: func(_ context.Context, record SubagentRecord, followUp string) (Result, error) {
				gotRecord, gotFollow = record, followUp
				return Result{Text: "resumed answer"}, nil
			},
		},
	}

	raw, _ := json.Marshal(taskArgs{Description: "follow-up", Prompt: "now also check tests", Resume: "task-42"})
	out, err := tools.Run(ctx, TaskToolName, string(raw))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if out != "resumed answer" {
		t.Fatalf("output = %q", out)
	}
	if gotRecord.ID != "task-42" || gotFollow != "now also check tests" {
		t.Fatalf("resume args = %+v / %q", gotRecord, gotFollow)
	}
	if child.seen != 0 {
		t.Fatal("resume must not spawn a fresh child")
	}
	if len(notices) < 2 || notices[0].Kind != EventSubagentStart || notices[len(notices)-1].Kind != EventSubagentEnd {
		t.Fatalf("events = %+v", notices)
	}

	// 找不到的 id 返回可读错误。
	bad, _ := json.Marshal(taskArgs{Prompt: "x", Resume: "missing"})
	if _, err := tools.Run(ctx, TaskToolName, string(bad)); err == nil || !strings.Contains(err.Error(), "resume not found") {
		t.Fatalf("want resume-not-found error, got %v", err)
	}
	// 未配置回调时同样可读，不 panic。
	tools.cfg.ResumeTask = nil
	if _, err := tools.Run(ctx, TaskToolName, string(raw)); err == nil || !strings.Contains(err.Error(), "configure") {
		t.Fatalf("want configuration error, got %v", err)
	}
}

// TestTaskWorktreeIsolationFallback 锁定 #56：默认 worktree，隔离成功时子级 root
// 指向 worktree 且结果附路径；无 git/无提交时降级 shared 并附说明，绝不中断任务。
func TestTaskWorktreeIsolationFallback(t *testing.T) {
	ctx := context.Background()
	var events []Event
	emit := func(e Event) { events = append(events, e) }
	child := &recordingChild{res: Result{Text: "done"}}

	newTools := func(isolate IsolateTaskFunc) *taskTools {
		return &taskTools{
			inner:  nopTools{},
			parent: Options{Root: "/shared", Emit: emit},
			depth:  0,
			cfg: &SubagentConfig{
				MaxDepth:    2,
				NewChild:    newRecordingChildFactory(child),
				IsolateTask: isolate,
			},
		}
	}

	// 成功路径：worktree 目录作为子级 root，结果附路径。
	tools := newTools(func(_ context.Context, _ string) (string, string, error) {
		return "/shared/.argentgo-worktrees/task-1", "", nil
	})
	raw, _ := json.Marshal(taskArgs{Description: "isolated", Prompt: "do work"})
	out, err := tools.Run(ctx, TaskToolName, string(raw))
	if err != nil {
		t.Fatalf("isolated run: %v", err)
	}
	if len(child.got) == 0 || child.got[len(child.got)-1].Root != "/shared/.argentgo-worktrees/task-1" {
		t.Fatalf("child root = %+v", child.got)
	}
	if !strings.Contains(out, "worktree at /shared/.argentgo-worktrees/task-1") {
		t.Fatalf("output missing worktree path: %q", out)
	}

	// 降级路径：dir 为空时保留共享 root，并发出说明事件。
	events = nil
	child.got = nil
	tools = newTools(func(_ context.Context, _ string) (string, string, error) {
		return "", "no git repository; using shared workspace", errors.New("not a repo")
	})
	out, err = tools.Run(ctx, TaskToolName, string(raw))
	if err != nil {
		t.Fatalf("fallback run must succeed: %v", err)
	}
	if len(child.got) == 0 || child.got[len(child.got)-1].Root != "/shared" {
		t.Fatalf("fallback child root = %+v", child.got)
	}
	foundNote := false
	for _, e := range events {
		if e.Kind == EventNotice && strings.Contains(e.Text, "shared workspace") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("fallback note missing: %+v", events)
	}
	if !strings.Contains(out, "shared workspace") {
		t.Fatalf("fallback output missing note: %q", out)
	}

	// 显式 shared 时不调用隔离回调。
	called := false
	tools = newTools(func(_ context.Context, _ string) (string, string, error) {
		called = true
		return "/should/not", "", nil
	})
	shared, _ := json.Marshal(taskArgs{Description: "plain", Prompt: "x", Isolation: "shared"})
	if _, err := tools.Run(ctx, TaskToolName, string(shared)); err != nil {
		t.Fatalf("shared run: %v", err)
	}
	if called {
		t.Fatal("explicit shared must not call isolate hook")
	}
}

// TestGoalLoopBounded 锁定 #46：上限生效、未完成时把 missing 注入下一轮、
// 判官判定 complete 时立即结束、任何用户输入（Cancel 关闭）立即中断并发 interrupted 状态。
func TestGoalLoopBounded(t *testing.T) {
	ctx := context.Background()
	var runs []string
	var statuses []GoalStatus
	child := &recordingChild{res: Result{Text: "work"}}
	runner := &GoalRunner{
		MaxIterations: 3,
		Run: func(_ context.Context, prompt string) (Result, error) {
			runs = append(runs, prompt)
			return child.Run(context.Background(), prompt)
		},
		Judge: func(_ context.Context, _ string, _ string, _ []string) (GoalVerdict, error) {
			return GoalVerdict{Complete: false, Missing: []string{"more work " + string(rune('A'+len(runs)))}}, nil
		},
		OnStatus: func(s GoalStatus) { statuses = append(statuses, s) },
	}
	res, err := runner.Execute(ctx, "finish the feature")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Text != "work" || len(runs) != 3 {
		t.Fatalf("runs = %d, res = %q", len(runs), res.Text)
	}
	if !strings.Contains(runs[1], "more work B") {
		t.Fatalf("missing not injected into next round: %q", runs[1])
	}
	last := statuses[len(statuses)-1]
	if last.State != GoalStateCapped || last.Iteration != 3 || last.MaxIterations != 3 {
		t.Fatalf("terminal status = %+v", last)
	}

	// 判官提前判定完成：只跑一轮。
	runs = nil
	runner.Judge = func(_ context.Context, _ string, _ string, _ []string) (GoalVerdict, error) {
		return GoalVerdict{Complete: true}, nil
	}
	if _, err := runner.Execute(ctx, "easy goal"); err != nil {
		t.Fatalf("execute complete: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("complete should stop after 1 round, got %d", len(runs))
	}

	// 用户输入取消：构造一个在第一轮之后关闭的取消通道。
	cancel := make(chan struct{})
	count := 0
	runner.Cancel = cancel
	runner.Judge = func(_ context.Context, _ string, _ string, _ []string) (GoalVerdict, error) {
		count++
		if count == 1 {
			close(cancel)
		}
		return GoalVerdict{Missing: []string{"still missing"}}, nil
	}
	statuses = nil
	if _, err := runner.Execute(ctx, "cancellable goal"); err != nil {
		t.Fatalf("cancelled execute returned error: %v", err)
	}
	interrupted := false
	for _, s := range statuses {
		if s.State == GoalStateInterrupted {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatalf("cancel must emit interrupted status: %+v", statuses)
	}
}
