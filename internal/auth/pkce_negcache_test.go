package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// freshRejectedRefreshCache 重置进程级负缓存与时钟，并返回可推进的时钟指针。
// 测试结束恢复原值，避免污染同包其他用例。
func freshRejectedRefreshCache(t *testing.T) *time.Time {
	t.Helper()
	oldCache := rejectedRefreshCache
	oldClock := refreshNegCacheClock
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	rejectedRefreshCache = newTestRejectedRefreshCache()
	refreshNegCacheClock = func() time.Time { return now }
	t.Cleanup(func() {
		rejectedRefreshCache = oldCache
		refreshNegCacheClock = oldClock
	})
	return &now
}

func newTestRejectedRefreshCache() *refreshNegativeCache {
	return &refreshNegativeCache{entries: make(map[string]rejectedRefreshEntry)}
}

// TestRefreshRejectedNegativeCache 锁定 #48：被 invalid_grant 拒绝的刷新令牌
// 在五分钟内只交换一次，过期后自动重试；非拒绝类错误不写入负缓存。
func TestRefreshRejectedNegativeCache(t *testing.T) {
	now := freshRejectedRefreshCache(t)

	var exchanges atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
	}))
	defer server.Close()

	cfg := Config{Provider: "test", ClientID: "cid", TokenURL: server.URL}
	token := Token{Kind: KindOAuth, Access: "old", Refresh: "refresh-revoked"}

	_, firstErr := Refresh(context.Background(), cfg, token)
	if firstErr == nil {
		t.Fatal("first Refresh should fail with invalid_grant")
	}
	if !strings.Contains(firstErr.Error(), "invalid_grant") {
		t.Fatalf("first error = %v, want invalid_grant detail", firstErr)
	}
	if got := exchanges.Load(); got != 1 {
		t.Fatalf("exchanges after first call = %d, want 1", got)
	}

	// TTL 内重复调用：直接返回缓存错误，不再打端点。
	for i := 0; i < 3; i++ {
		_, err := Refresh(context.Background(), cfg, token)
		if err == nil || err.Error() != firstErr.Error() {
			t.Fatalf("cached Refresh[%d] error = %v, want %v", i, err, firstErr)
		}
	}
	if got := exchanges.Load(); got != 1 {
		t.Fatalf("exchanges within TTL = %d, want 1", got)
	}

	// 推进到 TTL 之后：缓存失效，重新交换一次。
	*now = now.Add(rejectedRefreshTTL + time.Second)
	_, err := Refresh(context.Background(), cfg, token)
	if err == nil {
		t.Fatal("Refresh after TTL should still fail invalid_grant")
	}
	if got := exchanges.Load(); got != 2 {
		t.Fatalf("exchanges after TTL = %d, want 2", got)
	}
}

// TestRefreshNegativeCacheIgnoresTransientErrors 锁定：5xx 这类短暂故障
// 不进入负缓存，每次调用都重新尝试。
func TestRefreshNegativeCacheIgnoresTransientErrors(t *testing.T) {
	freshRejectedRefreshCache(t)

	var exchanges atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error"}`))
	}))
	defer server.Close()

	cfg := Config{Provider: "test", ClientID: "cid", TokenURL: server.URL}
	token := Token{Kind: KindOAuth, Refresh: "transient"}

	for i := 0; i < 3; i++ {
		if _, err := Refresh(context.Background(), cfg, token); err == nil {
			t.Fatalf("Refresh[%d] should fail", i)
		}
	}
	if got := exchanges.Load(); got != 3 {
		t.Fatalf("exchanges for transient errors = %d, want 3", got)
	}
}

// TestRefreshNegativeCacheBounded 锁定：负缓存条目数有上限，不会无界增长。
func TestRefreshNegativeCacheBounded(t *testing.T) {
	freshRejectedRefreshCache(t)
	cache := rejectedRefreshCache
	now := refreshNegCacheClock()
	err := &tokenError{Status: 400, Code: "invalid_grant"}
	for i := 0; i < rejectedRefreshMaxEntries+25; i++ {
		cache.put(string(rune('a'+i%26))+string(rune('a'+i/26)), err, now)
	}
	cache.mu.Lock()
	got := len(cache.entries)
	cache.mu.Unlock()
	if got > rejectedRefreshMaxEntries {
		t.Fatalf("cache entries = %d, want <= %d", got, rejectedRefreshMaxEntries)
	}
}
