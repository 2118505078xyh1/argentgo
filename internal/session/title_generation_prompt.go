package session

import (
	"strconv"
	"strings"

	"example.com/argentgo/internal/security"
)

const (
	// titleTargetRunes 是标题生成指令要求的最多字数。
	titleTargetRunes = 20

	// titlePromptMessageLimit 限制提示中携带的首条消息长度（按符文计），
	// 使提示体积有界，避免超长首条消息耗光模型上下文。
	titlePromptMessageLimit = 400
)

// titleGenerationPrompt 构造供模型异步生成会话标题的中文提示。
//
// 它是纯函数：不读取全局状态、不修改入参，相同输入总得到相同输出。
// 首条用户消息先经脱敏，再由 truncateRunes 折叠为单行并按符文安全截断，
// 因此密钥不会进入提示、提示长度有界，且不会把 CJK 或任意 UTF-8 字符切成两半。
func titleGenerationPrompt(firstUserMessage string) string {
	message := truncateRunes(security.Redact(firstUserMessage), titlePromptMessageLimit)

	var b strings.Builder
	b.WriteString("请为下面这段对话生成标题。要求：用不超过 ")
	b.WriteString(strconv.Itoa(titleTargetRunes))
	b.WriteString(" 字概括对话主题；只输出标题本身，不要添加引号、解释或其他内容。\n\n")
	b.WriteString("对话的首条用户消息：\n")
	b.WriteString(message)
	return b.String()
}
