package llm

import "strings"

const (
	// defaultContentFilterGuidance 是内容审核失败后写回会话历史的默认引导文字。
	// 即使提供方没有给出任何细节，这段文字本身也是一条完整的、可执行的指令，
	// 使模型把"审核拦截"理解为"需要调整措辞后重试"，而不是直接终止的信号。
	defaultContentFilterGuidance = "请调整措辞后重试，避免触发内容审核"

	// contentFilterGuidanceMaxDetail 限制从原始错误中提取的摘录长度（按 rune 计）。
	// 提供一个上界，防止超长的提供方响应体或整段被拦截的内容进入历史记录。
	contentFilterGuidanceMaxDetail = 120
)

// contentFilterGuidance 把一条内容审核错误转换为可操作的引导文字，供调用方
// 写回会话历史后让模型调整措辞重试。它是纯函数：不修改状态、不发事件，
// 也不改变任何分类或重试行为；是否接入循环由调用方依据产品策略决定。
//
// 行为：
//   - msg 为空或仅含空白时，返回默认引导。
//   - 否则从 msg 中提取一段有界、脱敏且按 rune 截断（不会切断 CJK/UTF-8 字符）
//     的摘录，附在默认引导之后的括号里，让模型知道具体是哪段措辞触发了审核。
//   - 无论提供方文案如何，返回值始终以默认引导开头，因此模型收到的一致是
//     可操作的下一步，而不是一个终止失败。
//
// 摘录会先经过 RedactText 去除凭据形态的值，因此密钥、邮箱等绝不会通过
// 这段文字进入历史或日志。
func contentFilterGuidance(msg string) string {
	detail := contentFilterDetail(msg)
	if detail == "" {
		return defaultContentFilterGuidance
	}
	return defaultContentFilterGuidance + "（提供方提示：" + detail + "）"
}

// contentFilterDetail 提取并清理原始错误中可供模型参考的摘录。
// 返回空串表示没有可用细节（此时调用方只返回默认引导）。
func contentFilterDetail(msg string) string {
	text := strings.TrimSpace(msg)
	if text == "" {
		return ""
	}
	// 先把换行/制表等字符归一为空格，避免摘录破坏历史记录的单行结构。
	text = strings.Join(strings.Fields(RedactText(text)), " ")
	if text == "" {
		return ""
	}
	return clipRedacted(text, contentFilterGuidanceMaxDetail)
}
