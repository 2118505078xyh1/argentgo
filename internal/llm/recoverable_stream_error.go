package llm

import (
	"regexp"
	"strings"
)

// recoverableStreamErrorPattern 识别"明确的流中断"文本：提供方已接受请求、
// 答复正在流式传输出来时被中途切断。这类失败重放同一请求通常能成功，
// 因此应纳入可重试判定。
//
// 模式在大小写不敏感模式下编译，并同时覆盖英文与中文文案。刻意保持保守：
//   - "stream"/"connection" 之后必须紧跟中断类措辞，因此 "stream error"（通用
//     哨兵）、"streaming supported"、"connection refused"、"connection timed out"
//     都不会命中；
//   - "cut short" 作为独立短语时也要求两侧不是字母，避免误伤 "shortcut" 之类；
//   - 不覆盖 "truncated due to length" / "max tokens" 这类由请求本身引起的截断，
//     它们属于上下文溢出或普通长度错误，原样重试不会成功；
//   - 中文要求"流/响应/输出/连接"与"中断/断开/关闭/截断"等成对出现。
const recoverableStreamErrorPattern = `(?i)(` +
	// 英文：流被中断、断开、中止、丢弃或过早结束（含 was/got/being 被动形式）。
	`stream[_\s-]*(?:was[_\s-]+|got[_\s-]+|being[_\s-]+)?(?:interrupt(?:ed|ion)?|disconnect(?:ed|ion)?|abort(?:ed|ion)?|drop(?:ped)?|break|broke|end(?:ed)?[_\s-]+(?:early|prematurely|unexpectedly|without)|cut[_\s-]+short|terminat(?:ed|ion)[_\s-]+(?:early|prematurely|unexpectedly))` +
	// 英文错误码：上游流读取错误，允许下划线、空格或连字符分隔。
	`|upstream[_\s-]*stream[_\s-]*read[_\s-]*error` +
	// 英文：流式传输中途连接/会话被关闭或断开。
	`|(?:connection|session)[_\s-]*(?:was[_\s-]+|got[_\s-]+|being[_\s-]+)?(?:closed|close|disconnected|disconnect|dropped|terminated)` +
	// 英文：响应/输出被截短，以及不带主语的 "cut short"。
	`|(?:response|output|answer|reply|completion|generation)[_\s-]*(?:was[_\s-]+|got[_\s-]+)?cut[_\s-]+short` +
	`|(?:^|[^A-Za-z])cut[_\s-]+short(?:$|[^A-Za-z])` +
	// 英文：流意外结束 / 流不完整。
	`|(?:unexpected[_\s-]+)?(?:end|eof)[_\s-]+of[_\s-]+(?:the[_\s-]+)?stream` +
	`|incomplete[_\s-]+stream` +
	// 中文：流/流式响应/输出/答复被中断、断开、关闭或截断。
	`|(?:流式(?:响应|输出|回复)?|流|响应|输出|答复)(?:被|已|意外|异常)*(?:中断|断开|截断|终止|中止|关闭|不完整|未完)` +
	// 中文：连接被关闭/断开/中断。
	`|连接(?:被|已|意外|异常)*(?:关闭|断开|中断|终止|中止)` +
	// 中文：上游流读取错误/失败/异常。
	`|上游(?:流式?)?(?:读取|读)?(?:错误|失败|异常)` +
	`)`

// 模式在包级只编译一次；匹配在脱敏后的文本上进行，确保凭据形态的值
// 不会因恰好含有中断词而误触发判定。
var recoverableStreamErrorRE = regexp.MustCompile(recoverableStreamErrorPattern)

// isRecoverableStreamError 报告一段错误文本是否描述"可重试的流中断"：
// 提供方在开始输出后被中途切断，重放同一请求通常能成功。
//
// msg 应为错误载荷的有界文本。这是纯函数：不读写任何状态，仅依据文本返回，
// 内部先做 TrimSpace 与脱敏；空串、纯空白或不含明确中断措辞的普通错误一律返回 false。
// 返回 true 仅表示文本命中明确的流中断，调用方仍需结合自身策略决定是否重试。
func isRecoverableStreamError(msg string) bool {
	if strings.TrimSpace(msg) == "" {
		return false
	}
	return recoverableStreamErrorRE.MatchString(RedactText(msg))
}
