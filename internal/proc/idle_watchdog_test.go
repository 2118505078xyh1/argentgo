package proc

import (
	"testing"
	"time"
)

func TestIdleWatchdogExpired(t *testing.T) {
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		lastActivity time.Time
		now          time.Time
		idleTimeout  time.Duration
		want         bool
	}{
		{
			name:         "未过期：静默时长小于阈值",
			lastActivity: base,
			now:          base.Add(time.Second),
			idleTimeout:  5 * time.Second,
			want:         false,
		},
		{
			name:         "过期：静默时长超过阈值",
			lastActivity: base,
			now:          base.Add(6 * time.Second),
			idleTimeout:  5 * time.Second,
			want:         true,
		},
		{
			name:         "边界恰好：静默时长等于阈值不判过期",
			lastActivity: base,
			now:          base.Add(5 * time.Second),
			idleTimeout:  5 * time.Second,
			want:         false,
		},
		{
			name:         "负超时：看门狗关闭",
			lastActivity: base,
			now:          base.Add(time.Hour),
			idleTimeout:  -time.Second,
			want:         false,
		},
		{
			name:         "零超时：看门狗关闭",
			lastActivity: base,
			now:          base.Add(time.Hour),
			idleTimeout:  0,
			want:         false,
		},
		{
			name:         "时间倒流：now 早于 lastActivity",
			lastActivity: base,
			now:          base.Add(-time.Second),
			idleTimeout:  5 * time.Second,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idleWatchdogExpired(tt.lastActivity, tt.now, tt.idleTimeout); got != tt.want {
				t.Errorf("idleWatchdogExpired(%v, %v, %s) = %t，期望 %t",
					tt.lastActivity, tt.now, tt.idleTimeout, got, tt.want)
			}
		})
	}
}

func TestIdleWatchdogExpiredMonotonicClock(t *testing.T) {
	// 用 time.Now() 及其 Add 构造带单调时钟读数的时刻：
	// 与基于 time.Date 的用例不同，这条路径会走 Sub 的单调分支。
	lastActivity := time.Now()
	now := lastActivity.Add(3 * time.Second)

	if !idleWatchdogExpired(lastActivity, now, 2*time.Second) {
		t.Error("3s 静默超过 2s 阈值应判过期（单调时钟路径）")
	}
	if idleWatchdogExpired(lastActivity, now, 5*time.Second) {
		t.Error("3s 静默未超过 5s 阈值不应判过期（单调时钟路径）")
	}
}
