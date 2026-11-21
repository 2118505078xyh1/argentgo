package session

import (
	"sync"
	"time"
)

// 记录时间戳的生成必须严格递增：Windows 等平台的系统时钟在同一"tick"内会返回完全
// 相同的值（实测约每 0.5ms~几 ms 才前进一次）。当三个会话在同一 tick 内创建并追加时，
// 它们的记录 TS 完全相同，进而让 Meta.Updated 与文件 mtime 都相等；此时 List 只能回退到
// 随机后缀的 id 字典序，而随机后缀与创建/追加顺序毫无关联，"最新会话排最前"就退化成
// 抛硬币。这里用一个进程级的单调钳制，保证每次生成的时间戳都严格晚于上一次，使 Updated
// 本身就能稳定反映真实的追加顺序，List 的主排序键不再依赖有歧义的次级决胜。
var recordClock struct {
	mu   sync.Mutex
	last time.Time
}

// nextRecordTS 返回严格递增的 UTC 时间戳，用作新记录的 TS。
// 当 time.Now() 尚未越过上一次取值时，把它向前推 1 纳秒；并发调用由互斥锁串行化。
func nextRecordTS() time.Time {
	recordClock.mu.Lock()
	defer recordClock.mu.Unlock()
	ts := time.Now().UTC()
	if !ts.After(recordClock.last) {
		ts = recordClock.last.Add(time.Nanosecond)
	}
	recordClock.last = ts
	return ts
}
