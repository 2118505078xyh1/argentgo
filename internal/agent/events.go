package agent

import (
	"time"

	"example.com/argentgo/internal/llm"
)

// EventKind 命名一次运行中的一个可观察步骤。
type EventKind string

// 前端渲染的事件词汇表。文本和推理增量随提供方的流式输出到达；
// 工具事件框住一次调用；用量事件报告单次请求的增量以及运行总量。
const (
	EventTurnStart       EventKind = "turn_start"
	EventTurnEnd         EventKind = "turn_end"
	EventCheckpointReady EventKind = "checkpoint_ready"
	EventTextDelta       EventKind = "text_delta"
	EventReasoningDelta  EventKind = "reasoning_delta"
	EventToolStart       EventKind = "tool_start"
	EventToolEnd         EventKind = "tool_end"
	EventUsage           EventKind = "usage"
	EventError           EventKind = "error"
	EventNotice          EventKind = "notice"
	EventStop            EventKind = "stop"
	EventSubagentStart   EventKind = "subagent_start"
	EventSubagentEnd     EventKind = "subagent_end"
	EventRetry           EventKind = "retry"
	// EventBudgetReminder 报告一条模型可见的上下文预算提醒。提醒本身会作为用户轮次
	// 追加到对话中；该事件只是让前端把同样的文本呈现给操作人员。
	EventBudgetReminder EventKind = "budget_reminder"
	// EventPartial 携带模型在提供方错误或取消使其提前中断之前成功流式输出的前缀。
	// 它是附加性的：不认识该种类的消费者只需忽略它，运行仍以恰好一个 EventStop 作为终结。
	EventPartial EventKind = "partial"
	// EventToolArgDelta 在模型仍在流式生成工具参数时逐片发布，携带调用 id 与片段。
	// 它是 live-only 事件：只经事件通道投递，绝不写入 JSONL；tool_start/tool_end
	// 仍承载完整参数，形状不变。
	EventToolArgDelta EventKind = "tool_arg_delta"
)

