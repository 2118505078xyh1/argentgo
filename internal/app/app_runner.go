package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/server"
	"example.com/argentgo/internal/session"
)

// eventBridgeCap 限制 agent 循环与 hub 桥接器之间每次运行的通道容量。
// server.WireAgentEvents 会及时抽取它，因此 EmitEvent 只会短暂阻塞，且不会丢弃任何事件。
const eventBridgeCap = 1024

// nextPendingID 为持久化的审批记录提供一个无冲突的关联 id，而无需工具调用 id
// （security.Approver.Ask 不接收工具调用 id）。
var nextPendingID atomic.Uint64

// eventEmitKey 在运行 context 上承载每次运行的事件接收器。agent 循环的 Emit 回调
// 用 EmitEvent 读取它，因此 Execute 的签名无需改变，且从不桥接事件的调用方不受影响。
type eventEmitKey struct{}

// NewEventHub 返回无界面传输发布事件所用的 hub。它被导出，使集成者在 app.Factory 之外
// 构建传输时，可以显式设置 server.Config.Events。
func NewEventHub() *server.EventHub { return server.NewEventHub() }

// eventLogEnv 是「事件日志持久化 + SSE Last-Event-ID 续传」的独立门控变量。
const eventLogEnv = "ARGENT_EVENT_LOG"

func eventLogEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(eventLogEnv)) == "1"
}

// configureEventLog 在 opt-in 开启时给 hub 挂载磁盘事件日志，使 SSE 客户端
// 能用 Last-Event-ID 重放超出内存窗口的事件。关闭时不触碰 hub。
func configureEventLog(hub *server.EventHub, env func(string) string, storage string) {
	if !eventLogEnabled(env) || hub == nil || strings.TrimSpace(storage) == "" {
		return
	}
	hub.AttachEventLog(server.NewEventLog(filepath.Join(storage, "events.jsonl")))
}

// WireRunEvents 启动把 agent 事件转发到 hub 的桥接器。对每一个事件，都要从 agent 循环的
// Emit/OnEvent 回调中调用返回的 emit 函数。ctx 被取消或 emit 关闭时桥接器停止；
// done 在其 goroutine 退出时关闭。nil 的 hub 会禁用桥接：emit 是空操作且 done 已关闭，
// 因此调用方可以无条件地传递它。
func WireRunEvents(hub *server.EventHub, ctx context.Context) (emit func(agent.Event), done <-chan struct{}) {
	if hub == nil {
		closed := make(chan struct{})
		close(closed)
		return func(agent.Event) {}, closed
	}
	events := make(chan agent.Event, eventBridgeCap)
	done = server.WireAgentEvents(hub, events, ctx)
	emit = func(ev agent.Event) {
		select {
		case events <- ev:
		case <-ctx.Done():
			// 桥接器正在关闭；丢弃这最后一个事件是安全的，
			// 并能防止已取消的运行阻塞在已满的缓冲区上。
		}
	}
	return emit, done
}

func withEventEmit(ctx context.Context, emit func(agent.Event)) context.Context {
	return context.WithValue(ctx, eventEmitKey{}, emit)
}

// EmitEvent 把 ev 发布到附着在 ctx 上的事件桥接器（如果有）。agent 循环的 Emit 回调
// 调用它，使 --serve 和 --rpc 的订阅者观察到与进程内调用方相同的事件。没有桥接器时为空操作。
func EmitEvent(ctx context.Context, ev agent.Event) {
	if emit, ok := ctx.Value(eventEmitKey{}).(func(agent.Event)); ok && emit != nil {
		emit(ev)
	}
}

// EventHubOf 返回附着在由 Factory 创建的运行器上的 hub。无界面传输通过 Runner 的 EventHub
// 方法自动获取它，但在这里导出它可以让集成者显式设置 server.Config.Events。
// 对于外部/伪运行器它返回 nil，保留"无 hub 则无流"的默认行为。
func EventHubOf(r cli.Runner) *server.EventHub {
	if typed, ok := r.(interface{ EventHub() *server.EventHub }); ok {
		return typed.EventHub()
	}
	return nil
}

// WithPendingApprovals 把审批器的决定镜像到持久化的 PendingStore 中。
// 该装饰器是透明的：操作人员的答案原样返回，同时从提问那一刻起直到被回答，
// 磁盘上都会存在一条待处理记录。nil 的存储或内部审批器会禁用镜像。
func WithPendingApprovals(store *security.PendingStore, session string, inner security.Approver) security.Approver {
	if store == nil || inner == nil {
		return inner
	}
	return &pendingApprover{store: store, session: session, inner: inner}
}

