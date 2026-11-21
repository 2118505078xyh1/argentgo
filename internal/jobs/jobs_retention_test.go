package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// expiredJob 构造一个只带 TTL 淘汰决策所需字段的终态作业视图。
func expiredJob(id string, started time.Time) JobInfo {
	return JobInfo{ID: id, Status: StatusExited, StartedAt: started}
}

func TestExpiredByTTL(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		jobs []JobInfo
		ttl  time.Duration
		want []string
	}{
		{
			name: "zero ttl disabled",
			jobs: []JobInfo{expiredJob("old", now.Add(-100*time.Hour))},
			ttl:  0,
			want: nil,
		},
		{
			name: "negative ttl disabled",
			jobs: []JobInfo{expiredJob("old", now.Add(-100*time.Hour))},
			ttl:  -time.Hour,
			want: nil,
		},
		{
			name: "old terminal expires",
			jobs: []JobInfo{expiredJob("old", now.Add(-2*time.Hour))},
			ttl:  time.Hour,
			want: []string{"old"},
		},
		{
			name: "recent terminal kept",
			jobs: []JobInfo{expiredJob("new", now.Add(-time.Minute))},
			ttl:  time.Hour,
			want: nil,
		},
		{
			name: "running never expires",
			jobs: []JobInfo{{ID: "run", Status: StatusRunning, StartedAt: now.Add(-100 * time.Hour)}},
			ttl:  time.Hour,
			want: nil,
		},
		{
			name: "killed counts as terminal",
			jobs: []JobInfo{{ID: "killed", Status: StatusKilled, StartedAt: now.Add(-2 * time.Hour)}},
			ttl:  time.Hour,
			want: []string{"killed"},
		},
		{
			name: "exact boundary expires",
			jobs: []JobInfo{expiredJob("edge", now.Add(-time.Hour))},
			ttl:  time.Hour,
			want: []string{"edge"},
		},
		{
			name: "future start kept",
			jobs: []JobInfo{expiredJob("future", now.Add(time.Hour))},
			ttl:  time.Hour,
			want: nil,
		},
		{
			name: "mixed returns sorted ids",
			jobs: []JobInfo{
				expiredJob("b", now.Add(-3*time.Hour)),
				{ID: "run", Status: StatusRunning, StartedAt: now.Add(-9 * time.Hour)}, // running: 被跳过
				expiredJob("a", now.Add(-2*time.Hour)),
				expiredJob("fresh", now),
			},
			ttl:  time.Hour,
			want: []string{"a", "b"},
		},
		{
			name: "nil jobs",
			jobs: nil,
			ttl:  time.Hour,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expiredByTTL(tt.jobs, now, tt.ttl)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectLRUEvictionCandidates(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	entry := func(id string, used time.Time, terminal bool) lruEntry {
		return lruEntry{id: id, lastUsed: used, terminal: terminal}
	}
	tests := []struct {
		name    string
		entries []lruEntry
		max     int
		want    []string
	}{
		{
			name:    "under limit",
			entries: []lruEntry{entry("a", base, true), entry("b", base.Add(time.Hour), false)},
			max:     3,
			want:    nil,
		},
		{
			name:    "exact limit",
			entries: []lruEntry{entry("a", base, true), entry("b", base.Add(time.Hour), false)},
			max:     2,
			want:    nil,
		},
		{
			name: "oldest used evicted first",
			entries: []lruEntry{
				entry("newest", base.Add(3*time.Hour), true),
				entry("oldest", base, true),
				entry("middle", base.Add(time.Hour), true),
			},
			max:  1,
			want: []string{"oldest", "middle"},
		},
		{
			name: "terminal preferred over running regardless of recency",
			entries: []lruEntry{
				entry("old-running", base, false),
				entry("new-exited", base.Add(3*time.Hour), true),
				entry("mid-killed", base.Add(2*time.Hour), true),
				entry("new-running", base.Add(4*time.Hour), false),
			},
			max:  2,
			want: []string{"mid-killed", "new-exited"},
		},
		{
			name: "equal times sorted by id",
			entries: []lruEntry{
				entry("b", base, true),
				entry("a", base, true),
			},
			max:  1,
			want: []string{"a"},
		},
		{
			name: "cjk names deterministic",
			entries: []lruEntry{
				entry("作业-北京", base.Add(2*time.Hour), false),
				entry("作业-上海", base, true),
				entry("作业-广州", base.Add(time.Hour), true),
			},
			max:  1,
			want: []string{"作业-上海", "作业-广州"},
		},
		{
			name:    "zero max clamps to all",
			entries: []lruEntry{entry("a", base, true), entry("b", base, true)},
			max:     0,
			want:    []string{"a", "b"},
		},
		{
			name:    "negative max clamps to all",
			entries: []lruEntry{entry("a", base, true), entry("b", base, true)},
			max:     -5,
			want:    []string{"a", "b"},
		},
		{
			name:    "nil entries",
			entries: nil,
			max:     3,
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]lruEntry(nil), tt.entries...)
			got := selectLRUEvictionCandidates(tt.entries, tt.max)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.entries, original) {
				t.Fatalf("input mutated: got %v, want %v", tt.entries, original)
			}
		})
	}
}

