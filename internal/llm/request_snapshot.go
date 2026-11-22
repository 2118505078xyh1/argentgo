package llm

import "context"

// 本文件提供"把实际发送的请求字节交给调用方"的 opt-in 钩子。
// 迁移自 killi 决策 21：日志记录的就是真正发给提供方的字节，而不是重建的语义消息。
//
// 默认 ctx 上没有收集器，recordRequestSnapshot 是完全的 no-op：不分配、不拷贝，
// 三个协议客户端的请求字节与未引入本文件之前逐字节一致。用 context 而非全局
// 状态，使并发的 subagent 各自携带自己的收集器而互不干扰。

type requestSnapshotSinkKey struct{}

// RequestSnapshotSink 接收一次请求的线协议字节。
// protocol 取本包的 Protocol* 常量；model 是实际请求使用的模型名；body 是
// json.Marshal 的结果。实现者若需持久化，必须在返回前自行拷贝 body。
type RequestSnapshotSink func(protocol, model string, body []byte) error

// WithRequestSnapshotSink 把快照收集器挂到 ctx，供该次运行的请求使用。
// 传入 nil 等价于不挂载（回到默认关闭状态）。
func WithRequestSnapshotSink(ctx context.Context, sink RequestSnapshotSink) context.Context {
	return context.WithValue(ctx, requestSnapshotSinkKey{}, sink)
}

// recordRequestSnapshot 把一次请求字节交给 ctx 上的收集器。
// 未挂载收集器时立即返回，零副作用。
func recordRequestSnapshot(ctx context.Context, protocol, model string, body []byte) {
	sink, _ := ctx.Value(requestSnapshotSinkKey{}).(RequestSnapshotSink)
	if sink == nil {
		return
	}
	// 收集器的错误不影响请求本身：快照是可观测能力，绝不能让落盘失败阻断模型调用。
	_ = sink(protocol, model, body)
}