// WithHeadlessPendingApprovals 是 --rpc/--serve/--acp 的审批路径。
// 这些传输拥有 os.Stdin：server.scanLines 从中读取以换行分隔的 JSON，
// 因此本地 PromptApprover 绝不能也从那里读取。它逐字节读取的 goroutine 会与传输的行扫描器竞争，
// 吞掉下一个请求的字节。在无界面运行中，操作人员的答案只通过 approval.answer 到达，
// PendingStore.Resolve 把它投递给 Ask 在这里注册的等待者。nil 的存储意味着根本没有答案通道：
// 此时应报告审批通道不可用，而不是伪造一次操作人员拒绝。
// 伪造的拒绝会被审计为"refused"，并给模型一个最终的、不可重试的阻断，
// 即使无界面通道可能稍后恢复。
func WithHeadlessPendingApprovals(store *security.PendingStore, session string) security.Approver {
	if store == nil {
		return security.UnavailableApprover{}
	}
	return &pendingApprover{store: store, session: session, headless: true}
}

// headlessTransport 报告 opts 是否运行在线传输而非直接的 CLI/TUI 会话。
// 在无界面模式下，stdin 是协议流，而不是操作人员的终端，因此不能在其上提示审批。
func headlessTransport(opts cli.Options) bool {
	return opts.Serve || opts.RPC || opts.ACP
}

type pendingApprover struct {
	store   *security.PendingStore
	session string
	inner   security.Approver
	// headless 完全抑制本地审批器：没有 goroutine 读取 stdin，
	// Ask 只等待外部答案或 ctx 取消。
	headless bool
}