// RetryInfo 是 EventRetry 可安全上线传输的负载。它是一组封闭的小型标量：
// 响应体、请求头或形似凭据的提供方文本都不会被复制到这里。
// 前端可以据此绘制"第 N/M 次重试"的进度和退避倒计时，而无需解析模型的错误字符串。
type RetryInfo struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	DelayMS     int64  `json:"delay_ms,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
}

// RetryInfoFromAttempt 把经过脱敏的 llm 重试决定映射到 EventRetry 的线上负载。
// 退避由 llm 层掌管，因此延迟由调用方提供（RetryPolicy.Delay）；
// 只从决定中复制封闭的 Reason 标记，绝不复制自由格式的提供方文本 Cause。
//
// 预期调用方是 app 层：它已经安装了 llm 的 AttemptHook，并构建了协议客户端和 agent Runner 的 Emit，
// 因此它可以在这里桥接每条 AttemptRetry 记录，以补充 agent 自身发出的模型切换重试，
// 实现逐次退避的进度展示。
func RetryInfoFromAttempt(a llm.RetryAttempt, model string, maxAttempts int, delay time.Duration) RetryInfo {
	return RetryInfo{
		Attempt:     a.Attempt,
		MaxAttempts: maxAttempts,
		DelayMS:     delay.Milliseconds(),
		Reason:      string(a.Reason),
		Model:       model,
	}
}

// Event 是运行事件流中的一项。
//
// 只有对 Kind 有意义的字段才会被填充，因此渲染器可以按 Kind 分支并直接读取其余字段，
// 无需进一步检查：
//
//	turn_start / turn_end    Turn
//	checkpoint_ready         Turn、Checkpoint（在变更型批次之前发出）
//	text_delta               Turn、Text
//	reasoning_delta          Turn、Text（完整的推理块）、StartedAtMS、FinishedAtMS
//	tool_start               Turn、ToolCallID、ToolName、Args、StartedAtMS
//	tool_end                 Turn、ToolCallID、ToolName、Output、IsError、DurationMS、StartedAtMS、FinishedAtMS
//	usage                    Turn、Usage（本次请求）、TotalUsage（运行总量）
//	error                    Err、Failure（脱敏后的分类；可能为 nil）
//	notice                   Text
//	stop                     Reason
//	subagent_start           Text（任务标签）、ToolName
//	subagent_end             Text（任务标签）、Output、IsError、Err
//	retry                    Turn、Retry（尝试次数、上限、退避延迟、原因、模型）
//	partial                  Turn、Text、Partial、PartialStopReason、PartialModel、PartialTruncated
type Event struct {
	Kind       EventKind
	Turn       int
	Depth      int // 子 agent 嵌套深度；根运行为 0
	Text       string
	ToolCallID string
	ToolName   string
	Args       string
	// Intent 是模型附加到工具调用上的简洁 `i` 字符串。它填充在 tool_start / tool_end 上，
	// 以便事后解释冗长或畸形的调用；对其他所有事件种类保持为空。
	Intent     string
	Output     string
	IsError    bool
	DurationMS int64
	Usage      llm.Usage
	TotalUsage llm.Usage
	Err        string
	// Failure 是 Err 经过脱敏后的分类，只在 EventError 上填充。
	// 它携带固定的 ErrorID 和路由提示（Retryable / UserAction），
	// 使前端无需解析错误文本，就能把可重试的失败发回模型，或把设置类失败导向凭据/模型界面。
	// 对其他所有事件种类，以及直接构造 EventError 的生产者，它都保持 nil，
	// 使其 JSON 逐字节不变。
	Failure *llm.FailureInfo `json:"failure,omitempty"`
	Reason  string
	// PendingMessages 是当前排队的控制消息数量。它填充在 turn_start / turn_end / stop 事件上，
	// 使前端无需清空 steering 通道即可显示"N 条排队"的提示；
	// 对其他所有事件种类保持为零。
	PendingMessages int
	// Checkpoint 只在 EventCheckpointReady 上填充。它携带会话检查点在变更型批次开始前
	// 所需的信息。
	Checkpoint *Checkpoint
	// Retry 只在 EventRetry 上填充。它以可安全上线传输的形式携带一个重试决定。
	// 对其他所有事件种类，以及直接构造 EventRetry 的生产者，它都保持 nil，
	// 使其 JSON 逐字节不变。
	Retry *RetryInfo `json:"retry,omitempty"`
	// StartedAtMS 和 FinishedAtMS 是工具或推理步骤的墙上时钟区间，以 Unix 纪元毫秒表示。
	// 它们只在 tool_start（仅起点）、tool_end 和 reasoning_delta 事件上填充。
	// 零值表示"无计时"，在 JSON 中会被省略，因此不带计时的事件编码保持逐字节不变。
	// 与相对的 DurationMS 不同，这些绝对时间戳在会话重载后仍然有效。
	StartedAtMS  int64 `json:"started_at_ms,omitempty"`
	FinishedAtMS int64 `json:"finished_at_ms,omitempty"`

	// 以下字段是附加性的，只在 EventPartial 上填充。它们都使用 omitempty，
	// 因此每个既有事件都保持逐字节相同的 JSON。当 Text 是被提供方错误或取消所截断的回复的
	// 捕获前缀、而非完整答案时，Partial 为 true。
	Partial bool `json:"partial,omitempty"`
	// PartialStopReason 是运行将在终结停止时报告的原因（StopCancelled 或 StopProviderError），
	// 使 UI 能用与停止事件上已显示原因相同的文字来标记该前缀。
	PartialStopReason string `json:"partial_stop_reason,omitempty"`
	// PartialModel 是失败或被取消时正在进行流式输出的模型。
	// 在回退链上它是最后一次尝试的模型，也就是 Text 中前缀所属的模型。
	PartialModel string `json:"partial_model,omitempty"`
	// PartialTruncated 报告捕获的前缀达到了字节上限，
	// 使 UI 能把显示的文本标记为不完整。
	PartialTruncated bool `json:"partial_truncated,omitempty"`

	// 以下字段只在 EventToolArgDelta 上填充。它们恒带 omitempty，
	// 因此既有事件（包括全零值事件）的 JSON 字节保持逐字节不变。
	// ArgDelta 是本次到达的参数原始片段，按到达顺序拼接即完整参数。
	ArgDelta string `json:"arg_delta,omitempty"`
	// ToolArgIndex 是多路并发调用中的槽位编号，与 ToolCallID 一起定位所属调用。
	ToolArgIndex int `json:"tool_arg_index,omitempty"`
}

// IsTerminal 报告 e 是否结束一次运行，因而是必须投递的：即使订阅方的缓冲区已满，
// 传输层也不能丢弃它，否则等待完成信号的无界面客户端会挂起。
//
// EventStop 是唯一的运行终结事件。EventTurnEnd 只是每个轮次的边界（一次运行可发出多个），
// 刻意不是终结事件。
func (e Event) IsTerminal() bool {
	return e.Kind == EventStop
}
