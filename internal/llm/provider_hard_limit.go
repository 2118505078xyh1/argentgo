package llm

import "regexp"

// providerHardLimitPattern 识别供应方专属的硬性额度码。这些码表示账户或订阅
// 层面的额度已耗尽，重试无法在秒级窗口内消除，应立即停止重试。
//
// 与 retry.go 的 nonRetryableQuotaRE 不同，这里只针对明确的专属码，不处理
// 通用的 配额/额度/billing 措辞——那些词在限流提示里也很常见，交给现有的
// 分类器按上下文判断即可。
//
// 模式刻意保持保守：
//   - GoUsageLimit / FreeUsageLimit 是错误类型名的驼峰形式，允许可选分隔符，
//     并要求前导词边界，避免命中 "Congo usage limit" 这类普通文本。
//   - subscription_sharing 必须紧邻 usage limit，中间最多允许四个小写词段
//     （覆盖 workplace_individual 变体），从而排除可重试的
//     subscription_sharing_usage_unavailable / _user_unavailable。
//   - 1308 只在明确的错误码字段（"code":1308 / type=1308 / [1308] /
//     code 1308）或紧邻额度文案时才匹配，避免误伤 request id、时间戳、
//     字节数里的裸 1308；数字后要求边界，防止 13080 之类的更长数字。
const providerHardLimitPattern = `(?i)(` +
	`\b(?:go|free)[_-]?usage[_-]?limit` +
	`|subscription[_-]sharing(?:[_-][a-z]+){0,4}[_-]usage[_-]?limit` +
	`|"code"\s*:\s*"?1308\b` +
	`|type\s*[=:]\s*"?1308\b` +
	`|\[1308\]` +
	`|\bcode\s+1308\b` +
	`|(?:usage[_\s-]?limit|使用上限|限额|上限)[^\n]{0,24}?\b1308\b` +
	`|\b1308\b[^\n]{0,24}?(?:usage[_\s-]?limit|使用上限|限额|上限)` +
	`)`

// 包级只编译一次；匹配在脱敏后的文本上进行，因此凭据形态的值
// 无法影响判定。
var providerHardLimitRE = regexp.MustCompile(providerHardLimitPattern)

// isProviderHardLimit 报告一段提供方错误文本是否带有供应方专属的硬性额度码。
// msg 应为错误载荷的有界文本；函数内部会先脱敏，确保凭据形态的值
// 无法影响判定。这是纯函数：不读写任何状态，仅依据文本返回。
//
// 返回 true 表示应立即停止重试；返回 false 表示文本里没有专属码，
// 调用方仍可用现有的配额/限流分类器做进一步判断。
func isProviderHardLimit(msg string) bool {
	if msg == "" {
		return false
	}
	return providerHardLimitRE.MatchString(RedactText(msg))
}
