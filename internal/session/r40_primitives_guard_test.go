package session

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// r40RunConcurrently 并行执行 fn 共 n 次并等待全部结束，
// 用于纯函数的并发只读守卫（fn 不得写入共享入参）。
func r40RunConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

// r40CloneStringMap 浅拷贝字符串 map，nil 保持 nil，用于入参不变性守卫。
func r40CloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// r40CloneRecords 深拷贝记录切片，包含 Data 底层字节。
func r40CloneRecords(in []Record) []Record {
	if in == nil {
		return nil
	}
	out := make([]Record, len(in))
	for i, r := range in {
		out[i] = r
		out[i].Data = slices.Clone(r.Data)
	}
	return out
}

// r40CloneTreeNodes 深拷贝树节点切片，包含 Children 底层切片。
func r40CloneTreeNodes(in []TreeNode) []TreeNode {
	if in == nil {
		return nil
	}
	out := make([]TreeNode, len(in))
	for i, n := range in {
		out[i] = n
		out[i].Children = slices.Clone(n.Children)
	}
	return out
}

// r40DeltaEqual 逐字段比较两个 ContextDelta：三个 key 切片与未变计数。
func r40DeltaEqual(a, b ContextDelta) bool {
	return slices.Equal(a.Added, b.Added) &&
		slices.Equal(a.Removed, b.Removed) &&
		slices.Equal(a.Changed, b.Changed) &&
		a.Unchanged() == b.Unchanged()
}

// TestR40GuardDiffContextDeterministicAndPure 锁定 DiffContextSnapshots 的
// 确定性、入参不变性与并发只读安全。
func TestR40GuardDiffContextDeterministicAndPure(t *testing.T) {
	cases := []struct{ before, after map[string]string }{
		{nil, nil},
		{map[string]string{"a": "1", "b": "2"}, map[string]string{"b": "X", "c": "3"}},
		{
			map[string]string{"用户画像": "旧", "工具清单": "同", "废弃": "删"},
			map[string]string{"用户画像": "新", "工具清单": "同", "新增": "加"},
		},
		{map[string]string{"k": ""}, map[string]string{"k": "v", "j": ""}},
	}
	for idx, tc := range cases {
		beforeSnap, afterSnap := r40CloneStringMap(tc.before), r40CloneStringMap(tc.after)
		want := DiffContextSnapshots(tc.before, tc.after)
		for rep := 0; rep < 64; rep++ {
			if got := DiffContextSnapshots(tc.before, tc.after); !r40DeltaEqual(got, want) {
				t.Fatalf("case %d rep %d: delta = %+v, want %+v", idx, rep, got, want)
			}
		}
		if !reflect.DeepEqual(tc.before, beforeSnap) || !reflect.DeepEqual(tc.after, afterSnap) {
			t.Fatalf("case %d: 入参被修改", idx)
		}
		r40RunConcurrently(16, func(_ int) {
			if got := DiffContextSnapshots(tc.before, tc.after); !r40DeltaEqual(got, want) {
				t.Errorf("case %d: 并发结果 = %+v, want %+v", idx, got, want)
			}
		})
	}
}

// TestR40GuardDiffContextCJKSpecialInputs 验证 CJK/emoji/组合字符/控制字符 key
// 被原样保留、稳定排序且 UTF-8 合法；特殊字符不会导致半个码点或结果漂移。
func TestR40GuardDiffContextCJKSpecialInputs(t *testing.T) {
	before := map[string]string{
		"中文":        "值甲",
		"emoji🚀":    "old",
		"组合a\u0301": "v1",
		"控制\x00key": "v2",
		"":          "空 key 合法",
	}
	after := map[string]string{
		"中文":        "值乙",
		"emoji🚀":    "new",
		"组合a\u0301": "v1",
		"新增🌙":       "v3",
	}

	want := DiffContextSnapshots(before, after)
	// 逐字段核对分类：新增/删除/修改/未变。
	// 三个切片均按 UTF-8 字节序稳定排序：空串最小，ASCII 的 'e' 小于汉字首字节。
	if !slices.Equal(want.Added, []string{"新增🌙"}) {
		t.Fatalf("Added = %v, want [新增🌙]", want.Added)
	}
	if !slices.Equal(want.Removed, []string{"", "控制\x00key"}) {
		t.Fatalf("Removed = %q, want [\"\", 控制key]", want.Removed)
	}
	if !slices.Equal(want.Changed, []string{"emoji🚀", "中文"}) {
		t.Fatalf("Changed = %v, want [emoji🚀 中文]", want.Changed)
	}
	if want.Unchanged() != 1 {
		t.Fatalf("Unchanged = %d, want 1", want.Unchanged())
	}
	for _, keys := range [][]string{want.Added, want.Removed, want.Changed} {
		for _, key := range keys {
			if !utf8.ValidString(key) {
				t.Errorf("key %q 被截断为非法 UTF-8", key)
			}
		}
	}
	// 排序按字节序稳定，重复调用不得漂移。
	for rep := 0; rep < 64; rep++ {
		if got := DiffContextSnapshots(before, after); !r40DeltaEqual(got, want) {
			t.Fatalf("rep %d: CJK 特殊输入结果漂移: %+v vs %+v", rep, got, want)
		}
	}
}

