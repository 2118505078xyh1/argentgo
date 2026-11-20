package i18n

// errorMessageEntry 是单个错误码在两种语言环境下的本地化文案。
type errorMessageEntry struct {
	en string
	zh string
}

// errorMessageTable 把常见的规范错误码映射到本地化文案。
//
// 键刻意保持小写蛇形，与 provider / agent 返回的 code 对齐；这里
// 不复用 catalog，因为这些是机器可读的错误码而非 UI key，二者各自
// 演进，互不干扰。
var errorMessageTable = map[string]errorMessageEntry{
	"auth_required":     {"authentication required", "需要登录认证"},
	"invalid_api_key":   {"invalid API key", "API 密钥无效"},
	"credits_exhausted": {"credits exhausted", "额度已用尽"},
	"rate_limit":        {"rate limit exceeded", "请求过于频繁，请稍后重试"},
	"context_overflow":  {"context length exceeded", "上下文长度超出限制"},
	"network_error":     {"network error", "网络错误"},
	"timeout":           {"request timed out", "请求超时"},
	"permission_denied": {"permission denied", "没有权限"},
	"model_not_found":   {"model not found", "未找到模型"},
	"session_not_found": {"session not found", "未找到会话"},
	"tool_failed":       {"tool execution failed", "工具执行失败"},
	"config_error":      {"configuration error", "配置错误"},
	"cancelled":         {"canceled", "已取消"},
}

// errorMessage 返回错误码在给定语言环境下的本地化文案。
//
// locale 经 Normalize 解析，因此 "zh"、"zh_cn"、"中文" 等别名与
// "ZH-CN" 等大小写变体都得到中文文案，"en"、"English" 等得到英文
// 文案；未知或空的 locale 回退到 DefaultLocale。code 未命中映射表时
// 原样返回，使新增错误码在尚未翻译时仍然可见而非渲染为空白。
//
// 查找与返回都是整串复制，不做逐字节切分，所以含 CJK 的文案与 code
// 始终保持完整的 UTF-8 字节序列。
//
// 它是纯函数：不读取配置或全局状态，也不向 Missing 记录任何内容。
func errorMessage(locale, code string) string {
	entry, ok := errorMessageTable[code]
	if !ok {
		return code
	}
	if Normalize(locale) == ZhCN {
		return entry.zh
	}
	return entry.en
}
