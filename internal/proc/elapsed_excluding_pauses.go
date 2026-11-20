package proc

import (
	"sort"
	"time"
)

// elapsedExcludingPauses 计算从 start 到 now 之间扣除所有暂停区间后的有效耗时。
//
// 暂停区间 pauses 按 [起点, 终点] 给出。审批或 elicitation 等待期间冻结的
// 那段时间不应计入命令耗时，本函数据此把各暂停区间从总耗时中剔除。
//
// 处理规则：
//   - 重叠的暂停区间只按并集扣除一次，避免重复扣减导致耗时被多算；
//   - 越过 start 或 now 边界的部分被裁到 [start, now] 窗口内，
//     落在窗口外的暂停不产生影响；
//   - 终点缺失（零值）或早于起点的区间视为“未闭合”，延续到 now，
//     表示此刻仍处于暂停中；
//   - now 早于 start（时间倒流或传入乱序时刻）时返回 0。
//
// 全程只用 time.Time 的 Before/After/Sub：当各时刻带有单调时钟读数时
// （例如都取自 time.Now()），计算基于单调时钟，不受 NTP 等墙上时钟校正影响。
// 函数只读入参：裁剪后的区间写入新切片，排序也只作用于该切片，
// 因此不会修改 pauses 及其中的任何元素。
func elapsedExcludingPauses(start time.Time, now time.Time, pauses [][2]time.Time) time.Duration {
	total := now.Sub(start)
	if total <= 0 {
		// 没有可计时的窗口：时间倒流或起止重合。
		return 0
	}

	// 先把每个有效暂停裁剪到 [start, now]，存入新切片，
	// 这样后续排序不会触碰调用方传入的 pauses。
	intervals := make([][2]time.Time, 0, len(pauses))
	for _, p := range pauses {
		ps, pe := p[0], p[1]
		if ps.IsZero() {
			// 没有起点，无法界定暂停区间，忽略。
			continue
		}
		if pe.IsZero() || pe.Before(ps) {
			// 未闭合：终点缺失或早于起点，视为延续到 now。
			pe = now
		}
		lo, hi := ps, pe
		if lo.Before(start) {
			lo = start
		}
		if hi.After(now) {
			hi = now
		}
		if hi.After(lo) {
			intervals = append(intervals, [2]time.Time{lo, hi})
		}
	}

	if len(intervals) == 0 {
		return total
	}

	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i][0].Equal(intervals[j][0]) {
			return intervals[i][1].Before(intervals[j][1])
		}
		return intervals[i][0].Before(intervals[j][0])
	})

	// 合并重叠（含首尾相接）的区间，求暂停的并集长度。
	var paused time.Duration
	curStart, curEnd := intervals[0][0], intervals[0][1]
	for _, iv := range intervals[1:] {
		if !iv[0].After(curEnd) {
			// 起点落在当前区间内或与之相接：合并。
			if iv[1].After(curEnd) {
				curEnd = iv[1]
			}
			continue
		}
		paused += curEnd.Sub(curStart)
		curStart, curEnd = iv[0], iv[1]
	}
	paused += curEnd.Sub(curStart)

	remaining := total - paused
	if remaining < 0 {
		// 防御性钳零：正常情况下并集不会超过窗口，
		// 这里保证即使传入异常时刻也不会返回负耗时。
		return 0
	}
	return remaining
}
