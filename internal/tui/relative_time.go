package tui

import (
	"strconv"
	"time"
)

// relativeTimeDay 是相对时间文案里"天"的长度。
const relativeTimeDay = 24 * time.Hour

// relativeTimeText 返回 then 相对于 now 的中文相对时间文案。
//
// 按经过时长 d = now - then 分档：
//   - d < 1 分钟："刚刚"
//   - 1 分钟 <= d < 1 小时："N 分钟前"
//   - 1 小时 <= d < 24 小时："N 小时前"
//   - d >= 24 小时："N 天前"
//
// N 为向下取整的整数。负时间（then 晚于 now）与极小正数一样收敛到"刚刚"，
// 因此不会出现"-3 分钟前"这类无意义文案。
//
// 这是纯函数：不读写任何全局状态、不依赖区域设置或时区，
// 同一对时刻永远得到逐字节相同的结果。
func relativeTimeText(then, now time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + " 分钟前"
	case d < relativeTimeDay:
		return strconv.FormatInt(int64(d/time.Hour), 10) + " 小时前"
	default:
		return strconv.FormatInt(int64(d/relativeTimeDay), 10) + " 天前"
	}
}