// TestR40GuardExportTreeDeterministicAndPure 锁定 ExportTree 的确定性、
// 入参（含 Data 字节）不变性与并发只读安全；覆盖环、重复 id、悬空 parent。
func TestR40GuardExportTreeDeterministicAndPure(t *testing.T) {
	records := []Record{
		{ID: "根", Parent: "", Kind: KindUser, Data: []byte(`{"content":"根"}`)},
		{ID: "子甲", Parent: "根", Kind: KindAssistant, Data: []byte("甲数据")},
		{ID: "子甲", Parent: "根", Kind: KindTool, Data: []byte("重复 id 应被忽略")},
		{ID: "孙", Parent: "子甲", Data: []byte{0x00, 0xff, 0x80}},
		{ID: "孤儿", Parent: "不存在", Data: []byte("orphan")},
		{ID: "回边A", Parent: "回边B", Data: []byte("A")},
		{ID: "回边B", Parent: "回边A", Data: []byte("B")},
	}
	snapshot := r40CloneRecords(records)
	want := ExportTree(records, "孙")

	for rep := 0; rep < 64; rep++ {
		got := ExportTree(records, "孙")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rep %d: ExportTree 结果不稳定:\n got %+v\nwant %+v", rep, got, want)
		}
		// 逐字段一致。
		for i := range got {
			if got[i].ID != want[i].ID || got[i].Parent != want[i].Parent ||
				got[i].IsActive != want[i].IsActive || got[i].Depth != want[i].Depth ||
				!slices.Equal(got[i].Children, want[i].Children) {
				t.Fatalf("rep %d node %d: 字段不一致: %+v vs %+v", rep, i, got[i], want[i])
			}
		}
	}
	if !reflect.DeepEqual(records, snapshot) {
		t.Fatalf("ExportTree 修改了入参:\n got %+v\nwant %+v", records, snapshot)
	}
	r40RunConcurrently(16, func(_ int) {
		if got := ExportTree(records, "孙"); !reflect.DeepEqual(got, want) {
			t.Errorf("并发 ExportTree 结果不一致")
		}
	})
}

// TestR40GuardRenderTreeDeterministicAndPure 锁定 RenderTreeASCII 的确定性、
// 入参（Children 切片）不变性与并发只读安全；验证 CJK/emoji 标签完整、无半码点。
func TestR40GuardRenderTreeDeterministicAndPure(t *testing.T) {
	records := []Record{
		{ID: "根", Parent: ""},
		{ID: "子节点🚀", Parent: "根"},
		{ID: "子节点乙", Parent: "根"},
		{ID: "孤环X", Parent: "孤环Y"},
		{ID: "孤环Y", Parent: "孤环X"},
	}
	nodes := ExportTree(records, "子节点🚀")
	// 追加包含未知子节点的手工节点，验证渲染对悬空引用安全。
	nodes = append(nodes, TreeNode{ID: "额外", Parent: "", Children: []string{"ghost"}})

	snapshot := r40CloneTreeNodes(nodes)
	want := RenderTreeASCII(nodes)
	for rep := 0; rep < 64; rep++ {
		if got := RenderTreeASCII(nodes); got != want {
			t.Fatalf("rep %d: RenderTreeASCII 结果不稳定:\n got:\n%s\nwant:\n%s", rep, got, want)
		}
	}
	if !reflect.DeepEqual(nodes, snapshot) {
		t.Fatalf("RenderTreeASCII 修改了入参 nodes")
	}
	if !utf8.ValidString(want) {
		t.Fatalf("渲染结果不是合法 UTF-8: %q", want)
	}
	for _, label := range []string{"根", "子节点🚀", "子节点乙", "孤环X", "孤环Y", asciiActive} {
		if !strings.Contains(want, label) {
			t.Errorf("渲染结果丢失标签 %q:\n%s", label, want)
		}
	}
	if strings.Contains(want, "ghost") {
		t.Errorf("未知子节点不应出现在渲染结果:\n%s", want)
	}
	r40RunConcurrently(16, func(_ int) {
		if got := RenderTreeASCII(nodes); got != want {
			t.Errorf("并发 RenderTreeASCII 结果不一致")
		}
	})
}

