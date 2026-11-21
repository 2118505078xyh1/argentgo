package server

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

func TestCapabilityBoardRegisterAndSet(t *testing.T) {
	tests := []struct {
		name string
		ops  func(b *CapabilityBoard)
		want []Capability
	}{
		{
			name: "zero value is empty",
			ops:  func(b *CapabilityBoard) {},
			want: []Capability{},
		},
		{
			name: "register single capability",
			ops:  func(b *CapabilityBoard) { b.Register("apply_patch", "tools", false) },
			want: []Capability{{Name: "apply_patch", Category: "tools", Enabled: false}},
		},
		{
			name: "set toggles registered capability",
			ops: func(b *CapabilityBoard) {
				b.Register("apply_patch", "tools", false)
				b.Set("apply_patch", true)
			},
			want: []Capability{{Name: "apply_patch", Category: "tools", Enabled: true}},
		},
		{
			name: "set can toggle back off",
			ops: func(b *CapabilityBoard) {
				b.Register("grep_ex", "search", true)
				b.Set("grep_ex", false)
			},
			want: []Capability{{Name: "grep_ex", Category: "search", Enabled: false}},
		},
		{
			name: "register overwrites full state",
			ops: func(b *CapabilityBoard) {
				b.Register("apply_patch", "old", true)
				b.Register("apply_patch", "new", false)
			},
			want: []Capability{{Name: "apply_patch", Category: "new", Enabled: false}},
		},
		{
			name: "set on unknown name is no-op",
			ops: func(b *CapabilityBoard) {
				b.Register("known", "tools", false)
				b.Set("unknown", true)
			},
			want: []Capability{{Name: "known", Category: "tools", Enabled: false}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b CapabilityBoard
			tt.ops(&b)
			if got := b.Snapshot(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Snapshot() = %#v, want %#v", got, tt.want)
			}
		})
	}

	// 零值 board 上 Set 必须安全。
	var zero CapabilityBoard
	zero.Set("anything", true)
	if got := zero.Snapshot(); len(got) != 0 {
		t.Fatalf("zero board Snapshot() = %#v, want empty", got)
	}
}

func TestCapabilityBoardSnapshotSorted(t *testing.T) {
	var b CapabilityBoard
	b.Register("zeta", "c", true)
	b.Register("alpha", "a", false)
	b.Register("mid", "b", true)
	b.Register("补丁", "工具", false)

	got := b.Snapshot()
	want := []Capability{
		{Name: "alpha", Category: "a", Enabled: false},
		{Name: "mid", Category: "b", Enabled: true},
		{Name: "zeta", Category: "c", Enabled: true},
		{Name: "补丁", Category: "工具", Enabled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Fatalf("Snapshot() 未按 Name 排序: %#v", got)
		}
	}
}

func TestCapabilityBoardSnapshotIsolation(t *testing.T) {
	var b CapabilityBoard
	b.Register("a", "cat", false)
	b.Register("b", "cat", true)

	snap := b.Snapshot()
	// 修改返回切片的元素与结构，都不应影响内部状态。
	snap[0].Name = "mutated"
	snap[0].Category = "mutated"
	snap[0].Enabled = true
	snap[1].Enabled = false
	snap = append(snap, Capability{Name: "injected"})

	want := []Capability{
		{Name: "a", Category: "cat", Enabled: false},
		{Name: "b", Category: "cat", Enabled: true},
	}
	if got := b.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改 Snapshot 影响了内部状态: %#v", got)
	}

	// 两次 Snapshot 之间也必须互不影响。
	first, second := b.Snapshot(), b.Snapshot()
	first[0].Name = "changed"
	if second[0].Name != "a" {
		t.Fatalf("两次 Snapshot 未隔离: %#v", second)
	}
}

func TestCapabilityBoardJSON(t *testing.T) {
	tests := []struct {
		name string
		ops  func(b *CapabilityBoard)
		want string
	}{
		{
			name: "zero value",
			ops:  func(b *CapabilityBoard) {},
			want: `{}`,
		},
		{
			name: "single capability",
			ops:  func(b *CapabilityBoard) { b.Register("apply_patch", "tools", false) },
			want: `{"apply_patch":{"category":"tools","enabled":false}}`,
		},
		{
			name: "keys sorted by capability name",
			ops: func(b *CapabilityBoard) {
				b.Register("zeta", "cat", true)
				b.Register("alpha", "b", false)
				b.Register("mid", "a", true)
			},
			want: `{"alpha":{"category":"b","enabled":false},"mid":{"category":"a","enabled":true},"zeta":{"category":"cat","enabled":true}}`,
		},
		{
			name: "set reflected in json",
			ops: func(b *CapabilityBoard) {
				b.Register("grep_ex", "search", false)
				b.Set("grep_ex", true)
			},
			want: `{"grep_ex":{"category":"search","enabled":true}}`,
		},
		{
			name: "CJK is not escaped",
			ops:  func(b *CapabilityBoard) { b.Register("补丁", "工具", false) },
			want: `{"补丁":{"category":"工具","enabled":false}}`,
		},
		{
			name: "HTML characters are not escaped",
			ops:  func(b *CapabilityBoard) { b.Register("view", "<tag>&", false) },
			want: `{"view":{"category":"<tag>&","enabled":false}}`,
		},
		{
			name: "empty name is allowed",
			ops:  func(b *CapabilityBoard) { b.Register("", "c", true) },
			want: `{"":{"category":"c","enabled":true}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b CapabilityBoard
			tt.ops(&b)
			if got := string(b.JSON()); got != tt.want {
				t.Fatalf("JSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCapabilityBoardJSONStableBytes(t *testing.T) {
	var a, b CapabilityBoard
	// 两个不同插入顺序的等价目录。
	a.Register("zeta", "c", true)
	a.Register("alpha", "a", false)
	a.Register("mid", "b", true)

	b.Register("mid", "b", true)
	b.Register("alpha", "a", false)
	b.Register("zeta", "c", true)

	first := a.JSON()
	second := a.JSON()
	other := b.JSON()

	if !bytes.Equal(first, second) {
		t.Fatalf("JSON() 不稳定: 第一次 %s, 第二次 %s", first, second)
	}
	if !bytes.Equal(first, other) {
		t.Fatalf("不同插入顺序产生不同字节: %s vs %s", first, other)
	}
	if bytes.HasSuffix(first, []byte("\n")) {
		t.Fatalf("JSON() 不应带尾换行: %q", first)
	}
}

func TestCapabilityBoardConcurrent(t *testing.T) {
	var b CapabilityBoard
	const (
		names      = 4
		iterations = 300
	)

	want := make([]Capability, 0, names)
	for i := 0; i < names; i++ {
		name := string(rune('a' + i))
		b.Register(name, "tools", false)
		want = append(want, Capability{Name: name, Category: "tools", Enabled: true})
	}

	var wg sync.WaitGroup
	for i := 0; i < names; i++ {
		// 每个 goroutine 只切换自己名字的开关，固定写同一值，结果确定。
		name := string(rune('a' + i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				b.Set(name, true)
				b.Register(name, "tools", true)
				_ = b.Snapshot()
				if body := b.JSON(); len(body) == 0 {
					t.Errorf("JSON() 返回空字节")
				}
			}
		}()
	}
	wg.Wait()

	if got := b.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("并发结束后 Snapshot() = %#v, want %#v", got, want)
	}
}
