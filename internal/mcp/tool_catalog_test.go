package mcp

import (
	"sync"
	"testing"
)

// TestToolCatalogRevisionBumpAndRead 验证版本号从零开始，每次 Bump 都返回
// 加一后的新值，且与随后读取到的 Revision 一致。
func TestToolCatalogRevisionBumpAndRead(t *testing.T) {
	var rev ToolCatalogRevision
	if got := rev.Revision(); got != 0 {
		t.Fatalf("初始 Revision() = %d, want 0", got)
	}
	for want := uint64(1); want <= 3; want++ {
		if got := rev.Bump(); got != want {
			t.Fatalf("第 %d 次 Bump() = %d, want %d", want, got, want)
		}
		if got := rev.Revision(); got != want {
			t.Fatalf("第 %d 次 Bump 后 Revision() = %d, want %d", want, got, want)
		}
	}
}

// TestToolCatalogRevisionReset 验证 Reset 将版本号归零，且归零后再次递增
// 从一开始，重置前的递增次数不再影响读取结果。
func TestToolCatalogRevisionReset(t *testing.T) {
	var rev ToolCatalogRevision
	for i := 0; i < 5; i++ {
		rev.Bump()
	}
	if got := rev.Revision(); got != 5 {
		t.Fatalf("Reset 前 Revision() = %d, want 5", got)
	}
	rev.Reset()
	if got := rev.Revision(); got != 0 {
		t.Fatalf("Reset 后 Revision() = %d, want 0", got)
	}
	if got := rev.Bump(); got != 1 {
		t.Fatalf("Reset 后首次 Bump() = %d, want 1", got)
	}
	if got := rev.Revision(); got != 1 {
		t.Fatalf("Reset 后再次 Bump 的 Revision() = %d, want 1", got)
	}
}

// TestIsToolListChanged 验证通知方法识别：精确匹配目标方法，容忍首尾空白，
// 但拒绝大小写差异、中间空白、CJK 文本以及相似但不同的方法名。
func TestIsToolListChanged(t *testing.T) {
	tests := []struct {
		name   string
		method string
		want   bool
	}{
		{name: "精确匹配", method: "notifications/tools/list_changed", want: true},
		{name: "首尾空白", method: " \tnotifications/tools/list_changed\n ", want: true},
		{name: "全大写不匹配", method: "NOTIFICATIONS/TOOLS/LIST_CHANGED", want: false},
		{name: "首字母大写不匹配", method: "Notifications/tools/list_changed", want: false},
		{name: "方法名大写不匹配", method: "notifications/tools/LIST_CHANGED", want: false},
		{name: "中间空白不匹配", method: "notifications / tools/list_changed", want: false},
		{name: "内部换行不匹配", method: "notifications/tools/\nlist_changed", want: false},
		{name: "缺少前缀不匹配", method: "tools/list_changed", want: false},
		{name: "相似方法不匹配", method: "notifications/tools/list_changed/extra", want: false},
		{name: "CJK 文本不匹配", method: "工具列表已变更", want: false},
		{name: "CJK 混入不匹配", method: "notifications/工具/list_changed", want: false},
		{name: "全角空白被裁剪", method: "　notifications/tools/list_changed　", want: true},
		{name: "空串不匹配", method: "", want: false},
		{name: "仅空白不匹配", method: "   ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isToolListChanged(tt.method); got != tt.want {
				t.Fatalf("isToolListChanged(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

// TestToolCatalogRevisionConcurrentBump 在 -race 下验证并发 Bump 既无数据竞争，
// 又让每次调用返回唯一且连续的版本号，最终 Revision 等于总调用次数。
func TestToolCatalogRevisionConcurrentBump(t *testing.T) {
	const (
		goroutines   = 16
		perGoroutine = 100
	)
	total := goroutines * perGoroutine

	var rev ToolCatalogRevision
	values := make([]uint64, total)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				values[g*perGoroutine+i] = rev.Bump()
			}
		}(g)
	}
	wg.Wait()

	if got := rev.Revision(); got != uint64(total) {
		t.Fatalf("并发 Bump 后 Revision() = %d, want %d", got, total)
	}
	seen := make(map[uint64]bool, total)
	for i, v := range values {
		if seen[v] {
			t.Fatalf("Bump() 返回值重复: index %d, value %d", i, v)
		}
		seen[v] = true
	}
}

// TestToolCatalogRevisionConcurrentReadAndBump 在 -race 下验证并发读取
// Revision 与并发 Bump 可安全共存，且读取到的版本号不会超过最终值。
func TestToolCatalogRevisionConcurrentReadAndBump(t *testing.T) {
	const (
		bumpers      = 8
		readers      = 4
		perGoroutine = 200
	)

	var rev ToolCatalogRevision
	var bumpWG, readWG sync.WaitGroup
	stop := make(chan struct{})

	readWG.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = rev.Revision()
				}
			}
		}()
	}

	bumpWG.Add(bumpers)
	for i := 0; i < bumpers; i++ {
		go func() {
			defer bumpWG.Done()
			for j := 0; j < perGoroutine; j++ {
				rev.Bump()
			}
		}()
	}
	bumpWG.Wait()
	close(stop)
	readWG.Wait()

	want := uint64(bumpers * perGoroutine)
	if got := rev.Revision(); got != want {
		t.Fatalf("并发读写后 Revision() = %d, want %d", got, want)
	}
}
