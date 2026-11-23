package llm

import "context"

// RawChatter 是由真实协议客户端实现的可选接口：发送一段已经序列化好的线协议
// 请求体（通常来自之前记录的快照），而不是从 messages/tools 重新构造。
//
// 用途是 resume 的字节级回放：把当初实际发送的 body 原样重发，保持提供方缓存
// 前缀命中（见 killi 决策 21）。它是可选接口——fake Chatter 无需实现，agent
// 用 type assertion 探测，未实现时完全走原路径。
//
// 实现者必须：
//   - 直接发送 body，不得重新序列化，也不得再次调用 recordRequestSnapshot
//     （否则 resume 会写出一条内容相同的重复快照）；
//   - 走与流式 Chat 相同的 readStream 管线（快照 body 里 stream=true）；
//   - 校验失败/不支持时由调用方回退到 Chat，本方法不负责降级。
type RawChatter interface {
	ChatRaw(ctx context.Context, model string, body []byte, onText func(string)) (Message, error)
}
