package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 本文件把 #55（按 id 续跑）与 #56（worktree 隔离）的宿主侧能力接到
// agent.SubagentConfig。两条能力都通过回调注入，agent 包保持零依赖。

// taskRecordStore 是一个进程内、有界的子代理记录表。真正的持久化转录由会话承担；
// 这里只保存足以定位与续写的轻量记录。键由 task id（后台任务的 task N 或标签）生成。
type taskRecordStore struct {
	mu      sync.RWMutex
	records map[string]agent.SubagentRecord
	seq     atomic.Uint64
}

func newTaskRecordStore() *taskRecordStore {
	return &taskRecordStore{records: make(map[string]agent.SubagentRecord)}
}

func (s *taskRecordStore) nextID() string {
	return "task-" + strconv.FormatUint(s.seq.Add(1), 10)
}

func (s *taskRecordStore) put(record agent.SubagentRecord) {
	if record.ID == "" {
		return
	}
	s.mu.Lock()
	s.records[record.ID] = record
	s.mu.Unlock()
}

func (s *taskRecordStore) get(id string) (agent.SubagentRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[id]
	return record, ok
}

var taskWorktreeSeq atomic.Uint64

// worktreeNameRE 与 tools.worktreeNameRE 同口径：只做受控目录名，杜绝逃逸与参数注入。
var taskWorktreeNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// isolateSubagentWorktree 为子任务建立隔离工作区。失败一律降级 shared 并附可读说明，
// 绝不因为隔离不可用而中断子任务（#56 的安全默认：先隔离，无 git/无提交则降级）。
func isolateSubagentWorktree(ctx context.Context, root, label string) (dir, note string, err error) {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, runErr := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), runErr
	}
	if out, gitErr := run("rev-parse", "--is-inside-work-tree"); gitErr != nil || out != "true" {
		return "", "not inside a git worktree; subagent uses the shared workspace", nil
	}
	if _, gitErr := run("rev-parse", "--verify", "HEAD"); gitErr != nil {
		return "", "repository has no commits yet; subagent uses the shared workspace", nil
	}

	slug := strings.ToLower(strings.TrimSpace(label))
	slug = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-._")
	if slug == "" || len(slug) > 40 {
		slug = "subagent"
	}
	name := slug + "-" + strconv.FormatUint(taskWorktreeSeq.Add(1), 10)
	if !taskWorktreeNameRE.MatchString(name) {
		return "", "invalid worktree name; subagent uses the shared workspace", nil
	}
	parent := filepath.Join(root, ".argentgo-worktrees")
	target := filepath.Join(parent, name)
	if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
		return "", "cannot create worktree directory; subagent uses the shared workspace", nil
	}
	if _, gitErr := run("worktree", "add", "-b", name, target); gitErr != nil {
		return "", "git worktree creation failed; subagent uses the shared workspace", nil
	}
	// 保留目录：父级后续可直接把 target 路径交给文件工具读取改动。
	return target, "", nil
}

// wireTaskExtras 把续跑与隔离回调安装到根运行的 SubagentConfig 上。
// 未启用委派（opts.Subagent == nil）时零副作用。sess/chat/tools 可为 nil，
// 此时续跑会返回可读错误，隔离仍按 worktree→shared 的降级路径工作。
func wireTaskExtras(
	opts *agent.Options,
	res Resolved,
	chat llm.Chatter,
	tools agent.ToolRunner,
	emit func(agent.Event),
) {
	if opts == nil || opts.Subagent == nil {
		return
	}
	store := newTaskRecordStore()
	opts.Subagent.LookupTask = store.get
	opts.Subagent.IsolateTask = func(ctx context.Context, label string) (string, string, error) {
		return isolateSubagentWorktree(ctx, res.Root, label)
	}
	// 每完成一次普通子运行就登记一条记录，供后续 resume 按 id 找回。
	// 这里用包装后的 Chatter 运行一次续写：把原任务、尾部状态与新追问拼成新提示，
	// 复用当前运行的工具集与模型，而不是从头派发一个全新的委派任务。
	opts.Subagent.ResumeTask = func(ctx context.Context, record agent.SubagentRecord, followUp string) (agent.Result, error) {
		if chat == nil {
			return agent.Result{}, fmt.Errorf("subagent resume unavailable: no chatter configured")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Continue this subagent task.\nOriginal task: %s\nPrevious tail state: %s\n\nFollow-up: %s",
			record.Prompt, record.Tail, followUp)
		runner := &agent.Runner{Opts: agent.Options{
			Model:    res.Model,
			Chatter:  chat,
			Tools:    tools,
			MaxTurns: agent.DefaultSubagentTurns,
			System:   "You are resuming a prior subagent task. Continue from the retained state and answer the follow-up concisely.",
			Emit:     emit,
			Root:     res.Root,
		}}
		res2, err := runner.Run(ctx, b.String())
		if err == nil {
			store.put(agent.SubagentRecord{
				ID: record.ID, Label: record.Label, Prompt: record.Prompt,
				Tail: firstRunes(res2.Text, 600), Transcript: record.Transcript,
			})
		}
		return res2, err
	}
	// 记录写入由 app 在子任务结束时通过 Registrar 回调完成；这里暴露一个可调用的
	// 注册函数供接线点在 EventSubagentEnd 处使用（未接线时续跑表为空，返回可读错误）。
	opts.Subagent.OnEvent = chainSubagentEvent(opts.Subagent.OnEvent, store)
}

// chainSubagentEvent 在原有事件观察者之前插入记录 capture：子级结束事件携带任务标签与答案，
// 用它维护续跑表。原回调仍按原顺序收到完全相同的事件。
func chainSubagentEvent(original func(agent.Event), store *taskRecordStore) func(agent.Event) {
	return func(event agent.Event) {
		if event.Kind == agent.EventSubagentEnd && !event.IsError && event.Output != "" {
			store.put(agent.SubagentRecord{
				ID:         store.nextID(),
				Label:      event.Text,
				Prompt:     event.Text,
				Tail:       firstRunes(event.Output, 600),
				Transcript: "",
			})
		}
		if original != nil {
			original(event)
		}
	}
}

// firstRunes 按 rune 截断（CJK 安全），用于给续跑表保存有界的尾部状态。
func firstRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}
