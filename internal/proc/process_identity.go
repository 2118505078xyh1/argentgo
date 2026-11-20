package proc

// DefaultStartTimeToleranceMS 是记录的时间与观测到的进程启动时间之间
// 允许的偏差。它与 argent 守护进程使用的容差一致：大到足以吸收
// 启动时间/时钟读取的偏差，小到足以检测几秒后 PID 被
// 复用为另一个实例。
const DefaultStartTimeToleranceMS int64 = 1000

// ProcessStartTime 以自 Unix epoch 起的毫秒数返回 pid 的启动时间，
// 若进程已消失、无法查询或平台没有探测手段，则返回错误。
// 该值是一个身份令牌：同一 PID 的两个实例带有不同的值，
// 这正是让调用方检测 PID 复用、而不是信任裸数字的
// 依据。
func ProcessStartTime(pid int) (int64, error) {
	return processStartTime(pid)
}

// IsSameProcess 报告 pid 是否存活，且仍是 startedAt 时刻启动的那个
// 实例。startedAt <= 0 表示"未知"：仍会执行存活探测，但身份
// 无法被否证，因此接受存活的 pid。
func IsSameProcess(pid int, startedAt int64, toleranceMS int64) bool {
	observed, err := ProcessStartTime(pid)
	if err != nil {
		// 缺失/不可读的进程绝不可能是我们记录的那个。
		return false
	}
	if startedAt <= 0 {
		return true
	}
	return withinStartTimeTolerance(observed, startedAt, toleranceMS)
}

// withinStartTimeTolerance 是纯比较核心。把它与 OS 探测分开，
// 就能在不依赖活进程或宿主时钟的情况下测试边界行为。
// a live process or on the host clock.
func withinStartTimeTolerance(observed, startedAt, toleranceMS int64) bool {
	if toleranceMS < 0 {
		toleranceMS = 0
	}
	// 在对操作数排序后取无符号域的差值绝对值：
	// 这样既避免 int64 端点处的有符号溢出，又不会把
	// 值域当作环（普通的 uint64 减法会把相差 2^63 的
	// 两个值报告为 1）。
	var diff uint64
	if observed >= startedAt {
		diff = uint64(observed) - uint64(startedAt)
	} else {
		diff = uint64(startedAt) - uint64(observed)
	}
	return diff <= uint64(toleranceMS)
}
