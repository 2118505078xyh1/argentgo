package llm

// usage_collect.go 提供从消息转录中汇总 token 用量的纯原语。
//
// 它不触碰任何客户端状态、不执行 I/O，也不接入协议客户端的 Chat 路径；
// 由调用方（如 agent 循环或追踪接线层）在持有 []Message 时显式调用。
// CollectUsage 只读取 msg.Usage，绝不修改入参，结果仅取决于入参内容。

// UsageTotals 是一组消息 token 用量的汇总。
//
// 字段语义与 Usage 保持一致：
//   - PromptTokens 是完整提示成本（新增 token 加缓存 token），缓存细分仅供参考；
//   - CompletionTokens 是补全 token 数；
//   - CachedTokens 是 PromptTokens 中由提示缓存提供的部分，仅作参考；
//   - TotalTokens 是可计费 token 总数，口径与 Usage.TotalTokens() 一致，
//     即 PromptTokens + CompletionTokens，缓存 token 只通过 PromptTokens 计一次，
//     不重复累加。
type UsageTotals struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	TotalTokens      int
}

// CollectUsage 汇总 msgs 中每条消息的 Usage。
//
// Usage 为 nil 的消息被跳过。各字段逐条累加；TotalTokens 采用每条消息的
// Usage.TotalTokens()（缓存 token 不重复计入），因此等于汇总后的
// PromptTokens + CompletionTokens。msgs 及其元素不被修改。
// 本函数是纯函数：相同输入始终得到相同输出，与文本内容（含 CJK）无关。
func CollectUsage(msgs []Message) UsageTotals {
	var totals UsageTotals
	for i := range msgs {
		u := msgs[i].Usage
		if u == nil {
			continue
		}
		totals.PromptTokens += u.PromptTokens
		totals.CompletionTokens += u.CompletionTokens
		totals.CachedTokens += u.CachedTokens
		totals.TotalTokens += u.TotalTokens()
	}
	return totals
}

// TraceFromUsage 把用量汇总转换为一条可安全记录的 RequestTrace。
//
// 复用 debug_trace.go 的 RequestTrace 类型：协议与模型原样写入，三个 token
// 计数取自 u，延迟与状态取自入参。RequestTrace 要求 Status 在写入前已经
// 完成脱敏且 Redacted 为真，因此这里对 status 调用 RedactText，保证把结果
// 直接交给 TraceSink.Record 时同样满足该契约（与 RecordTrace 行为一致）。
// u 与各字符串入参均不被修改。
func TraceFromUsage(protocol, model string, u UsageTotals, latencyMS int64, status string) RequestTrace {
	return RequestTrace{
		Protocol:         protocol,
		Model:            model,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CachedTokens:     u.CachedTokens,
		LatencyMS:        latencyMS,
		Status:           RedactText(status),
		Redacted:         true,
	}
}
