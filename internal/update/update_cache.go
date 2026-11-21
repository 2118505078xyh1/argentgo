package update

import "time"

// updateCacheFresh 判断一次更新检查的缓存结果是否仍然新鲜（有效）。
// 当 now 与 lastCheck 之间的时间间隔非负，且严格小于 ttl 时返回 true。
//
// 边界语义：
//   - 间隔恰好等于 ttl 视为过期（false）；
//   - ttl 为零或负数时永远视为过期；
//   - now 早于 lastCheck（时间倒流、负间隔）时视为过期。
//
// 本函数为纯函数：只依赖参数，不读写全局状态，也不访问网络或磁盘。它不涉及
// 任何字符串处理，因此与 CJK 输入无关。间隔通过 time.Time.Sub 计算：当两个
// 时间都带有单调时钟读数时，Sub 优先使用单调时钟，不受系统墙钟被调整（NTP
// 校时、手动改时间等）的影响；缺少单调读数时则退化为墙钟差值。
func updateCacheFresh(lastCheck time.Time, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	elapsed := now.Sub(lastCheck)
	return elapsed >= 0 && elapsed < ttl
}
