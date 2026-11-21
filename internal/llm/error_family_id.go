package llm

// errorFamilyID 把一个 ActionableError 家族映射为稳定的机器可读标识，
// 供遥测聚合使用（telemetry_type）。它是一个纯函数：只依赖入参，不读取
// 任何可变状态，也不产生副作用。
//
// 为什么需要独立于 Family 的函数：Family 本身是内部词汇表，未来可能因
// 重构而改名或新增取值；遥测标识必须与它解耦，否则改动内部字符串就会
// 悄悄改变仪表盘上的聚合维度。这里用显式白名单把每个已知家族固定到一个
// 小写 ASCII 标识，任何未知取值（包括 CJK 文本、大小写变体或未来新增的
// 家族）都统一折叠为 "unknown"，从而保证输出的取值集合封闭且稳定。
//
// 目标是"稳定"而非"信息最全"：调用方若要细分，可另外记录 Family；
// 这里的标识只保证跨版本、跨语言、跨提供方的一致聚合。
func errorFamilyID(family string) string {
	switch family {
	case FamilyNotConfigured:
		return "not_configured"
	case FamilyAuth:
		return "auth"
	case FamilyQuotaExhausted:
		return "quota_exhausted"
	case FamilyRateLimit:
		return "rate_limit"
	case FamilyContentFilter:
		return "content_filter"
	case FamilyContextOverflow:
		return "context_overflow"
	case FamilyModelNotFound:
		return "model_not_found"
	case FamilyNetwork:
		return "network"
	case FamilyServer:
		return "server"
	case FamilyUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}
