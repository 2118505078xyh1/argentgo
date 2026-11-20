package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TUI 返回命令层为 `argent tui` / --tui 运行的前端。
//
// 它复用 Execute 的 agent 接线：提示经由同一注册表、策略和会话存储流式输出，
// 审批由 TUI 模态框回答。实时模型从 tui.Options.OnReady 捕获，
// 每次 security.Approver 调用都会阻塞在 Model.Approve 上，直到操作人员回答或该轮次的 context 结束。
func TUI(stdio cli.IO) cli.TUIFunc {
	return func(ctx context.Context, opts cli.Options) error {
		res, err := Resolve(stdio, opts)
		if err != nil {
			return err
		}
		store, err := session.Open(filepath.Join(res.Storage, "sessions"))
		if err != nil {
			return fmt.Errorf("open sessions: %w", err)
		}

		approver := NewApprover()
		res.Approve = approver

		// 把真实 agent 事件喂给 TUI 的工具卡片。TUI 宿主直接调用 Execute（没有 cli.Runner），
		// 因此它拥有一个私有 hub，并在下方把其发出器附着到每次运行的 context 上。
		// 退出时 Stop 会关闭桥接器。
		events := NewTUIEventPipeline(ctx)
		defer events.Stop()

		var (
			mu      sync.Mutex
			current *session.Session
		)
		sessionFor := func(text string) (*session.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			if current != nil {
				return current, nil
			}
			sess, err := openOrCreateSession(store, opts, text)
			if err != nil {
				return nil, err
			}
			current = sess
			return sess, nil
		}

		// TurnAction 钩子用真实的会话操作接回 /undo、/retry 和 /name 命令（以及 Alt+U / Alt+R 按键）。
		// 没有它们，TUI 虽然公布了这些命令，却只会打印"宿主尚未接线"。
		// currentSession 返回实时会话；在第一次提示创建会话之前返回错误；
		// 这些钩子共享 sessionFor 的互斥锁，使并发提示不会与检查点/分叉/标题写入发生竞态。
		currentSession := func() (*session.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			if current == nil {
				return nil, fmt.Errorf("no active session yet")
			}
			return current, nil
		}
		undoTurn := func(context.Context, string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			checkpoint, _, err := sess.LatestCheckpoint()
			if err != nil {
				return err
			}
			_, err = session.UndoCheckpoint(res.Root, checkpoint)
			return err
		}
		retryTurn := func(_ context.Context, arg string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			fork, err := session.Retry(store.Dir(), sess.ID(), arg)
			if err != nil {
				return err
			}
			mu.Lock()
			current = fork // 后续提示在分叉出的分支上继续
			mu.Unlock()
			return nil
		}
		renameTurn := func(_ context.Context, title string) error {
			sess, err := currentSession()
			if err != nil {
				return err
			}
			return sess.SetTitle(title)
		}

		frontendOpts := tui.Options{
			Root:          res.Root,
			Model:         res.Model,
			Locale:        res.Locale,
			Sessions:      sessionTitles(store),
			ContextWindow: res.Cfg.ContextWindow,
			Events:        events.Events,
			Undo:          undoTurn,
			Retry:         retryTurn,
			Rename:        renameTurn,
			Prompt: func(turnCtx context.Context, text string, onText func(string)) error {
				turnCtx = withEventEmit(turnCtx, events.Emit)
				sess, err := sessionFor(text)
				if err != nil {
					return err
				}
				_, err = Execute(turnCtx, stdio, res, sess, text, onText)
				return err
			},
			OnReady: func(model *tui.Model) { approver.attach(model) },
		}
		// 测试（或管道）不得让终端进入 raw 模式。
		if !realStreams(stdio) {
			frontendOpts.IO = &tui.IO{In: stdio.In, Out: stdio.Out}
		}
		return tui.Run(ctx, frontendOpts)
	}
}

// Approver 通过委托给 TUI 模态框来满足 security.Approver。
type Approver struct {
	mu    sync.Mutex
	model *tui.Model
	ready chan struct{}
	once  sync.Once
}

// NewApprover 返回一个尚未附着模态框的 Approver。
func NewApprover() *Approver {
	return &Approver{ready: make(chan struct{})}
}

// attach 记录实时模型；第一次调用会打开 ready 闩锁。
func (a *Approver) attach(model *tui.Model) {
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	a.once.Do(func() { close(a.ready) })
}

// Ask 阻塞直到模态框给出结果或 ctx 结束。
func (a *Approver) Ask(ctx context.Context, name, args string) (bool, error) {
	select {
	case <-a.ready:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	a.mu.Lock()
	model := a.model
	a.mu.Unlock()
	if model == nil {
		return false, nil
	}
	return model.Approve(ctx, name, args)
}

// sessionTitles 渲染选择器条目，最新的排在最前。
func sessionTitles(store *session.Store) []string {
	metas, err := store.List()
	if err != nil {
		return nil
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	titles := make([]string, 0, len(metas))
	for _, meta := range metas {
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		titles = append(titles, fmt.Sprintf("%s — %s", title, meta.ID))
	}
	return titles
}

// realStreams 报告 stdio 是否为进程终端，这是 TUI 应当接管 raw 模式的唯一情形。
func realStreams(stdio cli.IO) bool {
	return stdio.In == os.Stdin && stdio.Out == os.Stdout
}