func (p *pendingApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	id := fmt.Sprintf("%s-%d", name, nextPendingID.Add(1))

	// 在持久化之前先注册进程内的答案通道，使与下方 Add 竞速的外部 approval.answer
	// 绝不会先调用 Resolve 却发现没有等待者。cancel 对提前返回/错误路径采用延迟调用，
	// 一旦做出决定就显式调用。
	answer, cancelWaiter := p.store.RegisterWaiter(p.session, id)
	defer cancelWaiter()

	// 尽力而为的持久化：写入失败绝不能阻塞或改变审批决定，
	// 该决定始终是纯内存交互。
	_ = p.store.Add(security.PendingApproval{
		ID:         id,
		Session:    p.session,
		Tool:       name,
		ArgsDigest: security.PendingArgsDigest(args),
	})

	// 保持本地 TUI/CLI 交互不变：在等待外部答案期间，inner.Ask 继续运行。先到的答案胜出。
	// 无界面传输改用 WithHeadlessPendingApprovals 安装其审批器，因此 local 保持为 nil：
	// 没有 goroutine 触碰由协议拥有的 stdin，approval.answer（通过 Resolve -> answer）
	// 是唯一的决定来源。nil 通道在 select 中会永远阻塞，这正是无界面情形所需要的。
	type localAnswer struct {
		granted bool
		err     error
	}
	var local <-chan localAnswer
	if !p.headless {
		ch := make(chan localAnswer, 1)
		local = ch
		go func() {
			granted, err := p.inner.Ask(ctx, name, args)
			ch <- localAnswer{granted: granted, err: err}
		}()
	}

	var granted bool
	var err error
	// 如果两个答案在同一瞬间都就绪，优先选择远端答案：
	// 它是操作人员在控制通道上的显式决定。
	select {
	case granted = <-answer:
	default:
		select {
		case granted = <-answer:
		case ans := <-local:
			granted, err = ans.granted, ans.err
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	// 在持久化之前先取消注册，使迟到的 Resolve 无法投递到无人读取的通道。
	// 这个 Resolve 是幂等的：外部答案已经移除了待处理记录并消费了它的等待者，
	// 因此对磁盘的第二次调用是空操作，不会投递重复答案。
	cancelWaiter()
	_ = p.store.Resolve(p.session, id, granted)
	return granted, err
}

// Factory 返回 CLI 所期望的运行器工厂。
func Factory(stdio cli.IO) cli.RunnerFactory {
	return func(opts cli.Options) (cli.Runner, error) {
		resolved, err := Resolve(stdio, opts)
		if err != nil {
			return nil, err
		}
		store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
		if err != nil {
			return nil, fmt.Errorf("open sessions: %w", err)
		}
		// 每个 serve/运行器实例拥有一个 hub 和一个待处理存储。传输通过 Runner.EventHub
		// 发现 hub，每次运行都会包装其审批器，使待处理请求在重连后仍然持久。
		hub := NewEventHub()
		configureEventLog(hub, stdio.Env, resolved.Storage)
		warmer, warmReplay := newRunnerWarmer(stdio.Env)
		r := &runner{
			stdio:      stdio,
			res:        resolved,
			store:      store,
			execute:    Execute,
			hub:        hub,
			pending:    security.NewPendingStore(filepath.Join(resolved.Storage, "pending")),
			warmer:     warmer,
			warmReplay: warmReplay,
		}
		// 运行器是 --serve/--rpc 在多个提示间复用的长生命周期引擎，因此它拥有配置热重载钩子。
		// 一次性调用方直接使用 Execute，从不构建重载器。该钩子是只读的（采用 + 记日志），
		// 绝不会重新接线在途的运行。
		r.reload = newConfigReloader(&r.res.Cfg, func(msg string) {
			if stdio.Err != nil {
				fmt.Fprintln(stdio.Err, "config: "+msg)
			}
		})
		return r, nil
	}
}

// ActiveSessionID 返回运行器正在追加的会话（当它暴露时）。只持有 cli.Runner 的调用方
// 可以用它显示或记录已恢复的会话 id。
func ActiveSessionID(r cli.Runner) string {
	if typed, ok := r.(interface{ SessionID() string }); ok {
		return typed.SessionID()
	}
	return ""
}

type runner struct {
	stdio cli.IO
	res   Resolved
	store *session.Store

	// hub 由该实例上的每次运行共享，无界面传输通过 EventHub() 发现它。pending 把审批镜像到磁盘。
	// 两者在手工构建的测试运行器上均为 nil，使其行为保持不变。
	hub     *server.EventHub
	pending *security.PendingStore

	// reload 是长生命周期引擎的配置热重载钩子。它在手工构建的测试运行器上为 nil，
	// 使那些路径保持不变。生产环境在 Factory 中设置它；一次性进程永远不会到达第二个轮次边界，
	// 因此从不轮询（见 Prompt）。
	reload *configReloader

	// execute 和 claimHook 是 Prompt 按实例划分的接缝。生产环境把 execute 设为 Execute（见 Factory），
	// 并让 claimHook 保持 nil；测试在自己的运行器上注入伪实现，而不是替换包级全局变量，
	// 这样它们可以并行运行。
	execute   func(context.Context, cli.IO, Resolved, *session.Session, string, func(string)) (agent.Result, error)
	claimHook func(*runner)

	// busy、cancel 和 current 都由 mu 保护。尤其是 single-flight 槽位（busy）及其 cancel 函数
	// 在同一个临界区中安装和清除，因此 Abort 绝不会观察到"忙但没有注册 cancel"而丢失中止。
	mu      sync.Mutex
	busy    bool
	cancel  context.CancelFunc
	current *session.Session

	// 后台结果队列归属 (runner, session)：同一会话跨提示共享，使"后台任务比提示
	// 活得久"时结果仍能在后续提示被抽取；换会话时重建，杜绝跨会话串味。
	// 它们只在 Prompt 串行路径上读写（single-flight 保证），无需 mu。
	bg        *agent.BackgroundResults
	bgSession string

	// warmer 与 warmReplay 是提示缓存保活的运行时状态，按 runner 生命周期持有，
	// 使空闲后的 1-token 重放能命中同一份缓存。禁用时为 nil，所有相关路径零副作用。
	warmer     *llm.CacheWarmer
	warmReplay *warmReplayStore
}

// Prompt 满足 cli.Runner。一个运行器上最多只有一个提示在途。
func (r *runner) Prompt(ctx context.Context, text string, onText func(string)) (string, error) {
	ctx, cancel := context.WithCancel(ctx)

	// 把本次运行的 agent 事件桥接到共享 hub。桥接器从每次运行的通道读取，并随运行上下文停止，
	// 因此没有 goroutine 或过期事件会比提示活得更久。除非本次运行的 Emit 回调通过 ctx 转发事件，
	// 否则 EmitEvent 是空操作（见报告中的接线说明）。
	if r.hub != nil {
		emit, _ := WireRunEvents(r.hub, ctx)
		ctx = withEventEmit(ctx, emit)
	}

	// 上下文压力观察是进程级 opt-in：开启时挂接观察器，关闭时返回 nil，
	// ctx 保持不变，事件序列（含 seq）与接线之前逐字节一致。
	// Execute 会在解析本次 opt-in 后按本次值覆盖它（见 replaceContextPressure），
	// 因此这里基于全局开关的装配只是兼容既有装配点测试的兜底，不会造成跨运行残留。
	ctx = withContextPressure(ctx, newContextPressureObserver())

	// 在同一个临界区中占用 single-flight 槽位并注册 cancel 函数。Abort 使用同一个 mu，
	// 因此它要么完全看不到提示（空操作），要么看到一个完整注册的 cancel 函数并可以取消它。
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		cancel() // 提示被拒绝时不要泄漏派生出的 context
		return "", errors.New("prompt already running")
	}
	r.busy = true
	r.cancel = cancel
	firstPrompt := r.current == nil
	r.mu.Unlock()

	// 提示缓存保活：轮次边界报告空闲，并在新提示到来前按需重放上一次请求的
	// 1-token 副本。两者都只在 warmer 存在时生效，重放失败不影响本次提示。
	if r.warmer != nil && r.warmReplay != nil {
		ctx = withWarmSink(ctx, r.warmReplay.record)
		defer func() { r.warmer.RecordIdle(time.Now()) }()
		if !firstPrompt {
			_ = maybeWarm(ctx, r.res.Chatter, r.warmer, r.warmReplay)
		}
	}

	// 轮次边界：长生命周期引擎在提示之间检查磁盘配置变化，并在有新配置时采用新快照。
	// 一次性进程只服务一个提示，因此永远不会到达第二个边界，也从不轮询——退出前不会做无意义的
	// stat/重载工作。重载器在手工构建的测试运行器上为 nil，因此那些也不受影响。
	if !firstPrompt && r.reload != nil {
		r.reload.poll()
	}

	// 测试接缝：到这一步 busy 和 cancel 都已安装。生产环境让该钩子保持 nil。
	if r.claimHook != nil {
		r.claimHook(r)
	}

	// 在同一个 mu 下原子地清除 cancel 槽位并释放该槽位，与 Prompt 和 Abort 互斥。
	defer func() {
		r.mu.Lock()
		r.cancel = nil
		r.busy = false
		r.mu.Unlock()
		cancel()
	}()

	sess, err := r.ensureSession(text)
	if err != nil {
		return "", err
	}
	// 后台结果队列按会话归属：同会话跨提示复用，换会话重建。队列归属 runner
	// 而非进程，天然隔离并发的 TUI/--rpc 实例（原进程级共享会串味）。
	if r.bg == nil || r.bgSession != sess.ID() {
		r.bg = agent.NewBackgroundResults(0, 0)
		r.bgSession = sess.ID()
	}
	ctx = agent.WithBackgroundResults(ctx, r.bg)
	// 把审批镜像到本次会话的持久化存储。按运行复制 Resolved 可使包装器不进入 r.res；
	// 手工构建的测试运行器（pending == nil）看到的是原始 Resolved 和未改变的行为。
	execRes := r.res
	if r.warmer != nil {
		// 按运行包装：真实 Chat 刷新保活时钟，预热器不会进入 r.res 的长期状态。
		execRes.Chatter = newWarmRecordChatter(r.res.Chatter, r.warmer)
	}
	if r.pending != nil {
		_ = r.pending.Clear(sess.ID()) // 新运行会取代过期的请求
		var approve security.Approver
		switch {
		case execRes.Approve != nil:
			// TUI/外部调用方会注入自己的模态审批器；保留它。
			approve = WithPendingApprovals(r.pending, sess.ID(), execRes.Approve)
		case headlessTransport(r.res.Options):
			if execRes.Approval == "never" {
				// 根本不会有人类答案；保留 never 模式的立即拒绝，而不派生 stdin 读取器。
				approve = WithPendingApprovals(r.pending, sess.ID(), security.NeverApprover{})
			} else {
				// --rpc/--serve/--acp 拥有 stdin。只有 approval.answer 可以做决定；
				// 这里的 PromptApprover 会窃取 RPC 字节。
				approve = WithHeadlessPendingApprovals(r.pending, sess.ID())
			}
		default:
			// 直接的 CLI 路径：操作人员的 tty 就是 stdin，因此本地 y/n 正是当下的行为。
			approve = WithPendingApprovals(r.pending, sess.ID(), approverFor(r.stdio, execRes))
		}
		execRes.Approve = approve
	}
	result, err := r.execute(ctx, r.stdio, execRes, sess, text, onText)
	return result.Text, err
}

// Abort 满足 cli.Runner。
func (r *runner) Abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// EventHub 把共享 hub 暴露给无界面传输。该导出方法使 server.NewHandler/RunRPC
// 能通过一个小接口发现它，而无需 internal/server 导入 internal/app。
func (r *runner) EventHub() *server.EventHub {
	return r.hub
}

// PendingStore 把持久化的审批镜像暴露给无界面传输。approval.list RPC 用它，
// 使重连的客户端能重新列出断开时仍在等待的请求。它在手工构建的测试运行器上返回 nil，
// 从而保持其行为不变。
func (r *runner) PendingStore() *security.PendingStore {
	return r.pending
}

// SessionID 返回运行器当前正在追加的会话。第一次提示之前它为空。
func (r *runner) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return ""
	}
	return r.current.ID()
}

func (r *runner) ensureSession(firstPrompt string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current, nil
	}
	sess, err := openOrCreateSession(r.store, r.res.Options, firstPrompt)
	if err != nil {
		return nil, err
	}
	r.current = sess
	return sess, nil
}
