package security

import (
	"fmt"
	"sync"
	"testing"
)

// 下面是可逆注册表各不变量的表格辅助类型，集中定义以避免重复声明匿名结构。
type reversibleReg struct {
	name  string
	value string
}

type reversibleLookup struct {
	name   string
	want   string
	wantOK bool
}

// TestReversibleRegistryRegisterLookup 覆盖注册与查找：空表、单条、多条、
// 同名重注册（以最近注册的存活值为准），并同时校验 Len 与 Snapshot。
func TestReversibleRegistryRegisterLookup(t *testing.T) {
	tests := []struct {
		name     string
		register []reversibleReg
		lookup   []reversibleLookup
		wantSnap map[string]string
		wantLen  int
	}{
		{
			name:    "空注册表",
			lookup:  []reversibleLookup{{name: "missing"}},
			wantLen: 0,
		},
		{
			name:     "单条注册",
			register: []reversibleReg{{name: "a", value: "A"}},
			lookup: []reversibleLookup{
				{name: "a", want: "A", wantOK: true},
				{name: "missing"},
			},
			wantSnap: map[string]string{"a": "A"},
			wantLen:  1,
		},
		{
			name: "多个不同名字",
			register: []reversibleReg{
				{name: "a", value: "A"},
				{name: "b", value: "B"},
				{name: "c", value: "C"},
			},
			lookup: []reversibleLookup{
				{name: "a", want: "A", wantOK: true},
				{name: "b", want: "B", wantOK: true},
				{name: "c", want: "C", wantOK: true},
			},
			wantSnap: map[string]string{"a": "A", "b": "B", "c": "C"},
			wantLen:  3,
		},
		{
			name: "同名重注册以最新值为准且分别计数",
			register: []reversibleReg{
				{name: "a", value: "first"},
				{name: "a", value: "second"},
			},
			lookup:   []reversibleLookup{{name: "a", want: "second", wantOK: true}},
			wantSnap: map[string]string{"a": "second"},
			wantLen:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReversibleRegistry[string]{}
			for _, reg := range tt.register {
				r.Register(reg.name, reg.value)
			}
			for _, q := range tt.lookup {
				got, ok := r.Lookup(q.name)
				if ok != q.wantOK || got != q.want {
					t.Errorf("Lookup(%q) = (%q, %v), want (%q, %v)", q.name, got, ok, q.want, q.wantOK)
				}
			}
			if got := r.Len(); got != tt.wantLen {
				t.Errorf("Len() = %d, want %d", got, tt.wantLen)
			}
			snap := r.Snapshot()
			if len(snap) != len(tt.wantSnap) {
				t.Errorf("Snapshot() = %v, want %v", snap, tt.wantSnap)
			}
			for name, want := range tt.wantSnap {
				if got := snap[name]; got != want {
					t.Errorf("Snapshot()[%q] = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestReversibleRegistryReleaseIdempotent 验证 release 幂等：重复调用不报错、
// 不越界删除，其它名字的注册必须完好无损。
func TestReversibleRegistryReleaseIdempotent(t *testing.T) {
	tests := []struct {
		name    string
		calls   int
		wantLen int
	}{
		{"只调用一次", 1, 1},
		{"重复调用五次", 5, 1},
		{"重复调用二十次", 20, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReversibleRegistry[string]{}
			releaseA := r.Register("a", "A")
			r.Register("other", "O")
			for i := 0; i < tt.calls; i++ {
				if err := releaseA(); err != nil {
					t.Fatalf("第 %d 次 release 返回错误: %v", i+1, err)
				}
			}
			if _, ok := r.Lookup("a"); ok {
				t.Error("release 后不应再查到条目 a")
			}
			if got, ok := r.Lookup("other"); !ok || got != "O" {
				t.Errorf("其它名字被误删: Lookup(other) = (%q, %v)", got, ok)
			}
			if got := r.Len(); got != tt.wantLen {
				t.Errorf("Len() = %d, want %d", got, tt.wantLen)
			}
		})
	}
}

// TestReversibleRegistrySameNameReleases 验证"release 只移除自己注册那次"：
// 同名注册两次产生两个独立的 release，无论先释放哪一个，另一个都不受影响；
// 先释放当前那次时回退到更早的存活值，两次都释放后该名字彻底消失。
func TestReversibleRegistrySameNameReleases(t *testing.T) {
	tests := []struct {
		name              string
		firstIsOld        bool
		wantFirst         string
		wantFirstOK       bool
		wantLenAfterFirst int
	}{
		{
			name:              "先释放早先那次不影响当前值",
			firstIsOld:        true,
			wantFirst:         "second",
			wantFirstOK:       true,
			wantLenAfterFirst: 1,
		},
		{
			name:              "先释放当前那次回退到早先值",
			firstIsOld:        false,
			wantFirst:         "first",
			wantFirstOK:       true,
			wantLenAfterFirst: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReversibleRegistry[string]{}
			releaseOld := r.Register("dup", "first")
			releaseNew := r.Register("dup", "second")
			if got := r.Len(); got != 2 {
				t.Fatalf("两次注册后 Len() = %d, want 2", got)
			}

			first, second := releaseNew, releaseOld
			if tt.firstIsOld {
				first, second = releaseOld, releaseNew
			}
			_ = first()
			if got, ok := r.Lookup("dup"); ok != tt.wantFirstOK || got != tt.wantFirst {
				t.Errorf("首次 release 后 Lookup(dup) = (%q, %v), want (%q, %v)", got, ok, tt.wantFirst, tt.wantFirstOK)
			}
			if got := r.Len(); got != tt.wantLenAfterFirst {
				t.Errorf("首次 release 后 Len() = %d, want %d", got, tt.wantLenAfterFirst)
			}
			if got := r.Snapshot()["dup"]; got != tt.wantFirst {
				t.Errorf("首次 release 后 Snapshot()[dup] = %q, want %q", got, tt.wantFirst)
			}

			_ = second()
			if _, ok := r.Lookup("dup"); ok {
				t.Error("两次 release 后不应再查到 dup")
			}
			if got := r.Len(); got != 0 {
				t.Errorf("两次 release 后 Len() = %d, want 0", got)
			}
			if got := len(r.Snapshot()); got != 0 {
				t.Errorf("两次 release 后 Snapshot() 长度 = %d, want 0", got)
			}

			// 全部释放后注册表仍可复用，不残留损坏的索引。
			releaseAgain := r.Register("dup", "again")
			if got, ok := r.Lookup("dup"); !ok || got != "again" {
				t.Errorf("复用注册失败: Lookup(dup) = (%q, %v)", got, ok)
			}
			_ = releaseAgain()
		})
	}
}

// TestReversibleRegistrySnapshotIndependent 验证 Snapshot 是独立副本：
// 修改副本不污染注册表，注册表后续变化也不渗入已返回的副本，且两份快照
// 之间不共享底层 map。
func TestReversibleRegistrySnapshotIndependent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"改动已有键", func(m map[string]string) { m["a"] = "tampered" }},
		{"新增键", func(m map[string]string) { m["new"] = "X" }},
		{"删除键", func(m map[string]string) { delete(m, "a") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReversibleRegistry[string]{}
			r.Register("a", "A")
			r.Register("b", "B")

			snap := r.Snapshot()
			tt.mutate(snap)

			// 修改副本不影响注册表。
			for name, want := range map[string]string{"a": "A", "b": "B"} {
				if got, ok := r.Lookup(name); !ok || got != want {
					t.Errorf("快照被修改后 Lookup(%q) = (%q, %v), want (%q, true)", name, got, ok, want)
				}
			}
			if got := r.Len(); got != 2 {
				t.Errorf("快照被修改后 Len() = %d, want 2", got)
			}

			// 注册表后续变化不渗入已返回的副本。
			r.Register("c", "C")
			if _, ok := snap["c"]; ok {
				t.Error("已返回的快照不应反映后续注册")
			}

			// 两份快照互不共享底层 map。
			fresh, control := r.Snapshot(), r.Snapshot()
			fresh["a"] = "mutated"
			if got := control["a"]; got != "A" {
				t.Errorf("两份快照共享了底层 map: control[a] = %q, want A", got)
			}
			if got, ok := r.Lookup("a"); !ok || got != "A" {
				t.Errorf("修改快照污染了注册表: Lookup(a) = (%q, %v)", got, ok)
			}
		})
	}
}

// TestReversibleRegistryConcurrentRegisterRelease 在并发下混合注册、查找、
// 快照与释放，并覆盖同名并发注册：结束后所有注册均被撤销，索引不残留。
func TestReversibleRegistryConcurrentRegisterRelease(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		release bool
		shared  bool
	}{
		{"8 协程只注册", 8, false, false},
		{"8 协程注册后释放", 8, true, false},
		{"64 协程注册后释放", 64, true, false},
		{"16 协程同名注册与释放", 16, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReversibleRegistry[int]{}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < tt.workers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					name := fmt.Sprintf("key-%d", i)
					if tt.shared {
						name = "shared"
					}
					release := r.Register(name, i)
					_, _ = r.Lookup(name)
					_ = r.Snapshot()
					_ = r.Len()
					if tt.release {
						_ = release()
						_ = release() // 并发下重复释放同样幂等
					}
				}(i)
			}
			close(start)
			wg.Wait()

			wantLen := tt.workers
			if tt.release {
				wantLen = 0
			}
			if got := r.Len(); got != wantLen {
				t.Fatalf("并发结束后 Len() = %d, want %d", got, wantLen)
			}
			if tt.shared {
				_, ok := r.Lookup("shared")
				if ok != !tt.release {
					t.Errorf("同名场景 Lookup(shared) 存在性 = %v, want %v", ok, !tt.release)
				}
				return
			}
			for i := 0; i < tt.workers; i++ {
				name := fmt.Sprintf("key-%d", i)
				got, ok := r.Lookup(name)
				if tt.release {
					if ok {
						t.Errorf("并发释放后仍查到 %q", name)
					}
				} else if !ok || got != i {
					t.Errorf("并发注册后 Lookup(%q) = (%d, %v), want (%d, true)", name, got, ok, i)
				}
			}
		})
	}
}

