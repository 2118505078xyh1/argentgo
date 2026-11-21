package app

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// tuiUsageVisible 控制是否把 EventUsage 映射成 TUI 用量事件。默认 0（关闭），
// 因此 TUI 帧字节与接线前逐字节相同；仅在 ARGENT_TUI_USAGE=1 时由 TUI 入口开启。
var tuiUsageVisible atomic.Bool

// configureTUIUsage 按注入的 env 设置用量显示门控。env 为 nil 或值不为 "1" 时关闭。
func configureTUIUsage(env func(string) string) {
	tuiUsageVisible.Store(env != nil && strings.TrimSpace(env("ARGENT_TUI_USAGE")) == "1")
}

// tuiEventBridgeCap 是 TUI 事件循环前面的缓冲区。run.go 每帧都会抽取它；缓冲区满意味着
// UI 确实落后了，因此丢弃单个事件，而不是拖住共享 hub（那会淘汰这个订阅者并丢失后续流）。
const tuiEventBridgeCap = 256

// AgentToTUIEvent 把一个 agent 事件转换为全屏 UI 消费的、面向前端的子集。
// internal/tui.EventKind 对应两种工具事件、错误、重试和用量（见 internal/tui/events.go）；
// 没有文本、通知或审批种类，因此这些事件没有 TUI 映射，返回 false。
//
// 文本增量通过 Execute 的 onText 回调到达 TUI，而不是这个通道。审批卡片由 Model.Approve 创建
// （由 app 的 Approver 调用），并在匹配的 tool_start 到达时就地提升；
// 为一条审批通知发出合成的 TUI 事件，会在操作人员回答之前就把卡片标记为运行中。
// 这里用 ParseApprovalEvent 显式识别这些通知，使它们被有意跳过，而非意外跳过。
//
// 返回 false 可以安全忽略：HandleEvent 本来就会忽略未知种类。
func AgentToTUIEvent(e agent.Event) (tui.Event, bool) {
	switch e.Kind {
	case agent.EventToolStart:
		return tui.Event{
			Kind:       tui.EventToolStart,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
		}, true
	case agent.EventToolEnd:
		return tui.Event{
			Kind:       tui.EventToolEnd,
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Args:       e.Args,
			Output:     e.Output,
			IsError:    e.IsError,
			DurationMS: e.DurationMS,
		}, true
	case agent.EventError:
		// EventError 被加入 tui.EventKind 正是为了这次拷贝：Detail 携带 agent 已经脱敏的错误文本。
		return tui.Event{Kind: tui.EventError, Detail: e.Err}, true
	case agent.EventRetry:
		// EventRetry 是实时的退避反馈。RetryInfo 可安全上线传输：Reason 是封闭的决定标记
		// （绝不是自由格式的提供方文本），且 TUI 在渲染 RetryNotice 时会再次脱敏。
		// 构造 EventRetry 时没有负载的生产者没有内容可显示，因此丢弃它，而不是编造一个空的倒计时。
		if e.Retry == nil {
			return tui.Event{}, false
		}
		return tui.Event{
			Kind:         tui.EventRetry,
			RetryReason:  e.Retry.Reason,
			RetryAttempt: e.Retry.Attempt,
			RetryMax:     e.Retry.MaxAttempts,
			RetryWaitMS:  e.Retry.DelayMS,
		}, true
	case agent.EventUsage:
		// 取当轮增量 Usage 而非 TotalUsage：account() 不累加 CachedTokens，
		// TotalUsage.Cached 恒为零，TUI 每帧只展示本轮的缓存用量。
		// 是否转发到 TUI 由 StartTUIEventBridge 的 opt-in 门控决定。
		return tui.Event{
			Kind:             tui.TuiEventUsage,
			PromptTokens:     e.Usage.PromptTokens,
			CompletionTokens: e.Usage.CompletionTokens,
			CachedTokens:     e.Usage.CachedTokens,
		}, true
	case agent.EventNotice:
		// 审批请求/解决通知为原生和 SSE 传输走普通事件流。它们没有 TUI EventKind，
		// 且审批卡片已经由 Model.Approve 渲染，因此在这里跳过。
		if kind, _, _, _, ok := agent.ParseApprovalEvent(e); ok && kind != "" {
			return tui.Event{}, false
		}
		return tui.Event{}, false
	default:
		return tui.Event{}, false
	}
}

// StartTUIEventBridge 订阅 hub，并把映射后的事件转发到一个适合 tui.Options.Events 的缓冲通道。
// ctx 结束或 hub 订阅结束时它停止，且恰好关闭返回的通道一次。
// 向 out 的发送是非阻塞的：缓慢的 TUI 会丢弃事件，而不是拖住 hub 读取循环。
func StartTUIEventBridge(ctx context.Context, hub *server.EventHub) <-chan tui.Event {
	out := make(chan tui.Event, tuiEventBridgeCap)
	go func() {
		defer close(out)
		if hub == nil {
			return
		}
		sub, stop := hub.Subscribe(0)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, open := <-sub:
				if !open {
					return
				}
				tevt, ok := AgentToTUIEvent(ev)
				if !ok {
					continue
				}
				// 用量事件 opt-in：默认关闭时不转发，TUI 帧字节与接线前逐字节相同。
				if tevt.Kind == tui.TuiEventUsage && !tuiUsageVisible.Load() {
					continue
				}
				select {
				case out <- tevt:
				case <-ctx.Done():
					return
				default:
					// 消费者缓慢：丢弃此事件，继续抽取 hub。
				}
			}
		}
	}()
	return out
}

// TUIEventPipeline 是全屏宿主把真实 agent 事件喂给前端所需的组合：
// tui.Options.Events 的通道、附着到每次运行 context 上的接收器，以及关闭两条桥接器的 cancel 函数。
type TUIEventPipeline struct {
	Events <-chan tui.Event
	Emit   func(agent.Event)

	cancel context.CancelFunc
}

// NewTUIEventPipeline 创建一个私有事件 hub，把发出器接入其中，并启动转换桥接器。
// TUI 宿主直接调用 Execute，而不是通过 cli.Runner，因此没有可借用的运行器 EventHub；
// 这与 app.Factory 给运行器的 hub 是同一种。每次提示用 withEventEmit 把 Emit 附到其运行 context 上，
// 使 Execute 的 Emit 回调向这里发布。
func NewTUIEventPipeline(ctx context.Context) *TUIEventPipeline {
	bridgeCtx, cancel := context.WithCancel(ctx)
	hub := NewEventHub()
	emit, _ := WireRunEvents(hub, bridgeCtx)
	return &TUIEventPipeline{
		Events: StartTUIEventBridge(bridgeCtx, hub),
		Emit:   emit,
		cancel: cancel,
	}
}

// Stop 取消桥接器；由此 Events 通道会关闭。
func (p *TUIEventPipeline) Stop() { p.cancel() }
