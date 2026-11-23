package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/commands"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// buildTUICommandInfo 按 contextPrompt 相同的 sources 构造命令集，
// 并把发现结果映射成 TUI 补全面板所需的富文本摘要。
//
// 插件命令与 MCP/技能共享同一信任门槛（pluginCommandSources），
// 因此不受信任的检出无法把命令泄漏进补全列表。这里只读取命令的
// 声明性字段，不修改传入的 stdio / res，也不触碰 set.List() 的结果。
//
// 依赖：tui.CommandInfo 与 tui.Options.CommandInfo 由 tui 包提供
// （R16 slash 面板），app 侧只负责构造与注入。
func buildTUICommandInfo(stdio cli.IO, res Resolved) []tui.CommandInfo {
	home := homeDir(stdio.Env)
	sources := commands.DefaultSources(res.Root, home)
	// 安全红线：与 app_execute.go 的 /context 路径一样，必须带插件来源，
	// 并保留其用户/项目信任门槛，否则不受信任检出的插件命令会泄漏进补全列表。
	sources = append(sources, pluginCommandSources(res)...)
	set := commands.NewSetWithBuiltins(sources)
	listed := set.List()
	infos := make([]tui.CommandInfo, 0, len(listed))
	for _, c := range listed {
		infos = append(infos, tui.CommandInfo{
			Command:      c.Name,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
		})
	}
	return infos
}

// TUI 返回命令层为 `argentgo tui` / --tui 运行的前端。
//
// 它复用 Execute 的 agent 接线：提示经由同一注册表、策略和会话存储流式输出，
// 审批由 TUI 模态框回答。实时模型从 tui.Options.OnReady 捕获，
// 每次 security.Approver 调用都会阻塞在 Model.Approve 上，直到操作人员回答或该轮次的 context 结束。
func TUI(stdio cli.IO) cli.TUIFunc {
	// 用量显示（opt-in ARGENT_TUI_USAGE）：默认关闭，TUI 帧字节不变。
	configureTUIUsage(stdio.Env)
	// 审批卡 diff 显示（opt-in ARGENT_APPROVAL_DIFF）：默认关闭，帧字节不变。
	tui.ConfigureApprovalDiff(stdio.Env)
	// 徽章对比度 hex 角色（opt-in，三个 env 同时提供才生效）：无配置时帧字节不变。
	tui.ConfigureBadgeContrast(stdio.Env)
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
		approver.SetEnv(stdio.Env)
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
		// OnReady 捕获的实时模型，供每轮结束后把会话累计成本写进头部。
		// 与会话指针分开加锁，避免和 sessionFor 的临界区互相嵌套。
		var (
			modelMu sync.Mutex
			uiModel *tui.Model
		)
		updateHeaderCost := func(costUSD float64) {
			modelMu.Lock()
			model := uiModel
			modelMu.Unlock()
			if model != nil {
				model.SetContextInfo(res.Cfg.ContextWindow, fmt.Sprintf("$%.4f", costUSD))
			}
		}
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
				// 成本由 agent 的定价表推导并持久化到会话 stats 记录；agent.Result
				// 没有成本字段，因此这里读取会话累计值。默认（用量未 opt-in）不写头部，
				// 保证帧字节与接线前一致；开启 ARGENT_TUI_USAGE 后才显示成本行。
				if err == nil && tuiUsageVisible.Load() {
					updateHeaderCost(sess.AggregateStats().CostUSD)
				}
				return err
			},
			OnReady: func(model *tui.Model) {
				approver.attach(model)
				modelMu.Lock()
				uiModel = model
				modelMu.Unlock()
				// opt-in 门控下沉到渲染层：usage 事件始终桥接（统计行需要 token），
				// 头部用量/成本行是否可见由此决定；默认关闭时帧字节不变。
				setTUIUsageVisible(model, tuiUsageVisible.Load())
				// vim 键位（可选）：ARGENT_VIM_MODE=1 时启用，默认经典输入逐字节不变。
				if env := stdio.Env; env != nil && strings.TrimSpace(env("ARGENT_VIM_MODE")) == "1" {
					model.SetVimMode(true)
				}
			},
			CommandInfo: buildTUICommandInfo(stdio, res),
		}
		// 输入历史持久化（opt-in ARGENT_TUI_PROMPT_HISTORY）：默认关闭，
		// 不传 store，编辑器不读写任何文件、帧字节不变。仅当环境精确为 "1" 时
		// 才把存储目录下的 prompts.jsonl 交给 TUI，使重启后 Up 能召回旧输入。
		if promptHistoryEnabled(stdio.Env) {
			frontendOpts.PromptHistory = tui.NewPromptStore(filepath.Join(res.Storage, "prompts.jsonl"))
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
	env   func(string) string // opt-in 危险原因展示所需的环境读取；nil 时门控关闭
}

// NewApprover 返回一个尚未附着模态框的 Approver。
func NewApprover() *Approver {
	return &Approver{ready: make(chan struct{})}
}

// SetEnv 注入本次运行的环境读取函数。仅 TUI 入口在 opt-in 危险原因展示时调用；
// 不调用时 env 为 nil，Approver.Ask 走原有的 model.Approve 路径，逐字节不变。
func (a *Approver) SetEnv(env func(string) string) {
	a.mu.Lock()
	a.env = env
	a.mu.Unlock()
}

// approveDangerEnabled 报告是否开启危险命令原因展示。仅当环境值精确为 "1" 时开启，
// env 为 nil 时 fail-closed，默认行为不变。
func approveDangerEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_APPROVE_DANGER")) == "1"
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
	env := a.env
	a.mu.Unlock()
	if model == nil {
		return false, nil
	}
	// opt-in 危险原因展示：门控开启且命令为变更型时，把 security 计算的稳定中文原因
	// 传给 ApproveDanger（danger 非空时弹窗多一行并预选拒绝）。门控关闭时 env 为 nil
	// 或命令无危险，继续走原 model.Approve——与接线之前逐字节相同。name/args 始终原样传递。
	if approveDangerEnabled(env) {
		if token := shellCommandOf(name, json.RawMessage(args)); token != "" {
			if reason := security.DangerReason(token); reason != "" {
				return model.ApproveDanger(ctx, name, args, reason)
			}
		}
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

// promptHistoryEnabled 报告是否开启 TUI 输入历史持久化。仅当注入的 env 非 nil
// 且值修剪空白后精确为 "1" 时开启；env 为 nil、未设置或其它值一律 fail-closed，
// 使默认行为与接线之前逐字节一致。
func promptHistoryEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_TUI_PROMPT_HISTORY")) == "1"
}

// realStreams 报告 stdio 是否为进程终端，这是 TUI 应当接管 raw 模式的唯一情形。
func realStreams(stdio cli.IO) bool {
	return stdio.In == os.Stdin && stdio.Out == os.Stdout
}
