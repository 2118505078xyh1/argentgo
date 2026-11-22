package proc

import (
	"io"
	"sync/atomic"
	"time"
)

// activityWriter 在每次有字节写入时更新 lastActivity（Unix 纳秒），
// 供空闲看门狗判断“距上次输出多久”。它透传给内层 writer，不改变字节。
type activityWriter struct {
	inner io.Writer
	last  *atomic.Int64
}

func (w *activityWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.last.Store(time.Now().UnixNano())
	}
	return w.inner.Write(p)
}

// lastActivityTime 把原子纳秒还原为时间；零值返回零时间（从未有活动）。
func lastActivityTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}