// TestReversibleRegistryZeroValue 验证零值可用：无需显式构造即可注册/查找，
// 空表的 Len 为 0、Snapshot 为空映射，释放后再次归零。
func TestReversibleRegistryZeroValue(t *testing.T) {
	tests := []struct {
		name    string
		wantLen int
		wantOK  bool
		wantVal string
	}{
		{"空零值", 0, false, ""},
		{"零值注册后", 1, true, "Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r ReversibleRegistry[string]
			if got := r.Len(); got != 0 {
				t.Errorf("零值 Len() = %d, want 0", got)
			}
			if snap := r.Snapshot(); len(snap) != 0 {
				t.Errorf("零值 Snapshot() = %v, want 空映射", snap)
			}
			if tt.wantLen == 1 {
				release := r.Register("z", "Z")
				got, ok := r.Lookup("z")
				if ok != tt.wantOK || got != tt.wantVal {
					t.Errorf("零值注册后 Lookup(z) = (%q, %v), want (%q, %v)", got, ok, tt.wantVal, tt.wantOK)
				}
				if got := r.Len(); got != tt.wantLen {
					t.Errorf("零值注册后 Len() = %d, want %d", got, tt.wantLen)
				}
				_ = r.Snapshot()
				_ = release()
				if got := r.Len(); got != 0 {
					t.Errorf("释放后 Len() = %d, want 0", got)
				}
			} else if _, ok := r.Lookup("z"); ok {
				t.Error("空零值不应查到条目")
			}
		})
	}
}