func TestBoundPreview(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxBytes int
		want     string
	}{
		{
			name:     "disabled unlimited",
			text:     "abcdefghij",
			maxBytes: 0,
			want:     "abcdefghij",
		},
		{
			name:     "negative disabled",
			text:     "abcdefghij",
			maxBytes: -1,
			want:     "abcdefghij",
		},
		{
			name:     "empty text",
			text:     "",
			maxBytes: 10,
			want:     "",
		},
		{
			name:     "shorter than limit unchanged",
			text:     "abc",
			maxBytes: 10,
			want:     "abc",
		},
		{
			name:     "exact fit unchanged",
			text:     "0123456789",
			maxBytes: 10,
			want:     "0123456789",
		},
		{
			name:     "ascii truncated with marker",
			text:     strings.Repeat("0123456789", 10), // 100 字节
			maxBytes: 30,
			// previewMarker 为 14 字节，30-14=16，取前 16 字节再追加标记。
			want: "0123456789012345" + previewMarker,
		},
		{
			name:     "limit smaller than marker uses plain rune-safe cut",
			text:     "0123456789",
			maxBytes: 5,
			want:     "01234",
		},
		{
			name:     "utf8 cut backs off to rune boundary",
			text:     "你好世界", // 12 字节
			maxBytes: 8,
			// 8 - 15 < 0，走纯截断：前 6 字节 = "你好"，第 7-9 字节是 "世" 的前两字节，回退到 6。
			want: "你好",
		},
		{
			name:     "utf8 with marker on boundary",
			text:     "你好世界abcdefghij", // 12 + 10 = 22 字节
			maxBytes: 20,
			// 20 - 14(marker) = 6，取前 6 字节 "你好"，正好落在 rune 边界。
			want: "你好" + previewMarker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boundPreview(tt.text, tt.maxBytes)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if tt.maxBytes > 0 && len(got) > tt.maxBytes {
				t.Fatalf("result %d bytes exceeds limit %d", len(got), tt.maxBytes)
			}
		})
	}
}

func TestEnforceTTLEvictsExpiredJobs(t *testing.T) {
	registry := newRegistry(t)
	now := time.Now().UTC()
	addJob := func(id string, status Status, started time.Time) {
		registry.jobs[id] = &Job{ID: id, Status: status, StartedAt: started}
	}
	addJob("old", StatusExited, now.Add(-2*time.Hour))
	addJob("killed-old", StatusKilled, now.Add(-3*time.Hour))
	addJob("fresh", StatusExited, now.Add(-time.Minute))
	addJob("running", StatusRunning, now.Add(-100*time.Hour))

	registry.SetRetention(RetentionConfig{TTL: time.Hour})
	got := registry.EnforceTTL()
	want := []string{"killed-old", "old"} // sort.Strings 按 ID 字节序
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evicted %v, want %v", got, want)
	}
	for _, id := range want {
		if _, ok := registry.jobs[id]; ok {
			t.Fatalf("%s should have been evicted", id)
		}
	}
	for _, id := range []string{"fresh", "running"} {
		if _, ok := registry.jobs[id]; !ok {
			t.Fatalf("%s should have been kept", id)
		}
	}
}

func TestEnforceLRUEvictsLeastRecentlyUsed(t *testing.T) {
	registry := newRegistry(t)
	base := time.Now().UTC().Add(-time.Hour)
	for _, j := range []struct {
		id string
		at time.Time
	}{
		{"a", base},
		{"b", base.Add(time.Minute)},
		{"c", base.Add(2 * time.Minute)},
	} {
		registry.jobs[j.id] = &Job{ID: j.id, Status: StatusExited, StartedAt: j.at}
	}
	registry.SetRetention(RetentionConfig{MaxEntries: 2})

	// 用确定性时间设置账本，避免依赖系统时钟分辨率造成偶发并列。
	registry.mu.Lock()
	registry.lastUsed["a"] = base.Add(10 * time.Minute)
	registry.lastUsed["b"] = base.Add(1 * time.Minute) // 最旧，应被淘汰
	registry.lastUsed["c"] = base.Add(11 * time.Minute)
	registry.mu.Unlock()

	got := registry.EnforceLRU()
	if want := []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("evicted %v, want %v", got, want)
	}
	if _, ok := registry.jobs["b"]; ok {
		t.Fatal("b should have been evicted")
	}
	for _, id := range []string{"a", "c"} {
		if _, ok := registry.jobs[id]; !ok {
			t.Fatalf("%s should have been kept", id)
		}
	}
}