// TestR40GuardDiskUsageDeterministicAndSpecialNames 验证 MeasureDiskUsage 在
// CJK/emoji/空格/点文件/大写扩展名/嵌套目录下结果确定、分类正确且并发只读安全。
func TestR40GuardDiskUsageDeterministicAndSpecialNames(t *testing.T) {
	dir := t.TempDir()
	files := map[string]int{
		"会话记录.jsonl":      9,
		"归档/历史.TAR.GZ":    15,
		"emoji🚀.zip":      7,
		"带 空格 的文件.txt":    4,
		".隐藏文件":           3,
		"深层/子目录/更多.jsonl": 11,
	}
	for name, n := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	want, err := MeasureDiskUsage(dir)
	if err != nil {
		t.Fatalf("MeasureDiskUsage: %v", err)
	}
	if want.Files != len(files) {
		t.Fatalf("Files = %d, want %d", want.Files, len(files))
	}
	if want.Bytes != 9+15+7+4+3+11 {
		t.Fatalf("Bytes = %d, want %d", want.Bytes, 9+15+7+4+3+11)
	}
	if want.ByKind[diskKindSession] != 9+11 || want.ByKind[diskKindArchive] != 15+7 ||
		want.ByKind[diskKindOther] != 4+3 {
		t.Fatalf("ByKind = %v", want.ByKind)
	}
	for rep := 0; rep < 32; rep++ {
		got, err := MeasureDiskUsage(dir)
		if err != nil {
			t.Fatalf("rep %d: MeasureDiskUsage: %v", rep, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rep %d: 磁盘统计不稳定:\n got %+v\nwant %+v", rep, got, want)
		}
	}
	r40RunConcurrently(12, func(_ int) {
		got, err := MeasureDiskUsage(dir)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("并发 MeasureDiskUsage 结果不一致: %+v, err=%v", got, err)
		}
	})
}

// TestR40GuardDiskHelpersDeterministic 锁定 diskUsageKind 与 clampAddInt64
// 两个纯辅助函数的确定性与特殊输入安全。
func TestR40GuardDiskHelpersDeterministic(t *testing.T) {
	kinds := []struct {
		path string
		want string
	}{
		{"会话.JSONL", diskKindSession},
		{"a/b/c.tar.gz", diskKindArchive},
		{"a/b/c.ZIP", diskKindArchive},
		{"emoji🚀.txt", diskKindOther},
		{"no-ext", diskKindOther},
		{"weird.jsonl.bak", diskKindOther},
	}
	for _, tc := range kinds {
		want := diskUsageKind(tc.path)
		for rep := 0; rep < 16; rep++ {
			if got := diskUsageKind(tc.path); got != want {
				t.Fatalf("diskUsageKind(%q) rep %d = %q, want %q", tc.path, rep, got, want)
			}
		}
		if want != tc.want {
			t.Fatalf("diskUsageKind(%q) = %q, want %q", tc.path, want, tc.want)
		}
	}
	adds := [][2]int64{{0, 0}, {10, 5}, {math.MaxInt64 - 1, 1}, {math.MaxInt64, 1}, {7, -3}}
	for _, p := range adds {
		want := clampAddInt64(p[0], p[1])
		for rep := 0; rep < 16; rep++ {
			if got := clampAddInt64(p[0], p[1]); got != want {
				t.Fatalf("clampAddInt64(%d,%d) rep %d = %d, want %d", p[0], p[1], rep, got, want)
			}
		}
	}
}
