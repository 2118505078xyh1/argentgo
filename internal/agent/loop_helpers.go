package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// maybeCompact 实现轮次中的压缩边界。它只在工具批次之后运行，绝不会在工具调用与其结果之间运行。
// 目标窗口是 0.8 迟滞带减去摘要预留量，因此一次压缩就能缓解压力，而不是在下一次检查时再次触发。
func (s *runState) maybeCompact() {
	if s.compactor == nil || s.compactThreshold <= 0 {
		return
	}
	before := EstimateMessages(s.messages)
	if before <= s.compactThreshold {
		return
	}
	window := int(float64(s.compactThreshold)*compactionRecoveryBand) - summaryReserveTokens
	if window < minCompactWindow {
		window = minCompactWindow
	}
	compacted, changed := s.compactor.MaybeCompact(s.messages, window)
	if !changed || len(compacted) == 0 {
		return
	}
	s.messages = compacted
	// 摘要后的上下文是一个新窗口：清除按窗口划分的投递状态，
	// 使预算提醒能随着该窗口被填满而再次触发。
	s.resetBudgetReminder()
	// 现在重新估算，使下一次迭代顶部的预算检查看到压缩后的历史，而非旧的超长历史。
	s.promptTokens = EstimateMessages(s.messages)
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"compacted context: ~%d -> ~%d tokens (target %d)", before, s.promptTokens, window,
	)})
}

// advertisedSpecs 确定下一次请求的工具列表。没有过滤器时使用运行器的完整目录；
// 有过滤器时只公布允许的子集。授权仍然发生在 Run 中，绝不在这里。
// 最终列表在发送之前会针对当前模型的 schema 特性做投影。
func (s *runState) advertisedSpecs() []llm.ToolSpec {
	specs := s.tools.Specs()
	if s.opts.FilterSpecs != nil {
		specs = s.opts.FilterSpecs(specs)
	}
	return projectSpecsForModel(specs, s.model)
}

// projectSpecsForModel 对每个非空的 Parameters 文档应用按模型系列的 schema 投影。
// 返回的切片始终是新的副本，因此投影请求绝不会修改工具集自身的规格目录。
func projectSpecsForModel(specs []llm.ToolSpec, model string) []llm.ToolSpec {
	compat := llm.CompatNone
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "moonshot"), strings.Contains(name, "kimi"):
		compat = llm.CompatMoonshot
	case strings.Contains(name, "gemini"):
		compat = llm.CompatGemini
	}
	if compat == llm.CompatNone {
		return specs
	}

	out := make([]llm.ToolSpec, len(specs))
	copy(out, specs)
	for i := range out {
		raw := out[i].Function.Parameters
		if len(raw) == 0 {
			continue
		}
		projected := llm.ProjectToolSchema(string(raw), compat)
		if projected != string(raw) {
			out[i].Function.Parameters = json.RawMessage(projected)
		}
	}
	return out
}

// chat 用当前模型发起一次模型请求，并把答案增量转发给 onText。
// 事件的发出留给调用方，使失败的尝试可以重试，而不会把部分文本泄漏到操作人员的流中。
func (s *runState) chat(ctx context.Context, onText func(string)) (llm.Message, error) {
	var streamed strings.Builder
	// 修复交给提供方的记录*视图*：在恢复或批次中断之后，助手的 tool_use 可能丢失了结果，
	// 严格的提供方会拒绝这种情况。NormalizeToolPairing 是纯视图辅助函数，
	// 因此 s.messages 保持原始字节，只有外发的请求被修正。
	messages := NormalizeToolPairing(s.messages)
	reply, err := s.opts.Chatter.Chat(ctx, s.model, messages, s.specs, func(delta string) {
		if delta == "" {
			return
		}
		streamed.WriteString(delta)
		if onText != nil {
			onText(delta)
		}
	})
	if err != nil {
		return llm.Message{}, err
	}
	if reply.Role == "" {
		reply.Role = "assistant"
	}
	if reply.Content == "" && streamed.Len() > 0 {
		// 只做流式输出却忘记组装的客户端，仍应把操作人员已看到的文本写入记录。
		reply.Content = streamed.String()
	}
	return reply, nil
}

