package security

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTTLCacheExpiredCleanupKeepsConcurrentlyRenewedGrant 覆盖这样一种竞态：
// 观察者的内存中授予副本已过期，而另一个实例
// 在观察者正要获取跨进程锁时（在跨进程锁下）续期了同一条授予。
// 清理在删除前必须重新检查磁盘上的最新
// 过期时间：无条件删除会抹掉刚刚写入的新授予，
// 这样本进程和新进程都不会认可它。
func TestTTLCacheExpiredCleanupKeepsConcurrentlyRenewedGrant(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Truncate(time.Second)
	const ttl = time.Hour
	expiredAt := base.Add(ttl + time.Second)

	writer := newTTLTestCache(t, dir, ttl, base)
	key := writer.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	if err := writer.Allow(key, "original"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	// 观察者在原始授予仍有效时加载了它。
	observer := NewTTLCache(dir, ttl)
	observer.now = func() time.Time { return base }
	if !observer.IsAllowed(key) {
		t.Fatal("observer should see the original grant before its expiry")
	}

	// 把观察者的时钟钉在原始过期时间之后。它第一次读时钟是在 IsAllowed 中，
	// 发生在到达跨进程锁之前；在那里一直保持，
	// 直到续期者为同一个键写入了一条新的授予。
	started := make(chan struct{})
	renewed := make(chan struct{})
	var once sync.Once
	observer.now = func() time.Time {
		once.Do(func() {
			close(started)
			<-renewed
		})
		return expiredAt
	}

	allowed := make(chan bool, 1)
	go func() { allowed <- observer.IsAllowed(key) }()
	<-started

	renewer := newTTLTestCache(t, dir, ttl, expiredAt)
	if err := renewer.Allow(key, "renewed"); err != nil {
		t.Fatalf("renewer Allow: %v", err)
	}
	close(renewed)

	if got := <-allowed; !got {
		t.Fatal("IsAllowed must honour a grant that another instance renewed concurrently")
	}

	// 新进程必须仍能在磁盘上看到续期后的授予：过期清理
	// 绝不能覆盖它。
	final := newTTLTestCache(t, dir, ttl, expiredAt)
	if !final.IsAllowed(key) {
		t.Fatal("the renewed grant was erased from disk by the expired cleanup")
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if !strings.Contains(string(data), key) {
		t.Fatalf("renewed key missing from grants file: %s", data)
	}
}
