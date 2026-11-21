package update

import (
	"testing"
	"time"
)

func TestUpdateCacheFresh(t *testing.T) {
	base := time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC)
	const ttl = time.Hour

	cases := []struct {
		name      string
		lastCheck time.Time
		now       time.Time
		ttl       time.Duration
		want      bool
	}{
		// 新鲜：间隔非负且严格小于 ttl。
		{"间隔在TTL内", base, base.Add(30 * time.Minute), ttl, true},
		{"间隔为零", base, base, ttl, true},
		{"间隔恰为TTL减一纳秒", base, base.Add(ttl - time.Nanosecond), ttl, true},
		{"最小正TTL且零间隔", base, base, time.Nanosecond, true},

		// 过期：边界、超出与非正 ttl。
		{"恰好等于TTL视为过期", base, base.Add(ttl), ttl, false},
		{"超过TTL一纳秒视为过期", base, base.Add(ttl + time.Nanosecond), ttl, false},
		{"远超TTL视为过期", base, base.Add(24 * time.Hour), ttl, false},
		{"负TTL视为过期", base, base.Add(time.Second), -ttl, false},
		{"零TTL视为过期", base, base.Add(time.Nanosecond), 0, false},
		{"负TTL且零间隔视为过期", base, base, -time.Nanosecond, false},

		// 时间倒流：now 早于 lastCheck，间隔为负。
		{"时间倒流一秒视为过期", base, base.Add(-time.Second), ttl, false},
		{"时间倒流远超TTL视为过期", base, base.Add(-24 * time.Hour), ttl, false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := updateCacheFresh(test.lastCheck, test.now, test.ttl)
			if got != test.want {
				t.Fatalf("updateCacheFresh(%v, %v, %v) = %v, want %v",
					test.lastCheck, test.now, test.ttl, got, test.want)
			}
		})
	}
}

// TestUpdateCacheFreshWithMonotonicClock 验证函数同时适配带单调时钟读数与
// 仅含墙钟的时间值。time.Now 带有单调读数，Add 会保留并推进该读数；Round(0)
// 会去掉单调读数，仅保留墙钟。time.Time.Sub 在两者带单调读数时优先用它，
// 否则退化为墙钟差值，函数在这两种情形下都应给出一致结果。
func TestUpdateCacheFreshWithMonotonicClock(t *testing.T) {
	lastCheck := time.Now()
	now := lastCheck.Add(30 * time.Minute)
	const ttl = time.Hour

	if !updateCacheFresh(lastCheck, now, ttl) {
		t.Fatal("带单调读数且间隔在 TTL 内时应为新鲜")
	}
	if updateCacheFresh(lastCheck, lastCheck.Add(ttl), ttl) {
		t.Fatal("带单调读数且间隔恰好等于 TTL 时应过期")
	}

	strippedLast := lastCheck.Round(0)
	strippedNow := now.Round(0)
	if !updateCacheFresh(strippedLast, strippedNow, ttl) {
		t.Fatal("去掉单调读数后间隔在 TTL 内时应为新鲜")
	}
	if updateCacheFresh(strippedLast, strippedLast.Add(ttl), ttl) {
		t.Fatal("去掉单调读数后间隔恰好等于 TTL 时应过期")
	}
}