// 下面的两个负载类型镜像了 session.UsageRecord 和 session.SessionStats 的线上结构，
// 但不导入 session 包：agent 层保持无依赖。它们通过注入的 Recorder 写入，
// app 把 Recorder 接到 Session.Append。保持 JSON 字段名同步，
// 才能让 session.UsageRecords / LatestStats / AggregateStats 读回它们。
type sessionUsagePayload struct {
	Model  string `json:"model,omitempty"`
	Input  int    `json:"input"`
	Output int    `json:"output"`
	Cached int    `json:"cached,omitempty"`
	TS     string `json:"ts,omitempty"`
}

type sessionStatsPayload struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostSource       string  `json:"cost_source,omitempty"`
	Updated          string  `json:"updated,omitempty"`
}

// recordRunStats 把一次运行的累计 token/花费汇总持久化为一条 stats 记录。
// 每次运行一条记录，可使 session.AggregateStats 的求和操作语义在多次恢复后仍然正确；
// 逐次请求的事实由 account() 写入。
func (s *runState) recordRunStats() {
	source := ""
	if s.costKnown {
		// 花费由注入的价格表推导而来，从不由提供方线上协议报告，因此它始终是估算值。
		source = "estimated"
	}
	s.record("stats", sessionStatsPayload{
		PromptTokens:     s.usage.PromptTokens,
		CompletionTokens: s.usage.CompletionTokens,
		CachedTokens:     s.usage.CachedTokens,
		Calls:            s.turns,
		CostUSD:          s.costUSD,
		CostSource:       source,
		Updated:          time.Now().UTC().Format(time.RFC3339),
	})
}

// account 把一条回复的用量折算进运行总量并返回它。
//
// 提供方报告的用量优先；否则使用刚发送的提示和收到的答案的估算值，
// 这样即使客户端完全不报告用量，预算也能工作。
func (s *runState) account(reply llm.Message, promptEstimate int) (llm.Usage, int) {
	var usage llm.Usage
	if reply.Usage != nil {
		usage = *reply.Usage
	}
	if usage.PromptTokens <= 0 {
		usage.PromptTokens = promptEstimate
	}
	if usage.CompletionTokens <= 0 {
		usage.CompletionTokens = EstimateTokens(reply.Content)
	}
	s.usage.PromptTokens += usage.PromptTokens
	s.usage.CompletionTokens += usage.CompletionTokens
	// 缓存细分也要累加，否则累计统计里的 cached 恒为 0，
	// 与逐轮用量和运行总计的口径不一致（R23 真 bug 修复）。
	// 它不影响预算与成本：两者都只按 prompt+completion 计费，缓存 token 不重复计入。
	// 这是纯 additive 的 wire 兼容：Usage 本就有 omitempty 的缓存字段，这里只是把值填上。
	s.usage.CachedTokens += usage.CachedTokens
	s.usage.CacheWriteTokens += usage.CacheWriteTokens
	if price, ok := PriceFor(s.opts.Prices, s.model); ok {
		s.costUSD += price.Cost(usage)
		s.costKnown = true
	}
	// 每次运行一个观察器：随着花费累积，每个花费阈值只告警一次。
	for _, warning := range s.costWatch.Observe(s.costUSD) {
		s.emit(Event{Kind: EventNotice, Text: warning})
	}
	// 逐次请求的用量事实。在轮次中途写入（在任何终结助手记录之前），
	// 因此它绝不会改变 recorder 最后看到的是哪一行。
	s.record("usage", sessionUsagePayload{
		Model:  s.model,
		Input:  usage.PromptTokens,
		Output: usage.CompletionTokens,
		Cached: usage.CachedTokens,
		TS:     time.Now().UTC().Format(time.RFC3339),
	})
	return usage, usage.PromptTokens
}

