package app

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/server"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// tuiUsageVisible 控制 TUI 头部用量/成本行是否可见。默认开启（UX 改善默认打开），
// 仅当 ARGENT_TUI_USAGE 修剪后精确为 "0" 时关闭。
//
// 门控下沉到渲染层：事件桥接器始终转发 usage 事件（统计行需要 token 数），
// 由 OnReady 里的 Model.SetUsageVisible 决定是否把用量显示在头部。
var tuiUsageVisible atomic.Bool

// configureTUIUsage 按注入的 env 设置用量显示门控。env 为 nil 或值修剪后不为 "0" 时开启。
func configureTUIUsage(env func(string) string) {
	tuiUsageVisible.Store(env == nil || strings.TrimSpace(env("ARGENT_TUI_USAGE")) != "0")
}

// tuiUsageVisibilitySetter 是 tui.Model 的按需能力接口，约定方法名为 SetUsageVisible。
// 这里用可选接口而非直接调用，使 app 包在 tui 代理并行补齐该 API 之前仍能编译；
// 一旦 setter 落地，类型断言即成功，门控自动下沉到渲染层。
// 若 tui 最终采用别的名字（如 ConfigureUsage），只需改这一处接口定义。
type tuiUsageVisibilitySetter interface {
	SetUsageVisible(visible bool)
}

// setTUIUsageVisible 把 opt-in 门控交给渲染层。tui 尚未提供 setter 时为空操作，
// 此时由主会话在 tui API 就绪后确认接线即可；app 侧逻辑保持不变。
// 参数用 any 而非 *tui.Model，便于用假实现守护"门控确实下沉"这条契约。
func setTUIUsageVisible(model any, visible bool) {
	if setter, ok := model.(tuiUsageVisibilitySetter); ok {
		setter.SetUsageVisible(visible)
	}
}

// tuiEventBridgeCap 是 TUI 事件循环前面的缓冲区。run.go 每帧都会抽取它；缓冲区满意味着
// UI 确实落后了，因此丢弃单个事件，而不是拖住共享 hub（那会淘汰这个订阅者并丢失后续流）。
const tuiEventBridgeCap = 256

// AgentToTUIEvent 把一个 agent 事件转换为全屏 UI 消费的、面向前端的子集。
// internal/tui.EventKind 对应轮次边界、两种工具事件、工具阶段进度、错误、重试和用量
// （见 internal/tui/events.go）；没有文本、审批和普通通知种类，这些事件返回 false。
//
// 文本增量通过 Execute 的 onText 回调到达 TUI，而不是这个通道。审批卡片由 Model.Approve 创建
// （由 app 的 Approver 调用），并在匹配的 tool_start 到达时就地提升；
// 为一条审批通知发出合成的 TUI 事件，会在操作人员回答之前就把卡片标记为运行中。
// 这里用 ParseApprovalEvent 显式识别这些通知，使它们被有意跳过，而非意外跳过。
//
// 返回 false 可以安全忽略：HandleEvent 本来就会忽略未知种类。
func AgentToTUIEvent(e agent.Event) (tui.Event, bool) {
	switch e.Kind {
	case agent.EventTurnStart:
		// 轮次边界用于统计行的"当前轮次"。字符串种类与 tui 侧
		// TuiEventTurnStart/TuiEventTurnEnd 一致，避免 app 反向依赖新增常量。
		return tui.Event{Kind: "turn_start"}, true
	case agent.EventTurnEnd:
		return tui.Event{Kind: "turn_end"}, true
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
		// 事件始终转发（统计行需要 token）；是否显示在头部由渲染层的
		// SetUsageVisible 门控，桥接层不再过滤。
		tevt := tui.Event{
			Kind:             tui.TuiEventUsage,
			PromptTokens:     e.Usage.PromptTokens,
			CompletionTokens: e.Usage.CompletionTokens,
			CachedTokens:     e.Usage.CachedTokens,
		}
		// 可选时间点：只有 llm 打点同时给出首 token 与流完成时刻时才填写。
		// 任一缺失就保持零值，TUI 回退到相邻 Usage 的 Δtokens/Δt 速率
		// （R33 行为），显示字节不变。CompletedAt 以流完成时刻作近似——
		// 纯流式速率只取 StreamedAt-FirstTokenAt，CompletedAt 不参与计算。
		if e.Usage.StreamFirstMS > 0 && e.Usage.StreamDoneMS > 0 {
			tevt.FirstTokenAt = time.UnixMilli(e.Usage.StreamFirstMS)
			tevt.StreamedAt = time.UnixMilli(e.Usage.StreamDoneMS)
			tevt.CompletedAt = tevt.StreamedAt
		}
		return tevt, true
	case agent.EventNotice:
		// 审批请求/解决通知为原生和 SSE 传输走普通事件流。它们没有 TUI EventKind，
		// 且审批卡片已经由 Model.Approve 渲染，因此在这里跳过。
		if kind, _, _, _, ok := agent.ParseApprovalEvent(e); ok && kind != "" {
			return tui.Event{}, false
		}
		// 工具阶段进度通知转成 TUI 的 tool_progress 事件，供侧边栏显示阶段标签。
		// 其余普通通知仍无 TUI 映射，返回 false 由 HandleEvent 忽略。
		if label, ok := tui.ParseToolProgress(e.Text); ok {
			return tui.Event{Kind: tui.EventToolProgress, Detail: label}, true
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
				// usage / turn / tool_progress 一律转发：统计行与阶段行依赖这些事件，
				// opt-in 语义由渲染层的 SetUsageVisible 控制，桥接层不再过滤。
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

	cancel        context.CancelFunc
	disableTiming func()
}

// NewTUIEventPipeline 创建一个私有事件 hub，把发出器接入其中，并启动转换桥接器。
// TUI 宿主直接调用 Execute，而不是通过 cli.Runner，因此没有可借用的运行器 EventHub；
// 这与 app.Factory 给运行器的 hub 是同一种。每次提示用 withEventEmit 把 Emit 附到其运行 context 上，
// 使 Execute 的 Emit 回调向这里发布。
func NewTUIEventPipeline(ctx context.Context) *TUIEventPipeline {
	bridgeCtx, cancel := context.WithCancel(ctx)
	hub := NewEventHub()
	emit, _ := WireRunEvents(hub, bridgeCtx)
	// TUI 头部需要速率显示，因此开启 llm 的 opt-in 时间点打点；
	// 打点只会给 Usage 附带 json:"-" 的内存字段，不改变任何 wire 字节。
	// Stop 时对称关闭；引用计数使多个 pipeline 并存时互不干扰。
	disableTiming := llm.EnableStreamTiming()
	return &TUIEventPipeline{
		Events:        StartTUIEventBridge(bridgeCtx, hub),
		Emit:          emit,
		cancel:        cancel,
		disableTiming: disableTiming,
	}
}

// Stop 取消桥接器；由此 Events 通道会关闭，并对称关闭本 pipeline 开启的打点。
func (p *TUIEventPipeline) Stop() {
	p.cancel()
	if p.disableTiming != nil {
		p.disableTiming()
		p.disableTiming = nil
	}
}