func TestRetentionAccessTrackingUpdatesLRU(t *testing.T) {
	registry := newRegistry(t)
	base := time.Now().UTC().Add(-time.Hour)
	registry.jobs["a"] = &Job{ID: "a", Status: StatusExited, StartedAt: base}
	registry.jobs["b"] = &Job{ID: "b", Status: StatusExited, StartedAt: base}
	registry.SetRetention(RetentionConfig{MaxEntries: 1})

	// 把账本压到过去，随后 Get("a") 应只刷新 a。
	registry.mu.Lock()
	registry.lastUsed["a"] = base
	registry.lastUsed["b"] = base
	registry.mu.Unlock()

	if _, err := registry.Get("a"); err != nil {
		t.Fatalf("get a: %v", err)
	}

	registry.mu.Lock()
	aUsed, bUsed := registry.lastUsed["a"], registry.lastUsed["b"]
	registry.mu.Unlock()
	if !aUsed.After(base) {
		t.Fatalf("Get did not refresh lastUsed for a: %v", aUsed)
	}
	if !bUsed.Equal(base) {
		t.Fatalf("Get unexpectedly refreshed lastUsed for b: %v", bUsed)
	}

	// a 更新后成为最近使用，b 应被淘汰。
	if got, want := registry.EnforceLRU(), []string{"b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("evicted %v, want %v", got, want)
	}
}

func TestRetentionDisabledPreservesDefaultBehavior(t *testing.T) {
	registry := newRegistry(t)
	logPath := filepath.Join(registry.Dir(), "default.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	registry.jobs["a"] = &Job{
		ID: "a", Status: StatusExited, StartedAt: time.Now().UTC(), LogPath: logPath,
	}

	// 先落盘一次，作为"默认行为"的基准字节。
	registry.mu.Lock()
	snap, version := registry.changedLocked()
	registry.mu.Unlock()
	if err := registry.persist(snap, version); err != nil {
		t.Fatalf("persist: %v", err)
	}
	before, err := os.ReadFile(registry.indexPath())
	if err != nil {
		t.Fatalf("read index before: %v", err)
	}

	// 显式设置为零值，并调用所有新增入口；它们都应是空操作。
	registry.SetRetention(RetentionConfig{})
	if got := registry.EnforceRetention(); got != nil {
		t.Fatalf("EnforceRetention with zero config = %v, want nil", got)
	}
	if _, err := registry.List(); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := registry.Get("a"); err != nil {
		t.Fatalf("get: %v", err)
	}
	preview, err := registry.Preview("a", 50)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	tail, err := registry.Tail("a", 50)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if preview != tail {
		t.Fatalf("disabled Preview = %q, want Tail %q", preview, tail)
	}
	if len(registry.jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(registry.jobs))
	}

	after, err := os.ReadFile(registry.indexPath())
	if err != nil {
		t.Fatalf("read index after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("default behavior changed persisted bytes:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestPreviewTruncatesLogWithMarker(t *testing.T) {
	registry := newRegistry(t)
	logPath := filepath.Join(registry.Dir(), "preview.log")
	content := strings.Repeat("0123456789\n", 20) // 220 字节，Tail 后 219 字节
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	registry.jobs["p"] = &Job{ID: "p", Status: StatusExited, StartedAt: time.Now().UTC(), LogPath: logPath}

	full, err := registry.Tail("p", 1000)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	// 默认未开启：Preview 与 Tail 完全一致。
	def, err := registry.Preview("p", 1000)
	if err != nil {
		t.Fatalf("preview default: %v", err)
	}
	if def != full {
		t.Fatalf("default preview = %q, want full %q", def, full)
	}

	const limit = 50
	registry.SetRetention(RetentionConfig{PreviewBytes: limit})
	got, err := registry.Preview("p", 1000)
	if err != nil {
		t.Fatalf("preview bounded: %v", err)
	}
	if len(got) > limit {
		t.Fatalf("preview = %d bytes, want <= %d", len(got), limit)
	}
	if !strings.HasSuffix(got, previewMarker) {
		t.Fatalf("preview %q missing truncation marker", got)
	}
	if want := boundPreview(full, limit); got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestRetentionConcurrentAccess(t *testing.T) {
	registry := newRegistry(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("job%d", i)
		registry.jobs[id] = &Job{ID: id, Status: StatusExited, StartedAt: base.Add(time.Duration(i) * time.Second)}
	}
	// 只开启 LRU 容量约束（TTL 设为 24h，避免本测试窗口内过期）。
	registry.SetRetention(RetentionConfig{TTL: 24 * time.Hour, MaxEntries: 8, PreviewBytes: 64})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				_, _ = registry.List()
				_, _ = registry.Get("job0")
				_, _ = registry.Preview("job0", 10)
				_ = registry.EnforceRetention()
			}
		}()
	}
	wg.Wait()

	// 并发结束后账本应与存活条目一一对应，且数量不超过上限。
	list, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) > 8 {
		t.Fatalf("job count = %d, want <= 8", len(list))
	}
	registry.mu.Lock()
	ledger := len(registry.lastUsed)
	registry.mu.Unlock()
	if ledger != len(list) {
		t.Fatalf("LRU ledger = %d, want %d", ledger, len(list))
	}
}
