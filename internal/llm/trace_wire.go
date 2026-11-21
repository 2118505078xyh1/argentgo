package llm

import (
	"time"
)

// recordTrace 是三个协议客户端共用的 trace 记录助手。它把"是否安装 sink"的
// 检查前置到任何脱敏/字符串处理之前，使默认（未安装 sink）路径零分配、零额外
// 工作——这是 debug trace 默认逐字节不变、且不影响性能的关键。
//
// 参数：
//   - protocol/model：本次请求的线协议与模型名（仅作诊断标签）。
//   - usage：本次回复的 usage；成功路径传 reply.Usage（可能为 nil），失败路径传 nil。
//   - start：Chat 方法开头的时间点，用于计算耗时。
//   - err：失败原因；nil 表示成功。
//   - okStatus：成功时记录的状态串（"ok"）。
//
// 流错误等失败路径拿不到部分累积的 usage，token 记为 0——这是诚实口径，
// 不做臆测填充。
func recordTrace(protocol, model string, usage *Usage, start time.Time, err error, okStatus string) {
	if currentTraceSink() == nil {
		return
	}
	prompt, completion, cached := 0, 0, 0
	if usage != nil {
		prompt = usage.PromptTokens
		completion = usage.CompletionTokens
		cached = usage.CachedTokens
	}
	status := okStatus
	if err != nil {
		status = RedactError(err)
	}
	RecordTrace(protocol, model, prompt, completion, cached, time.Since(start).Milliseconds(), status)
}