// injectSteering 把排队的纠正消息移入对话。额外的文本（例如一次性的卡住提示）
// 也走同一条用户轮次路径。
func (s *runState) injectSteering(extra ...string) bool {
	pending := s.steer.drain()
	// 把已完成的后台子 agent 结果作为合成用户轮次抽取到本次运行中。
	// 投递以本次运行的唯一 owner 为键，而非深度：同级子 agent 共享相同深度，
	// 否则会窃取彼此的结果。深度保留在每个排队项上仅用于诊断。
	// 见 SubagentConfig.Background / BackgroundResults。
	if s.opts.Subagent != nil && s.opts.Subagent.Background != nil {
		pending = append(pending, s.opts.Subagent.Background.DrainFor(s.bgOwner)...)
	}
	pending = append(pending, extra...)
	for _, text := range pending {
		s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
		s.record("steer", text)
		s.emit(Event{Kind: EventNotice, Text: "steering: " + text})
	}
	return len(pending) > 0
}

// budgetReminderPrefix 在记录中标记一条模型可见的预算提醒，
// 使测试和前端无需解析英文正文即可识别它。
const budgetReminderPrefix = "[budget reminder] "

// budgetReminderWindow 返回用于提醒的上下文占用窗口，以及本次运行是否启用提醒。
// 提醒以压缩阈值作为选择性启用的条件，它是循环管理的显式上下文边界：
// 未配置阈值的运行不改动其消息，因此默认行为（包括仅裁剪的 MaxContextTokens 路径）不变。
func (s *runState) budgetReminderWindow() (int, bool) {
	if s.compactThreshold > 0 {
		return s.compactThreshold, true
	}
	return 0, false
}

// resetBudgetReminder 在压缩后清除按窗口划分的投递状态。下一次检查会把摘要后的上下文
// 当作一个新窗口，并可以再次播报其层级。
func (s *runState) resetBudgetReminder() {
	s.reminderDelivered = nil
	s.reminderWindow = 0
}

// maybeBudgetReminder 在当前上下文占用率跨越新里程碑时，向 s.messages 追加一条模型可见的提醒。
// 它以只读方式复用纯决策层（BudgetTier/ShouldRemind），对没有配置窗口的运行是空操作。
//
// 该提醒是一条独立的 "user" 轮次，与控制/错误指引注入器类似：调用方在循环顶部、
// 停止检查和裁剪之后、记录处于配对的轮末视图时调用它。这个位置保证了消息会在下一次请求中
// 真正被发送（模型会读到它并自行收尾），同时不会拆散 tool_use/tool_result 对，
// 也不会改变任何停止语义。
func (s *runState) maybeBudgetReminder() {
	window, ok := s.budgetReminderWindow()
	if !ok || window <= 0 {
		return
	}
	if s.reminderDelivered == nil {
		s.reminderDelivered = make(map[int]bool)
	}
	// 不同的窗口大小（压缩后全新的 run state，或配置发生变化）意味着新窗口；
	// ShouldRemind 随后会清除投递记录，并允许相同层级再次告警。
	newWindow := s.reminderWindow != window
	s.reminderWindow = window

	curTier := BudgetTier(s.promptTokens, window)
	// delivered 恰好保存已播报的层级，因此其最大值就是决策层应比较的上一层级。
	prevTier := 0
	for tier := range s.reminderDelivered {
		if tier > prevTier {
			prevTier = tier
		}
	}
	if !ShouldRemind(prevTier, curTier, s.reminderDelivered, newWindow) {
		return
	}

	text := budgetReminderPrefix + BudgetReminderText(window-s.promptTokens, window)
	s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
	s.emit(Event{Kind: EventBudgetReminder, Turn: s.turns, Text: text})
}

// maxParallel 是只读批次的 worker 数量。
func (s *runState) maxParallel() int {
	if s.opts.MaxParallel <= 1 {
		return 1
	}
	return s.opts.MaxParallel
}

