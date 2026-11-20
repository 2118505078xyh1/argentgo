package hooks

import "strings"

const (
	// maxContinuationBytes 限制 Stop 钩子续跑提示的最终字节上限。
	maxContinuationBytes = 1024
	// clipRunes 在末尾附加一个省略号（3 字节）并预留 1 字节给截断点，
	// 传入此上限可保证结果的字节数不超过 maxContinuationBytes。
	continuationClipLimit = maxContinuationBytes - len("…") + 1
)

// stopHookVerdict 根据 Stop 钩子的退出码与输出决定是否阻断本轮完成，
// 并在阻断时给出要注入下一轮的续跑提示。
//
// 这是一个纯函数：不读取外部状态、不访问时钟、不改动输入；给定相同参数
// 总是返回相同结果。判定规则如下：
//
//   - 退出码为 2 且 message 去除首尾空白后非空：阻断本轮完成，返回有界的续跑提示；
//   - 退出码为 2 但 message 为空或仅含空白：不阻断，续跑提示为空；
//   - 其它任何退出码（成功 0、普通失败 1、超时约定的 -1 等）：不阻断，续跑提示为空。
//
// 退出码是唯一的编码信号，函数从不解析 message 的内容，因此 CJK 等本地化
// 文本会被逐字节原样保留（仅在超过上限时于 rune 边界截断并加省略号）。
// 调用方应将返回的续跑提示作为下一轮的用户提示原样注入。
func stopHookVerdict(exitCode int, message string) (block bool, continuation string) {
	if exitCode != 2 {
		return false, ""
	}
	text := strings.TrimSpace(message)
	if text == "" {
		return false, ""
	}
	return true, clipRunes(text, continuationClipLimit)
}
