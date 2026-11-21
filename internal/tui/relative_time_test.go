package tui

import (
	"testing"
	"time"
)

// relativeTimeBase 返回测试用的固定基准时刻，不含单调时钟读数。
func relativeTimeBase() time.Time {
	return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
}

// TestRelativeTimeTextJustNow 验证不足一分钟以及零时长都返回"刚刚"。
func TestRelativeTimeTextJustNow(t *testing.T) {
	now := relativeTimeBase()
	for _, d := range []time.Duration{
		0,
		time.Nanosecond,
		time.Second,
		59 * time.Second,
		time.Minute - time.Nanosecond,
	} {
		if got := relativeTimeText(now.Add(-d), now); got != "刚刚" {
			t.Errorf("经过 %v：得到 %q，期望 %q", d, got, "刚刚")
		}
	}
}

// TestRelativeTimeTextMinutes 验证分钟档的取整与范围。
func TestRelativeTimeTextMinutes(t *testing.T) {
	now := relativeTimeBase()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{time.Minute, "1 分钟前"},
		{90 * time.Second, "1 分钟前"},
		{2*time.Minute + 30*time.Second, "2 分钟前"},
		{59*time.Minute + 59*time.Second, "59 分钟前"},
	} {
		if got := relativeTimeText(now.Add(-tc.d), now); got != tc.want {
			t.Errorf("经过 %v：得到 %q，期望 %q", tc.d, got, tc.want)
		}
	}
}

// TestRelativeTimeTextHours 验证小时档的取整与范围。
func TestRelativeTimeTextHours(t *testing.T) {
	now := relativeTimeBase()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1 小时前"},
		{90 * time.Minute, "1 小时前"},
		{2*time.Hour + 59*time.Minute, "2 小时前"},
		{23*time.Hour + 59*time.Minute, "23 小时前"},
	} {
		if got := relativeTimeText(now.Add(-tc.d), now); got != tc.want {
			t.Errorf("经过 %v：得到 %q，期望 %q", tc.d, got, tc.want)
		}
	}
}

// TestRelativeTimeTextDays 验证天档的取整与范围。
func TestRelativeTimeTextDays(t *testing.T) {
	now := relativeTimeBase()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{relativeTimeDay, "1 天前"},
		{relativeTimeDay + time.Hour, "1 天前"},
		{2 * relativeTimeDay, "2 天前"},
		{30 * relativeTimeDay, "30 天前"},
		{365 * relativeTimeDay, "365 天前"},
	} {
		if got := relativeTimeText(now.Add(-tc.d), now); got != tc.want {
			t.Errorf("经过 %v：得到 %q，期望 %q", tc.d, got, tc.want)
		}
	}
}

// TestRelativeTimeTextBoundaries 验证各档位临界点前后各一纳秒的归属。
func TestRelativeTimeTextBoundaries(t *testing.T) {
	now := relativeTimeBase()
	const ns = time.Nanosecond
	for _, tc := range []struct {
		name string
		d    time.Duration
		want string
	}{
		{"不足一分钟", time.Minute - ns, "刚刚"},
		{"恰好一分钟", time.Minute, "1 分钟前"},
		{"不足一小时", time.Hour - ns, "59 分钟前"},
		{"恰好一小时", time.Hour, "1 小时前"},
		{"不足一天", relativeTimeDay - ns, "23 小时前"},
		{"恰好一天", relativeTimeDay, "1 天前"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := relativeTimeText(now.Add(-tc.d), now); got != tc.want {
				t.Fatalf("经过 %v：得到 %q，期望 %q", tc.d, got, tc.want)
			}
		})
	}
}

// TestRelativeTimeTextNegative 验证负时间（then 晚于 now）统一收敛到"刚刚"，
// 绝不产生带负号的文案。
func TestRelativeTimeTextNegative(t *testing.T) {
	now := relativeTimeBase()
	for _, d := range []time.Duration{
		time.Nanosecond,
		time.Minute,
		time.Hour,
		100 * relativeTimeDay,
	} {
		if got := relativeTimeText(now.Add(d), now); got != "刚刚" {
			t.Errorf("超前 %v：得到 %q，期望 %q", d, got, "刚刚")
		}
	}
}

// TestRelativeTimeTextCJKIndependent 验证文案与区域设置、时区表示无关：
// 同一时刻用不同时区表达得到逐字节相同的结果，且重复调用保持稳定。
func TestRelativeTimeTextCJKIndependent(t *testing.T) {
	instant := relativeTimeBase()
	elapsed := 5*time.Hour + 30*time.Minute
	thenUTC := instant.Add(-elapsed)
	zone := time.FixedZone("CST", 8*3600)
	thenCJK := thenUTC.In(zone)
	nowCJK := instant.In(zone)

	const want = "5 小时前"
	if got := relativeTimeText(thenUTC, instant); got != want {
		t.Errorf("UTC 表示：得到 %q，期望 %q", got, want)
	}
	if got := relativeTimeText(thenCJK, nowCJK); got != want {
		t.Errorf("CJK 时区表示：得到 %q，期望 %q", got, want)
	}
	for i := 0; i < 3; i++ {
		if got := relativeTimeText(thenUTC, instant); got != want {
			t.Fatalf("第 %d 次调用：得到 %q，期望 %q", i, got, want)
		}
	}
}