// mutating 报告某工具是否会改变状态、因而需要审批。
func (s *runState) mutating(name string) bool {
	if s.opts.Mutating != nil {
		return s.opts.Mutating(name)
	}
	if len(s.opts.MutatingTools) > 0 {
		for _, candidate := range s.opts.MutatingTools {
			if candidate == name {
				return true
			}
		}
		return false
	}
	for _, candidate := range defaultMutatingTools {
		if candidate == name {
			return true
		}
	}
	return false
}

func (s *runState) needsApproval(name string) bool {
	return s.opts.Approve != nil && s.mutating(name)
}

func (s *runState) budgetBreach() (string, string, bool) {
	return s.opts.Budget.Breach(s.usage, s.promptTokens, s.costUSD, s.costKnown)
}

// result 把本次运行快照为公开的 Result 类型。
func (s *runState) result(stopped string) Result {
	return Result{
		Text:     s.lastText,
		Messages: append([]llm.Message(nil), s.messages...),
		Usage:    s.usage,
		Turns:    s.turns,
		Stopped:  stopped,
	}
}

// appendAbortedAssistant 在被取消的运行上留下一条终结的助手轮次。
// 没有它，从最后一条助手消息推断结果的消费者会看到一个工具使用轮次（或根本没有助手消息），
// 而非中止。只有记录发生变化：Result.Stopped 仍为 StopCancelled。
func (s *runState) appendAbortedAssistant() {
	aborted := llm.Message{
		Role:         "assistant",
		Content:      "Operation aborted",
		FinishReason: "aborted",
	}
	s.messages = append(s.messages, aborted)
	s.record("assistant", aborted)
}

// emit 投递一个事件，打上子 agent 深度标记，并把文本增量镜像到旧版 Out 写入器。
//
// 记录与投递被刻意分离：eventsMu 只保护追加操作，
// 因此缓慢的 emitFn（管道写入、SSE 刷新、UI 渲染）绝不会阻塞进度回调或轮次边界的记录。
// 投递由追加顺序票号门控，使 emitFn 观察到的事件顺序与追加顺序完全一致，即使有多个并发发出者。
func (s *runState) emit(event Event) {
	event.Depth = s.depth
	// 前端可能想显示"N 条排队"的边界事件会携带控制消息积压的快照。
	// 这是非抽取式的读取，因此报告数量绝不会消费排队的纠正消息。
	// 其他所有事件种类保持零值。Pending 是 nil 安全的，因此没有 steering 的 runState 也没问题。
	switch event.Kind {
	case EventTurnStart, EventTurnEnd, EventStop:
		if s.steer != nil {
			event.PendingMessages = s.steer.Pending()
		}
	}
	// sync.Once 使 emit 在未经 newRunState 构建的 runState 上仍可用。
	s.emitOnce.Do(func() { s.emitCond = sync.NewCond(&s.emitSendMu) })

	s.eventsMu.Lock()
	seq := s.emitSeq
	s.emitSeq++
	s.events = append(s.events, event)
	s.eventsMu.Unlock()

	s.emitSendMu.Lock()
	for s.emitDelivered != seq {
		s.emitCond.Wait()
	}
	if event.Kind == EventTextDelta && s.opts.Out != nil {
		_, _ = io.WriteString(s.opts.Out, event.Text)
	}
	if s.emitFn != nil {
		s.emitFn(event)
	}
	s.emitDelivered++
	s.emitCond.Broadcast()
	s.emitSendMu.Unlock()
}

// eventsSnapshot 在 eventsMu 下复制已记录的事件。调用方获得自己的切片，
// 因此 worker 之后的追加不会改变它们观察到的内容。
func (s *runState) eventsSnapshot() []Event {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return append([]Event(nil), s.events...)
}

// record 持久化一条记录项。recorder 失败绝不会停止运行。
func (s *runState) record(kind string, payload any) {
	if s.rec == nil {
		return
	}
	s.recordMu.Lock()
	_ = s.rec.Append(kind, payload)
	s.recordMu.Unlock()
}
