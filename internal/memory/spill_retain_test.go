package memory

import (
	"reflect"
	"testing"
)

// sf 构造一个只带决策所需字段的溢出工件视图。
func sf(name string, size, lastUsed int64) SpillFile {
	return SpillFile{Name: name, Size: size, LastUsed: lastUsed}
}

// TestPlanSpillRetention 表驱动验证回收决策：容量淘汰、字节淘汰、pinned 保护、
// 钉住超限、组合约束、零限制全保留、CJK 文件名、未超限不淘汰以及零值 stats。
func TestPlanSpillRetention(t *testing.T) {
	shuffled := []SpillFile{
		sf("c.txt", 100, 300),
		sf("a.txt", 100, 100),
		sf("d.txt", 100, 400),
		sf("b.txt", 100, 200),
	}
	statsShuffled := SpillStats{Count: 4, TotalBytes: 400, OldestTS: 100, NewestTS: 400}

	cases := []struct {
		name     string
		stats    SpillStats
		files    []SpillFile
		maxCount int
		maxBytes int64
		pinned   map[string]bool
		want     RetainDecision
	}{
		{
			name:     "容量淘汰最旧两个",
			stats:    statsShuffled,
			files:    shuffled,
			maxCount: 2,
			want: RetainDecision{
				Keep:  []string{"c.txt", "d.txt"},
				Evict: []string{"a.txt", "b.txt"},
			},
		},
		{
			name:     "字节淘汰直到达标",
			files:    []SpillFile{sf("big-a.txt", 60, 100), sf("small-b.txt", 30, 200), sf("big-c.txt", 50, 300)},
			maxBytes: 80,
			want: RetainDecision{
				Keep:  []string{"small-b.txt", "big-c.txt"},
				Evict: []string{"big-a.txt"},
			},
		},
		{
			name:     "字节淘汰从最旧开始且已淘汰不再计入",
			files:    []SpillFile{sf("a", 40, 100), sf("b", 40, 200), sf("c", 40, 300)},
			maxCount: 2, // 先淘汰 a，余下 b+c=80
			maxBytes: 50,
			want: RetainDecision{
				Keep:  []string{"c"},
				Evict: []string{"a", "b"},
			},
		},
		{
			name:     "pinned 保护但其余仍按字节淘汰",
			files:    []SpillFile{sf("old.txt", 100, 100), sf("pin.txt", 100, 200), sf("new.txt", 100, 300)},
			maxCount: 2,
			maxBytes: 150,
			pinned:   map[string]bool{"pin.txt": true},
			want: RetainDecision{
				Keep:  []string{"pin.txt"},
				Evict: []string{"old.txt", "new.txt"},
			},
		},
		{
			name:     "pinned 自身超限也不淘汰",
			files:    []SpillFile{sf("pin.txt", 1000, 100), sf("x.txt", 10, 200)},
			maxCount: 1,
			maxBytes: 100,
			pinned:   map[string]bool{"pin.txt": true},
			want: RetainDecision{
				Keep:  []string{"pin.txt"},
				Evict: []string{"x.txt"},
			},
		},
		{
			name:     "pinned 数超过上限时淘汰全部非钉住工件",
			files:    []SpillFile{sf("p1", 1, 100), sf("p2", 1, 200), sf("free", 1, 300)},
			maxCount: 1,
			pinned:   map[string]bool{"p1": true, "p2": true},
			want: RetainDecision{
				Keep:  []string{"p1", "p2"},
				Evict: []string{"free"},
			},
		},
		{
			name:     "组合约束先容量后字节",
			files:    []SpillFile{sf("f1", 100, 100), sf("f2", 100, 200), sf("f3", 100, 300), sf("f4", 100, 400), sf("f5", 100, 500)},
			maxCount: 4,
			maxBytes: 250,
			want: RetainDecision{
				Keep:  []string{"f4", "f5"},
				Evict: []string{"f1", "f2", "f3"},
			},
		},
		{
			name:  "零限制全保留并按最近使用升序排列",
			files: shuffled,
			want: RetainDecision{
				Keep: []string{"a.txt", "b.txt", "c.txt", "d.txt"},
			},
		},
		{
			name:     "仅字节限制零 stats 仍按 files 决策",
			stats:    SpillStats{},
			files:    shuffled,
			maxBytes: 250,
			want: RetainDecision{
				Keep:  []string{"c.txt", "d.txt"},
				Evict: []string{"a.txt", "b.txt"},
			},
		},
		{
			name:     "未超限不淘汰",
			files:    []SpillFile{sf("a", 10, 100), sf("b", 20, 200), sf("c", 30, 300)},
			stats:    SpillStats{Count: 3, TotalBytes: 60},
			maxCount: 5,
			maxBytes: 1000,
			want: RetainDecision{
				Keep: []string{"a", "b", "c"},
			},
		},
		{
			name:     "CJK 文件名",
			files:    []SpillFile{sf("附件-北京.txt", 100, 300), sf("附件-上海.txt", 100, 100), sf("附件-广州.txt", 100, 200)},
			maxCount: 2,
			want: RetainDecision{
				Keep:  []string{"附件-广州.txt", "附件-北京.txt"},
				Evict: []string{"附件-上海.txt"},
			},
		},
		{
			name:     "pinned 中的名字不在 files 时不影响结果",
			files:    []SpillFile{sf("a", 1, 100), sf("b", 1, 200)},
			maxCount: 1,
			pinned:   map[string]bool{"ghost": true},
			want: RetainDecision{
				Keep:  []string{"b"},
				Evict: []string{"a"},
			},
		},
		{
			name:  "空清单返回空决策",
			files: nil,
			want:  RetainDecision{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanSpillRetention(tc.stats, tc.files, tc.maxCount, tc.maxBytes, tc.pinned)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlanSpillRetention() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPlanSpillRetentionStableWithEqualLastUsed 验证稳定排序：LastUsed 相同
// 时保持输入中的相对顺序，最旧（最先出现）的先被淘汰，保留组也不打乱次序。
func TestPlanSpillRetentionStableWithEqualLastUsed(t *testing.T) {
	files := []SpillFile{
		sf("first", 10, 100),
		sf("second", 10, 100),
		sf("third", 10, 100),
		sf("older", 10, 50),
	}
	want := RetainDecision{
		Keep:  []string{"second", "third"},
		Evict: []string{"older", "first"},
	}
	got := PlanSpillRetention(SpillStats{Count: 4, TotalBytes: 40}, files, 2, 0, nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanSpillRetention() = %+v, want %+v", got, want)
	}
}

// TestPlanSpillRetentionDoesNotMutateInput 验证 copy 后排序：调用不修改 files
// 切片的顺序与内容，也不修改 pinned 映射。
func TestPlanSpillRetentionDoesNotMutateInput(t *testing.T) {
	files := []SpillFile{
		sf("c", 100, 300),
		sf("a", 100, 100),
		sf("b", 100, 200),
	}
	originalFiles := append([]SpillFile(nil), files...)
	pinned := map[string]bool{"b": true}
	originalPinned := map[string]bool{"b": true}

	_ = PlanSpillRetention(SpillStats{Count: 3, TotalBytes: 300}, files, 1, 150, pinned)

	if !reflect.DeepEqual(files, originalFiles) {
		t.Fatalf("files 被修改: got %v, want %v", files, originalFiles)
	}
	if !reflect.DeepEqual(pinned, originalPinned) {
		t.Fatalf("pinned 被修改: got %v, want %v", pinned, originalPinned)
	}
}
