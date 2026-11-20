package proc

import "time"

// idleWatchdogExpired 判断从 lastActivity 到 now 的静默时长是否严格超过
// idleTimeout。与只看总耗时的整体超时不同，这里衡量的是“距上次活动有多久”：
// 一个持续产出输出的长时间命令，只要每次输出都会刷新 lastActivity，
// 就永远不会被误判为超时。
//
// idleTimeout <= 0 表示看门狗关闭，函数恒返回 false。这与 Command.Timeout
// 中“非正值即不设超时”的约定一致：负值不会回退成一个极小的正阈值而
// 把仍在活动的进程立刻判死。
//
// 静默时长恰好等于 idleTimeout（边界）时不判过期，只有严格大于才算；
// now 早于 lastActivity（墙上时钟倒流或传入乱序时刻）时差值为负，
// 同样不判过期。
//
// 计算走 time.Time.Sub：当 lastActivity 与 now 都带有单调时钟读数时
// （例如都取自 time.Now()），Sub 会基于单调时钟求值，不受 NTP 等
// 墙上时钟校正的影响。这里刻意不改成 UnixNano 相减，
// 因为那会丢掉单调读数，使时钟回拨被误判成“长期无活动”。
func idleWatchdogExpired(lastActivity time.Time, now time.Time, idleTimeout time.Duration) bool {
	if idleTimeout <= 0 {
		return false
	}
	return now.Sub(lastActivity) > idleTimeout
}
