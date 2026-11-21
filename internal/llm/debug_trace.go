package llm

import "sync"

// debug_trace.go 提供请求级的调试追踪收集原语。
//
// 它刻意不接入三个协议客户端的 Chat 调用路径：默认没有安装收集器，
// RecordTrace 是 no-op，所有请求字节与新增本文件之前完全一致。
// 后续接线时，可在每个协议客户端构造请求、读取响应、统计 token 的各点
// 调用 RecordTrace，再由命令行层用 SetTraceSink 安装实际的收集器。
// 本文件只做内存中的记录，不执行任何 I/O。

// RequestTrace 是一次模型请求的、可安全记录的追踪摘要。
//
// 字段只覆盖协议、模型、token 计数、延迟与状态文本，绝不携带提示正文、
// 响应体或凭据材料。各字段含义：
//   - Protocol：线协议名（取值遵循本包的 Protocol* 常量）；
//   - Model：请求使用的模型名；
//   - PromptTokens / CompletionTokens / CachedTokens：提供方上报的 token 计数；
//   - LatencyMS：本次请求的耗时，单位毫秒；
//   - Status：写入前已经 RedactText 脱敏的状态或错误文本；
//   - Redacted：Status 在写入前是否已完成脱敏（始终为真，便于下游显示"已脱敏"标记）。
type RequestTrace struct {
	Protocol         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	LatencyMS        int64
	Status           string
	Redacted         bool
}

// TraceSink 接收请求追踪记录。
//
// RecordTrace 会在本包持有的锁之外调用 Record，因此实现者必须自行保证
// Record 可并发调用；实现者也可以在 Record 内回调 SetTraceSink 而不会死锁。
type TraceSink interface {
	Record(RequestTrace)
}

// traceSinkMu 保护 traceSink 的并发读写。
var traceSinkMu sync.RWMutex

// traceSink 是当前安装的追踪收集器；nil 表示未安装（默认）。
// 零值即关闭追踪，因此默认请求与新增该原语之前逐字节一致。
var traceSink TraceSink

// SetTraceSink 安装一个追踪收集器，用于接收后续每个请求的摘要。
//
// 该能力是 opt-in 的：不调用本函数时不产生任何追踪记录，默认逐字节不变。
// 传入 nil 等价于清除（回到默认关闭状态）；再次调用会整体替换先前安装的
// 收集器。本函数并发安全。
func SetTraceSink(s TraceSink) {
	traceSinkMu.Lock()
	defer traceSinkMu.Unlock()
	traceSink = s
}

// currentTraceSink 返回当前安装的追踪收集器；未安装时返回 nil。
// 并发安全。
func currentTraceSink() TraceSink {
	traceSinkMu.RLock()
	defer traceSinkMu.RUnlock()
	return traceSink
}

// RecordTrace 记录一次请求追踪。未安装收集器时是完全的 no-op：不分配、
// 不脱敏、不触碰任何状态，默认逐字节不变。
//
// status 参数携带状态或错误文本，在写入记录前一定经过 RedactText 脱敏，
// 只有脱敏后的文本进入 Status，原始文本不会落进追踪；记录的 Redacted 为 true。
// 本函数不修改任何入参（各参数均按值传入，脱敏结果写入新的 RequestTrace）。
// 本函数并发安全：它先读取一份收集器快照，再在锁外调用 Record，
// 因此不会在回调期间持有锁，收集器内部回调 SetTraceSink 也不会死锁。
func RecordTrace(protocol, model string, prompt, completion, cached int, latencyMS int64, status string) {
	sink := currentTraceSink()
	if sink == nil {
		return
	}
	sink.Record(RequestTrace{
		Protocol:         protocol,
		Model:            model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
		LatencyMS:        latencyMS,
		Status:           RedactText(status),
		Redacted:         true,
	})
}
