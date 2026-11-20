// Package provider 端点健康探测。
//
// 本文件移植 opencode 的 vLLM provider 健康检查中的端点过滤部分，不含
// 任何 I/O：无 HTTP 客户端、无计时器、无副作用。调用方拥有实际的探测（对 <baseURL>/health 的 HTTP GET、
// 本文件移植 opencode 的 vLLM provider 健康检查中的端点过滤部分，不含任何 I/O：无 HTTP 客户端、无计时器、无副作用。调用方拥有实际的探测（对 <baseURL>/health 的 HTTP GET、
// TCP 拨号或其他任何方式）并把它作为函数注入。ProbeHealthy 将该策略应用于候选 base URL 列表并返回回答健康的子集。保持策略纯函数使其确定、
// 将该策略应用于候选 base URL 列表并返回回答健康的子集。保持策略纯函数使其
// 确定、保序且可从任何 goroutine 安全调用。
// 保序且可从任何 goroutine 安全调用。
package provider

// ProbeHealthy 把 baseURLs 过滤为通过注入探测的端点。
// probe.
//
// ProbeHealthy 把 baseURLs 过滤为通过注入探测的端点。对每个唯一 URL，按输入顺序，probe(url) 恰好调用一次：nil 错误标记端点健康并保留，任何非 nil 错误丢弃
// nil 错误标记端点健康并保留，任何非 nil 错误丢弃
// 它。重复 URL 只探测一次且在结果中最多出现一次，位于
// 它。重复 URL 只探测一次且在结果中最多出现一次，位于其第一次出现的位置。返回的切片是全新
// 分配的且绝不与 baseURLs 共享底层数组；nil 或空输入产生空（非 nil）切片。
// （非 nil）切片。
//
// probe 始终在调用 goroutine 上顺序调用，因此它不必并发安全；只需可重复调用。nil
// 不必并发安全；只需可重复调用。nil
// 探针是无操作过滤器：每个唯一 URL 都被视为健康，这对只想去重很有用。
// 这对只想去重很有用。
func ProbeHealthy(baseURLs []string, probe func(url string) error) []string {
	seen := make(map[string]struct{}, len(baseURLs))
	healthy := make([]string, 0, len(baseURLs))
	for _, url := range baseURLs {
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		if probe == nil || probe(url) == nil {
			healthy = append(healthy, url)
		}
	}
	return healthy
}
